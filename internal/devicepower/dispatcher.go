package devicepower

import (
	"context"
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
)

// runningRequeue is how often a running operation is looked at even without
// an event.
const runningRequeue = 10 * time.Second

// Reconciler is the site dispatcher. It accepts or refuses each operation,
// starts at most one worker for an accepted one, and publishes the worker's
// result. It is the only writer of operation status and holds no BMC
// credential: it reads Devices and capacity in Milo and writes stage records,
// worker Jobs and their RBAC in the site.
type Reconciler struct {
	// Milo reads operations, Devices and capacity and writes only operation
	// status. MiloReader bypasses the cache for the checks that must be
	// fresh: the capacity re-check and a lock holder's state.
	Milo       client.Client
	MiloReader client.Reader
	// Site writes stage records, worker objects and per-Device Leases in
	// Namespace; SiteReader reads them without a cache.
	Site       client.Client
	SiteReader client.Reader
	Namespace  string
	Config     Config
	Now        func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var op inventory.DevicePowerOperation
	if err := r.Milo.Get(ctx, req.NamespacedName, &op); apierrors.IsNotFound(err) {
		return reconcile.Result{}, r.collectOrphans(ctx, req.NamespacedName)
	} else if err != nil {
		return reconcile.Result{}, err
	}
	if op.Status.Result != "" {
		return r.finished(ctx, &op)
	}
	rec, err := r.record(ctx, op.UID)
	if err != nil {
		return reconcile.Result{}, err
	}
	if rec == nil && !meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerAccepted) {
		return r.accept(ctx, &op)
	}
	if rec == nil {
		if meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerExecuting) {
			// The record existed and is gone: a worker may have run.
			return reconcile.Result{}, r.publish(ctx, &op, Progress{Result: inventory.PowerUnknown, Reason: ReasonRecordLost,
				Message: "the site stage record of a started operation is missing; a command may have been sent", FinishedAt: ptrTime(r.now())})
		}
		if rec, err = r.start(ctx, &op); err != nil || rec == nil {
			return reconcile.Result{Requeue: err == nil && rec == nil}, err
		}
	}
	return r.follow(ctx, &op, rec)
}

// accept runs the acceptance checks and either refuses the operation or
// takes the Device's site lock and marks it Accepted.
func (r *Reconciler) accept(ctx context.Context, op *inventory.DevicePowerOperation) (reconcile.Result, error) {
	a := Acceptance{Operation: op, DiscoveryMaxAge: r.Config.DiscoveryMaxAge.Duration, Now: r.now()}
	var device inventory.Device
	if err := r.Milo.Get(ctx, types.NamespacedName{Namespace: op.Namespace, Name: op.Spec.DeviceRef.Name}, &device); err == nil {
		a.Device = &device
	} else if !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	var capacities metal.MachineCapacityList
	if err := r.Milo.List(ctx, &capacities, client.InNamespace(op.Namespace)); err != nil {
		return reconcile.Result{}, err
	}
	a.Capacities = capacities.Items
	var others inventory.DevicePowerOperationList
	if err := r.Milo.List(ctx, &others, client.InNamespace(op.Namespace)); err != nil {
		return reconcile.Result{}, err
	}
	a.Others = others.Items
	if refusal := Check(a); refusal != nil {
		return reconcile.Result{}, r.refuse(ctx, op, *refusal)
	}
	held, err := r.lock(ctx, op)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !held {
		return reconcile.Result{}, r.refuse(ctx, op, Refusal{Reason: inventory.ReasonOperationInProgress, Message: "another operation on this Device is still running"})
	}
	return reconcile.Result{Requeue: true}, r.writeStatus(ctx, op, func(s *inventory.DevicePowerOperationStatus) {
		r.condition(op, s, inventory.ConditionPowerAccepted, metav1.ConditionTrue, "Accepted", "every acceptance check passed")
	})
}

// start re-checks capacity against Milo itself, not the cache, and retains
// the operation's exact inputs in a new stage record. It returns nil when the
// operation was refused instead.
func (r *Reconciler) start(ctx context.Context, op *inventory.DevicePowerOperation) (*corev1.ConfigMap, error) {
	var capacities metal.MachineCapacityList
	if err := r.MiloReader.List(ctx, &capacities, client.InNamespace(op.Namespace)); err != nil {
		return nil, err
	}
	checkedAt := r.now()
	if BacksCapacity(capacities.Items, op.Spec.DeviceRef.Name) {
		return nil, r.publish(ctx, op, Progress{Result: inventory.PowerFailed, Reason: inventory.ReasonCapacityBacked,
			Message: "customer capacity was defined on the Device before the worker started; nothing was sent", FinishedAt: ptrTime(checkedAt)})
	}
	var device inventory.Device
	if err := r.MiloReader.Get(ctx, types.NamespacedName{Namespace: op.Namespace, Name: op.Spec.DeviceRef.Name}, &device); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, r.publish(ctx, op, Progress{Result: inventory.PowerFailed, Reason: inventory.ReasonDeviceNotFound,
				Message: "the Device was deleted before the worker started; nothing was sent", FinishedAt: ptrTime(checkedAt)})
		}
		return nil, err
	}
	if _, ok := CredentialSecretName(op.Namespace, device.Name); !ok || device.Spec.BMC == nil || device.Status.Discovery == nil || device.Status.Discovery.TLS == nil {
		return nil, r.publish(ctx, op, Progress{Result: inventory.PowerFailed, Reason: inventory.ReasonDiscoveryNotCurrent,
			Message: "the Device lost its BMC endpoint or observed TLS key before the worker started; nothing was sent", FinishedAt: ptrTime(checkedAt)})
	}
	in := Inputs{OperationUID: string(op.UID), Project: r.Config.Project, Namespace: op.Namespace, Operation: op.Name, Device: device.Name,
		Endpoint: device.Spec.BMC.Endpoint, SystemUUID: op.Spec.ExpectedSystemUUID, PublicKeySHA256: device.Status.Discovery.TLS.PublicKeySHA256,
		Action: op.Spec.Action, CapacityCheckedAt: metav1.NewTime(checkedAt), ObserveTimeout: r.Config.ObserveTimeout, RequestTimeout: r.Config.RequestTimeout}
	rec := record(r.Namespace, in)
	if err := r.Site.Create(ctx, rec); apierrors.IsAlreadyExists(err) {
		// Another dispatcher, or this one before a restart, wrote it.
		return r.record(ctx, op.UID)
	} else if err != nil {
		return nil, err
	}
	return rec, nil
}

// follow starts the worker for a Pending record, and publishes the result
// once the worker recorded one or is gone.
func (r *Reconciler) follow(ctx context.Context, op *inventory.DevicePowerOperation, rec *corev1.ConfigMap) (reconcile.Result, error) {
	in, p, err := decodeRecord(rec)
	if err != nil {
		return reconcile.Result{}, r.publish(ctx, op, Progress{Result: inventory.PowerUnknown, Reason: ReasonRecordLost,
			Message: "the site stage record is unreadable; a command may have been sent", FinishedAt: ptrTime(r.now())})
	}
	if p.Stage == StageFinished || p.Stage == StageAbandoned {
		return reconcile.Result{}, r.publish(ctx, op, p)
	}
	var job batchv1.Job
	err = r.SiteReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: rec.Name}, &job)
	if apierrors.IsNotFound(err) && p.Stage == StagePending {
		if err := r.startWorker(ctx, rec, in); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: runningRequeue}, r.markExecuting(ctx, op)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	if err == nil && !jobFinished(&job) {
		return reconcile.Result{RequeueAfter: runningRequeue}, r.markExecuting(ctx, op)
	}
	// The worker is gone without a result. Fence the record so a lost worker
	// process can no longer send, then publish what the record proves.
	return reconcile.Result{}, r.fence(ctx, op, rec.Name)
}

func jobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// fence moves an unfinished record to Abandoned with the optimistic lock and
// publishes the result. Nothing sent is Failed; a possible send is Unknown.
func (r *Reconciler) fence(ctx context.Context, op *inventory.DevicePowerOperation, name string) error {
	var rec corev1.ConfigMap
	if err := r.SiteReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &rec); err != nil {
		return err
	}
	_, p, err := decodeRecord(&rec)
	if err != nil {
		return err
	}
	if p.Stage == StageFinished || p.Stage == StageAbandoned {
		return r.publish(ctx, op, p)
	}
	switch {
	case p.Stage.mayHaveSent():
		p.Result, p.Reason, p.Message = inventory.PowerUnknown, ReasonWorkerLost, "the worker stopped after a command may have been sent; it was not resent"
	case p.Stage == StageClaimed:
		p.Result, p.Reason, p.Message = inventory.PowerFailed, ReasonWorkerLost, "the worker stopped before it sent a command; nothing was sent"
	default:
		p.Result, p.Reason, p.Message = inventory.PowerFailed, ReasonWorkerNotStarted, "the worker ended before it claimed the operation; nothing was sent"
	}
	p.Stage, p.FinishedAt = StageAbandoned, ptrTime(r.now())
	setProgress(&rec, p)
	if err := r.Site.Update(ctx, &rec); err != nil {
		// A conflict means the worker wrote first; the next pass reads it.
		return err
	}
	return r.publish(ctx, op, p)
}

func (r *Reconciler) startWorker(ctx context.Context, rec *corev1.ConfigMap, in Inputs) error {
	secret, _ := CredentialSecretName(in.Namespace, in.Device)
	sa, role, binding, job := workerObjects(rec, in, r.Config.WorkerImage, secret, int64(r.Config.jobDeadline()/time.Second))
	for _, obj := range []client.Object{sa, role, binding, job} {
		if err := r.Site.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create worker %s: %w", obj.GetObjectKind().GroupVersionKind().Kind, err)
		}
	}
	return nil
}

func (r *Reconciler) markExecuting(ctx context.Context, op *inventory.DevicePowerOperation) error {
	if meta.IsStatusConditionTrue(op.Status.Conditions, inventory.ConditionPowerExecuting) {
		return nil
	}
	return r.writeStatus(ctx, op, func(s *inventory.DevicePowerOperationStatus) {
		r.condition(op, s, inventory.ConditionPowerExecuting, metav1.ConditionTrue, "WorkerStarted", "one worker was started for this operation")
	})
}

// refuse ends an operation at acceptance: Failed, nothing sent.
func (r *Reconciler) refuse(ctx context.Context, op *inventory.DevicePowerOperation, refusal Refusal) error {
	return r.writeStatus(ctx, op, func(s *inventory.DevicePowerOperationStatus) {
		r.condition(op, s, inventory.ConditionPowerAccepted, metav1.ConditionFalse, refusal.Reason, refusal.Message+"; nothing was sent")
		r.condition(op, s, inventory.ConditionPowerFinished, metav1.ConditionTrue, refusal.Reason, refusal.Message+"; nothing was sent")
		s.Result, s.FinishedAt = inventory.PowerFailed, ptrTime(r.now())
	})
}

// publish copies a result into status. The CRD admits the result once.
func (r *Reconciler) publish(ctx context.Context, op *inventory.DevicePowerOperation, p Progress) error {
	if err := r.writeStatus(ctx, op, func(s *inventory.DevicePowerOperationStatus) {
		if meta.FindStatusCondition(s.Conditions, inventory.ConditionPowerExecuting) != nil {
			r.condition(op, s, inventory.ConditionPowerExecuting, metav1.ConditionFalse, "Finished", "the operation finished")
		}
		r.condition(op, s, inventory.ConditionPowerFinished, metav1.ConditionTrue, p.Reason, p.Message)
		s.Result, s.RequestSentAt, s.FinishedAt = p.Result, p.RequestSentAt, p.FinishedAt
		if s.FinishedAt == nil {
			s.FinishedAt = ptrTime(r.now())
		}
		if p.Before != nil || p.After != nil {
			s.Power = &inventory.PowerObservations{Before: p.Before, After: p.After}
		}
	}); err != nil {
		return err
	}
	return r.unlock(ctx, op)
}

func (r *Reconciler) condition(op *inventory.DevicePowerOperation, s *inventory.DevicePowerOperationStatus, kind string, status metav1.ConditionStatus, reason, message string) {
	if len(message) > 1024 {
		message = message[:1024]
	}
	meta.SetStatusCondition(&s.Conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message,
		ObservedGeneration: op.Generation, LastTransitionTime: metav1.NewTime(r.now().Truncate(time.Second))})
}

// writeStatus updates the status subresource with the object's
// resourceVersion, so a stale read conflicts instead of overwriting.
func (r *Reconciler) writeStatus(ctx context.Context, op *inventory.DevicePowerOperation, mutate func(*inventory.DevicePowerOperationStatus)) error {
	updated := op.DeepCopyObject().(*inventory.DevicePowerOperation)
	mutate(&updated.Status)
	if err := r.Milo.Status().Update(ctx, updated); err != nil {
		return err
	}
	*op = *updated
	return nil
}

// record reads an operation's stage record without the cache, or nil.
func (r *Reconciler) record(ctx context.Context, uid types.UID) (*corev1.ConfigMap, error) {
	var rec corev1.ConfigMap
	err := r.SiteReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: WorkerName(uid)}, &rec)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// lock takes the Device's site Lease for the operation. Two dispatchers, or
// two operations on one Device, cannot both hold it: creation and takeover
// are optimistic writes. A holder that finished or no longer exists is
// replaced.
func (r *Reconciler) lock(ctx context.Context, op *inventory.DevicePowerOperation) (bool, error) {
	name := LockName(r.Config.Project, op.Namespace, op.Spec.DeviceRef.Name)
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.Namespace, Labels: objectLabels(string(op.UID), "lock"),
		Annotations: map[string]string{DeviceNamespace: op.Namespace, DeviceName: op.Spec.DeviceRef.Name, OperationNamespace: op.Namespace, OperationName: op.Name}},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(string(op.UID))}}
	err := r.Site.Create(ctx, lease)
	if err == nil {
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, err
	}
	var current coordinationv1.Lease
	if err := r.SiteReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, &current); err != nil {
		return false, err
	}
	holder := ptr.Deref(current.Spec.HolderIdentity, "")
	if holder == string(op.UID) {
		return true, nil
	}
	var ops inventory.DevicePowerOperationList
	if err := r.MiloReader.List(ctx, &ops, client.InNamespace(op.Namespace)); err != nil {
		return false, err
	}
	for _, other := range ops.Items {
		if string(other.UID) == holder && other.Status.Result == "" {
			return false, nil
		}
	}
	current.Spec.HolderIdentity = ptr.To(string(op.UID))
	current.Labels = lease.Labels
	current.Annotations = lease.Annotations
	if err := r.Site.Update(ctx, &current); err != nil {
		return false, err
	}
	return true, nil
}

// unlock releases the Device's Lease if this operation holds it.
func (r *Reconciler) unlock(ctx context.Context, op *inventory.DevicePowerOperation) error {
	var lease coordinationv1.Lease
	err := r.SiteReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: LockName(r.Config.Project, op.Namespace, op.Spec.DeviceRef.Name)}, &lease)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if ptr.Deref(lease.Spec.HolderIdentity, "") != string(op.UID) {
		return nil
	}
	err = r.Site.Delete(ctx, &lease, client.Preconditions{UID: &lease.UID, ResourceVersion: &lease.ResourceVersion})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// finished releases the lock and, after the retention period, deletes the
// operation's stage record, which owns its worker objects.
func (r *Reconciler) finished(ctx context.Context, op *inventory.DevicePowerOperation) (reconcile.Result, error) {
	if err := r.unlock(ctx, op); err != nil {
		return reconcile.Result{}, err
	}
	rec, err := r.record(ctx, op.UID)
	if err != nil || rec == nil {
		return reconcile.Result{}, err
	}
	var job batchv1.Job
	err = r.SiteReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: rec.Name}, &job)
	if err == nil && !jobFinished(&job) {
		// A worker still runs; keep its record until it ends.
		return reconcile.Result{RequeueAfter: runningRequeue}, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	finishedAt := rec.CreationTimestamp.Time
	if op.Status.FinishedAt != nil {
		finishedAt = op.Status.FinishedAt.Time
	}
	if wait := finishedAt.Add(r.Config.Retention.Duration).Sub(r.now()); wait > 0 {
		return reconcile.Result{RequeueAfter: wait}, nil
	}
	return reconcile.Result{}, r.deleteRecord(ctx, rec)
}

// collectOrphans deletes the records of a deleted operation once their
// worker has ended. Deleting an operation never cancels a command.
func (r *Reconciler) collectOrphans(ctx context.Context, key types.NamespacedName) error {
	var list corev1.ConfigMapList
	if err := r.SiteReader.List(ctx, &list, client.InNamespace(r.Namespace), client.MatchingLabels{PartOfLabel: PartOf, ComponentLabel: "record"}); err != nil {
		return err
	}
	var errs []error
	for i := range list.Items {
		rec := &list.Items[i]
		if rec.Annotations[OperationNamespace] != key.Namespace || rec.Annotations[OperationName] != key.Name {
			continue
		}
		var job batchv1.Job
		err := r.SiteReader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: rec.Name}, &job)
		if err == nil && !jobFinished(&job) {
			continue
		}
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, r.deleteRecord(ctx, rec))
	}
	return errors.Join(errs...)
}

func (r *Reconciler) deleteRecord(ctx context.Context, rec *corev1.ConfigMap) error {
	err := r.Site.Delete(ctx, rec, client.PropagationPolicy(metav1.DeletePropagationBackground), client.Preconditions{UID: &rec.UID})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
