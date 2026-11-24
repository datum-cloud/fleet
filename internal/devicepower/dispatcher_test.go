package devicepower

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
)

const workerImage = "ghcr.io/datum-cloud/fleet@sha256:0000000000000000000000000000000000000000000000000000000000000000"

type dispatchHarness struct {
	milo, site client.Client
	now        time.Time
	mu         sync.Mutex
}

func newDispatchHarness(t *testing.T, objs ...client.Object) *dispatchHarness {
	t.Helper()
	scheme, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	milo := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&inventory.DevicePowerOperation{}, &inventory.Device{}).Build()
	site := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&batchv1.Job{}).Build()
	return &dispatchHarness{milo: milo, site: site, now: t0}
}

func (h *dispatchHarness) reconciler() *Reconciler {
	c := Config{Project: project, MiloKubeconfig: "unused", DeviceNamespaces: []string{deviceNS}, WorkerNamespace: siteNS, WorkerImage: workerImage}
	c.Default()
	c.DiscoveryMaxAge.Duration = time.Hour
	return &Reconciler{Milo: h.milo, MiloReader: h.milo, Site: h.site, SiteReader: h.site, Namespace: siteNS, Config: c,
		Now: func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }}
}

// settle reconciles one operation until it asks for no immediate requeue.
func (h *dispatchHarness) settle(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	for range 10 {
		result, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: deviceNS, Name: name}})
		if err != nil && !apierrors.IsConflict(err) {
			t.Fatal(err)
		}
		if err == nil && !result.Requeue {
			return
		}
	}
	t.Fatal("reconcile did not settle")
}

func (h *dispatchHarness) op(t *testing.T, name string) *inventory.DevicePowerOperation {
	t.Helper()
	var op inventory.DevicePowerOperation
	if err := h.milo.Get(context.Background(), types.NamespacedName{Namespace: deviceNS, Name: name}, &op); err != nil {
		t.Fatal(err)
	}
	return &op
}

func (h *dispatchHarness) count(t *testing.T, list client.ObjectList) int {
	t.Helper()
	if err := h.site.List(context.Background(), list, client.InNamespace(siteNS)); err != nil {
		t.Fatal(err)
	}
	return meta.LenList(list)
}

// setProgressOf plays the worker's part on the stage record.
func (h *dispatchHarness) setProgressOf(t *testing.T, op *inventory.DevicePowerOperation, mutate func(*Progress)) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := h.site.Get(context.Background(), types.NamespacedName{Namespace: siteNS, Name: WorkerName(op.UID)}, &cm); err != nil {
		t.Fatal(err)
	}
	_, p, err := decodeRecord(&cm)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&p)
	setProgress(&cm, p)
	if err := h.site.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
}

func (h *dispatchHarness) failJob(t *testing.T, op *inventory.DevicePowerOperation) {
	t.Helper()
	var job batchv1.Job
	if err := h.site.Get(context.Background(), types.NamespacedName{Namespace: siteNS, Name: WorkerName(op.UID)}, &job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue})
	if err := h.site.Status().Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
}

func finishedReason(op *inventory.DevicePowerOperation) string {
	if c := meta.FindStatusCondition(op.Status.Conditions, inventory.ConditionPowerFinished); c != nil && c.Status == metav1.ConditionTrue {
		return c.Reason
	}
	return ""
}

func TestDispatcherStartsOneBoundedWorker(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerForceRestart))
	r := h.reconciler()
	h.settle(t, r, "op")
	op := h.op(t, "op")
	if !meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerAccepted) || !meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerExecuting) || op.Status.Result != "" {
		t.Fatalf("status = %+v", op.Status)
	}
	name := types.NamespacedName{Namespace: siteNS, Name: WorkerName(op.UID)}
	var rec corev1.ConfigMap
	if err := h.site.Get(context.Background(), name, &rec); err != nil {
		t.Fatal(err)
	}
	in, p, err := decodeRecord(&rec)
	if err != nil || p.Stage != StagePending || in.Endpoint != endpoint || in.SystemUUID != uuidA || in.PublicKeySHA256 != keyA || in.Action != inventory.PowerForceRestart || !in.CapacityCheckedAt.Time.Equal(t0) {
		t.Fatalf("record = %+v %+v %v", in, p, err)
	}
	var job batchv1.Job
	if err := h.site.Get(context.Background(), name, &job); err != nil {
		t.Fatal(err)
	}
	spec := job.Spec.Template.Spec
	if *job.Spec.BackoffLimit != 0 || *job.Spec.PodReplacementPolicy != batchv1.Failed || spec.ServiceAccountName != name.Name || len(spec.Containers) != 1 ||
		spec.Containers[0].Image != workerImage || spec.HostNetwork || spec.HostPID || spec.HostIPC || len(spec.Volumes) != 1 ||
		spec.Volumes[0].Secret.SecretName != "bmc.default.device-a" || !*spec.Volumes[0].Secret.Optional || len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != rec.UID {
		t.Fatalf("job = %+v", job.Spec)
	}
	var role rbacv1.Role
	if err := h.site.Get(context.Background(), name, &role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 || !slices.Equal(role.Rules[0].Resources, []string{"configmaps"}) || !slices.Equal(role.Rules[0].ResourceNames, []string{name.Name}) ||
		!slices.Equal(role.Rules[0].Verbs, []string{"get", "update"}) {
		t.Fatalf("worker role = %+v", role.Rules)
	}
	// More passes while the worker runs start nothing new.
	for range 3 {
		h.settle(t, r, "op")
	}
	if n := h.count(t, &batchv1.JobList{}); n != 1 {
		t.Fatalf("%d Jobs", n)
	}
}

func TestDispatcherRefusalsSendNothing(t *testing.T) {
	retired := discoveredDevice("device-r")
	retired.Spec.Lifecycle = inventory.DeviceRetired
	for _, tc := range []struct {
		op     *inventory.DevicePowerOperation
		objs   []client.Object
		reason string
	}{
		{operation("op", "device-a", inventory.PowerOn), []client.Object{discoveredDevice("device-a"), capacity("cap", "device-a")}, inventory.ReasonCapacityBacked},
		{operation("op", "missing", inventory.PowerOn), nil, inventory.ReasonDeviceNotFound},
		{operation("op", "device-r", inventory.PowerOn), []client.Object{retired}, inventory.ReasonDeviceRetired},
		{operation("op", "device-a", inventory.PowerCycle), []client.Object{discoveredDevice("device-a")}, inventory.ReasonResetTypeNotOffered},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			h := newDispatchHarness(t, append(tc.objs, tc.op)...)
			h.settle(t, h.reconciler(), "op")
			op := h.op(t, "op")
			if op.Status.Result != inventory.PowerFailed || finishedReason(op) != tc.reason || meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerAccepted) {
				t.Fatalf("status = %+v", op.Status)
			}
			if n := h.count(t, &corev1.ConfigMapList{}) + h.count(t, &batchv1.JobList{}) + h.count(t, &coordinationv1.LeaseList{}); n != 0 {
				t.Fatalf("%d site objects for a refused operation", n)
			}
		})
	}
}

func TestDispatcherRefusesAWrongSystemUUID(t *testing.T) {
	op := operation("op", "device-a", inventory.PowerOn)
	op.Spec.ExpectedSystemUUID = uuidB
	h := newDispatchHarness(t, discoveredDevice("device-a"), op)
	h.settle(t, h.reconciler(), "op")
	if got := h.op(t, "op"); got.Status.Result != inventory.PowerFailed || finishedReason(got) != inventory.ReasonSystemUUIDMismatch {
		t.Fatalf("status = %+v", got.Status)
	}
}

// Capacity defined after acceptance is caught by the re-check just before
// the worker starts.
func TestCapacityRecheckBeforeTheWorker(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerOn))
	r := h.reconciler()
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: deviceNS, Name: "op"}}); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(h.op(t, "op").Status.Conditions, inventory.ConditionPowerAccepted) {
		t.Fatal("not accepted")
	}
	if err := h.milo.Create(context.Background(), capacity("cap", "device-a")); err != nil {
		t.Fatal(err)
	}
	h.settle(t, r, "op")
	op := h.op(t, "op")
	if op.Status.Result != inventory.PowerFailed || finishedReason(op) != inventory.ReasonCapacityBacked {
		t.Fatalf("status = %+v", op.Status)
	}
	if n := h.count(t, &corev1.ConfigMapList{}) + h.count(t, &batchv1.JobList{}) + h.count(t, &coordinationv1.LeaseList{}); n != 0 {
		t.Fatalf("%d site objects left", n)
	}
}

func TestDispatcherPublishesTheWorkersResult(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerForceOff))
	r := h.reconciler()
	h.settle(t, r, "op")
	op := h.op(t, "op")
	sent, done := metav1.NewTime(t0.Add(time.Second)), metav1.NewTime(t0.Add(20*time.Second))
	h.setProgressOf(t, op, func(p *Progress) {
		*p = Progress{Stage: StageFinished, ClaimedBy: "pod", Result: inventory.PowerSucceeded, Reason: ReasonRequestedStateReached, Message: "the server reached Off",
			RequestSentAt: &sent, FinishedAt: &done, Before: &inventory.PowerObservation{State: "On", ObservedAt: sent}, After: &inventory.PowerObservation{State: "Off", ObservedAt: done}}
	})
	// A dispatcher restarted from nothing picks the operation up from the
	// record.
	h.settle(t, h.reconciler(), "op")
	op = h.op(t, "op")
	if op.Status.Result != inventory.PowerSucceeded || finishedReason(op) != ReasonRequestedStateReached || !op.Status.RequestSentAt.Equal(&sent) ||
		op.Status.Power.Before.State != "On" || op.Status.Power.After.State != "Off" || meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerExecuting) {
		t.Fatalf("status = %+v", op.Status)
	}
	if n := h.count(t, &coordinationv1.LeaseList{}); n != 0 {
		t.Fatal("the Device lock was not released")
	}
}

func TestLostWorkerOutcomes(t *testing.T) {
	for _, tc := range []struct {
		stage  Stage
		result inventory.PowerResult
		reason string
	}{
		{StagePending, inventory.PowerFailed, ReasonWorkerNotStarted},
		{StageClaimed, inventory.PowerFailed, ReasonWorkerLost},
		{StageSending, inventory.PowerUnknown, ReasonWorkerLost},
		{StageSent, inventory.PowerUnknown, ReasonWorkerLost},
	} {
		t.Run(string(tc.stage), func(t *testing.T) {
			h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerForceRestart))
			r := h.reconciler()
			h.settle(t, r, "op")
			op := h.op(t, "op")
			h.setProgressOf(t, op, func(p *Progress) { p.Stage = tc.stage })
			h.failJob(t, op)
			h.settle(t, r, "op")
			op = h.op(t, "op")
			if op.Status.Result != tc.result || finishedReason(op) != tc.reason {
				t.Fatalf("status = %+v", op.Status)
			}
			var cm corev1.ConfigMap
			_ = h.site.Get(context.Background(), types.NamespacedName{Namespace: siteNS, Name: WorkerName(op.UID)}, &cm)
			if _, p, _ := decodeRecord(&cm); p.Stage != StageAbandoned || p.Result != tc.result {
				t.Fatalf("record not fenced: %+v", p)
			}
		})
	}
}

// A Job deleted mid-operation is not recreated once the record was claimed:
// the outcome follows the record.
func TestDeletedJobAfterTheClaimIsNotRestarted(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerOn))
	r := h.reconciler()
	h.settle(t, r, "op")
	op := h.op(t, "op")
	h.setProgressOf(t, op, func(p *Progress) { p.Stage = StageSent; p.SendOutcome = SendLost })
	if err := h.site.Delete(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: siteNS, Name: WorkerName(op.UID)}}); err != nil {
		t.Fatal(err)
	}
	h.settle(t, r, "op")
	if got := h.op(t, "op"); got.Status.Result != inventory.PowerUnknown || finishedReason(got) != ReasonWorkerLost {
		t.Fatalf("status = %+v", got.Status)
	}
	if n := h.count(t, &batchv1.JobList{}); n != 0 {
		t.Fatalf("%d Jobs were recreated", n)
	}
}

func TestStartedOperationWithoutARecordIsUnknown(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerOn))
	r := h.reconciler()
	h.settle(t, r, "op")
	op := h.op(t, "op")
	if err := h.site.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: siteNS, Name: WorkerName(op.UID)}}); err != nil {
		t.Fatal(err)
	}
	h.settle(t, r, "op")
	if got := h.op(t, "op"); got.Status.Result != inventory.PowerUnknown || finishedReason(got) != ReasonRecordLost {
		t.Fatalf("status = %+v", got.Status)
	}
}

func TestSecondOperationOnADeviceIsRefused(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("first", "device-a", inventory.PowerOn), operation("second", "device-a", inventory.PowerForceOff))
	r := h.reconciler()
	h.settle(t, r, "first")
	h.settle(t, r, "second")
	if got := h.op(t, "second"); got.Status.Result != inventory.PowerFailed || finishedReason(got) != inventory.ReasonOperationInProgress {
		t.Fatalf("second = %+v", got.Status)
	}
	if n := h.count(t, &batchv1.JobList{}); n != 1 {
		t.Fatalf("%d Jobs", n)
	}
}

// Two dispatchers that both judge two operations on one Device acceptable
// cannot both start a worker: the site Lease admits one.
func TestDeviceLockAcrossDispatchers(t *testing.T) {
	first, second := operation("first", "device-a", inventory.PowerOn), operation("second", "device-a", inventory.PowerOn)
	h := newDispatchHarness(t, discoveredDevice("device-a"), first, second)
	a, b := h.reconciler(), h.reconciler()
	if held, err := a.lock(context.Background(), first); err != nil || !held {
		t.Fatalf("first lock = %v %v", held, err)
	}
	if held, err := b.lock(context.Background(), second); err != nil || held {
		t.Fatalf("second lock = %v %v", held, err)
	}
	// Once the holder has a result the lock is taken over.
	done := h.op(t, "first")
	done.Status.Result = inventory.PowerSucceeded
	if err := h.milo.Status().Update(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	if held, err := b.lock(context.Background(), second); err != nil || !held {
		t.Fatalf("takeover = %v %v", held, err)
	}
}

// Duplicate dispatch: several dispatchers reconciling the same operation at
// once write one record and one Job.
func TestDuplicateDispatchStartsOneWorker(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerForceRestart))
	var wg sync.WaitGroup
	for range 6 {
		r := h.reconciler()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: deviceNS, Name: "op"}})
			}
		}()
	}
	wg.Wait()
	h.settle(t, h.reconciler(), "op")
	if jobs, records, leases := h.count(t, &batchv1.JobList{}), h.count(t, &corev1.ConfigMapList{}), h.count(t, &coordinationv1.LeaseList{}); jobs != 1 || records != 1 || leases != 1 {
		t.Fatalf("%d Jobs, %d records, %d leases", jobs, records, leases)
	}
	if !meta.IsStatusConditionTrue(h.op(t, "op").Status.Conditions, inventory.ConditionPowerExecuting) {
		t.Fatal("not executing")
	}
}

func TestRetentionDeletesTheRecordAfterTheWorkerEnds(t *testing.T) {
	h := newDispatchHarness(t, discoveredDevice("device-a"), operation("op", "device-a", inventory.PowerOn))
	r := h.reconciler()
	h.settle(t, r, "op")
	op := h.op(t, "op")
	done := metav1.NewTime(t0)
	h.setProgressOf(t, op, func(p *Progress) {
		*p = Progress{Stage: StageFinished, ClaimedBy: "pod", Result: inventory.PowerSucceeded, Reason: ReasonAlreadyInState, FinishedAt: &done}
	})
	var job batchv1.Job
	_ = h.site.Get(context.Background(), types.NamespacedName{Namespace: siteNS, Name: WorkerName(op.UID)}, &job)
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := h.site.Status().Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	h.settle(t, r, "op")
	h.settle(t, r, "op")
	if n := h.count(t, &corev1.ConfigMapList{}); n != 1 {
		t.Fatalf("record removed before retention: %d", n)
	}
	h.now = t0.Add(r.Config.Retention.Duration + time.Minute)
	h.settle(t, r, "op")
	if n := h.count(t, &corev1.ConfigMapList{}); n != 0 {
		t.Fatalf("record kept after retention: %d", n)
	}
}

func TestShippedWorkerDeadlineCoversTheObservation(t *testing.T) {
	c := Config{}
	c.Default()
	if d := c.jobDeadline(); d < StaleAfter+c.ObserveTimeout.Duration {
		t.Fatalf("job deadline %s", d)
	}
}
