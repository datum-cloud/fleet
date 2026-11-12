package standalone

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/simulator"
)

type storeExecutor struct{ store *simulator.Store }

type deleteCaptureClient struct {
	client.Client
	options *client.DeleteOptions
}

type conflictOnceClient struct {
	client.Client
	remaining int
}

type recreateOnDeleteClient struct{ client.Client }

func (c *recreateOnDeleteClient) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	if err := c.Client.Delete(ctx, object, opts...); err != nil {
		return err
	}
	replacement := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: object.GetNamespace(), Name: object.GetName(), UID: "replacement-uid"}}
	return c.Client.Create(ctx, replacement)
}

func (c *conflictOnceClient) Update(ctx context.Context, object client.Object, opts ...client.UpdateOption) error {
	if c.remaining > 0 {
		if _, ok := object.(*fleetv1.Machine); ok {
			c.remaining--
			return apierrors.NewConflict(schema.GroupResource{Group: fleetv1.GroupVersion.Group, Resource: "machines"}, object.GetName(), errors.New("injected conflict"))
		}
	}
	return c.Client.Update(ctx, object, opts...)
}

func (c *deleteCaptureClient) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	c.options = (&client.DeleteOptions{}).ApplyOptions(opts)
	return c.Client.Delete(ctx, object, opts...)
}

func (e storeExecutor) Execute(_ context.Context, request simulator.Request) (simulator.OperationResult, error) {
	result, _, err := e.store.Submit(request)
	return result.ResultView(), err
}
func (e storeExecutor) Lookup(_ context.Context, id string) (simulator.OperationResult, bool, error) {
	result, found := e.store.Get(id)
	return result.ResultView(), found, nil
}

func TestPhasesKeepExclusiveCustodyAndRecoverInstallation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions()
	objects := fixtureObjects(options)
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&fleetv1.MachineCapacity{}, &fleetv1.Machine{}, &fleetv1.Installation{}).Build()
	kube := &deleteCaptureClient{Client: base}
	statePath := t.TempDir() + "/state.json"
	store, err := simulator.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runner := Runner{Client: kube, Executor: storeExecutor{store}, Options: options, Now: func() time.Time { return now }}
	if err := runner.Run(context.Background(), PhaseAllocate); err != nil {
		t.Fatal(err)
	}
	var first, second fleetv1.Machine
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: options.FirstNamespace, Name: options.ClaimName}, &first); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: options.SecondNamespace, Name: options.ClaimName}, &second); err != nil {
		t.Fatal(err)
	}
	if first.Status.Phase != "Ready" || second.Status.Phase != "Pending" {
		t.Fatalf("unexpected contention result: first=%s second=%s", first.Status.Phase, second.Status.Phase)
	}
	if first.Status.Placement == nil || first.Status.Placement.LocationRef.Name != options.LocationName || first.Status.AllocatedHardware == nil || first.Status.AllocatedHardware.ObservedAt == nil || !first.Status.AllocatedHardware.ObservedAt.Time.Equal(now) {
		t.Fatalf("claim did not project exact retained location/evidence: %#v", first.Status)
	}
	var evidenceSecret corev1.Secret
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: options.PrivateNamespace, Name: evidenceName("machine-uid")}, &evidenceSecret); err != nil {
		t.Fatal(err)
	}
	var evidence syntheticEvidence
	if err := json.Unmarshal(evidenceSecret.Data["record.json"], &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Location != options.LocationName || evidence.MachineUID != "machine-uid" || !evidence.ObservedAt.Equal(now) {
		t.Fatalf("unexpected retained evidence: %#v", evidence)
	}
	assertRetainedOwner(t, kube, options, "claim-a-uid", 1)
	if err := runner.Run(context.Background(), PhaseInstall); err != nil {
		t.Fatal(err)
	}
	var installed fleetv1.Installation
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: options.FirstNamespace, Name: options.InstallationName}, &installed); err != nil {
		t.Fatal(err)
	}
	if installed.Status.Phase != "Ready" || installed.Status.CompletedAt == nil {
		t.Fatalf("installation was not projected: %#v", installed.Status)
	}
	beforeRestart := store.Snapshot()
	if len(beforeRestart.Operations) != 1 {
		t.Fatalf("got %d install operations, want 1", len(beforeRestart.Operations))
	}
	var installID string
	var installBinding string
	for id, operation := range beforeRestart.Operations {
		installID, installBinding = id, operation.BindingID
		if operation.ID != id || operation.Kind != "install" || operation.Epoch != 1 || operation.Phase != "Succeeded" || operation.Result != "installed" || operation.BindingID == "" {
			t.Fatalf("unexpected retained install: %#v", operation)
		}
	}
	// Reopen the durable file, rather than reusing in-memory simulator state.
	reopened, err := simulator.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := Runner{Client: kube, Executor: storeExecutor{reopened}, Options: options, Now: func() time.Time { return now }}
	if err := restarted.Run(context.Background(), PhaseInstall); err != nil {
		t.Fatal(err)
	}
	if snapshot := reopened.Snapshot(); len(snapshot.Operations) != 1 || snapshot.Operations[installID] == nil || snapshot.Operations[installID].BindingID != installBinding {
		t.Fatalf("restart created %d effects, want 1", len(snapshot.Operations))
	}
	if err := restarted.Run(context.Background(), PhaseRelease); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: options.FirstNamespace, Name: options.ClaimName}, &first); !apierrors.IsNotFound(err) {
		t.Fatalf("released claim still exists or lookup failed: %v", err)
	}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: options.SecondNamespace, Name: options.ClaimName}, &second); err != nil {
		t.Fatal(err)
	}
	if second.Status.Phase != "Ready" || second.Status.AllocatedHardware == nil || second.Status.AllocatedHardware.Hardware.CPU.Cores != 24 {
		t.Fatalf("waiting claim did not acquire released hardware: %#v", second.Status)
	}
	if kube.options == nil || kube.options.Preconditions == nil || kube.options.Preconditions.UID == nil || *kube.options.Preconditions.UID != "claim-a-uid" || kube.options.Preconditions.ResourceVersion == nil {
		t.Fatalf("claim delete lacked exact UID/resourceVersion preconditions: %#v", kube.options)
	}
	if snapshot := reopened.Snapshot(); len(snapshot.Operations) != 2 {
		t.Fatalf("release produced %d total effects, want install plus cleanup", len(snapshot.Operations))
	} else {
		for id, operation := range snapshot.Operations {
			if id == installID {
				continue
			}
			if operation.ID != id || operation.Kind != "cleanup" || operation.BindingID != installBinding || operation.Epoch != 1 || operation.Phase != "Succeeded" || operation.Result != "clean" {
				t.Fatalf("unexpected retained cleanup: %#v", operation)
			}
		}
	}
	assertRetainedOwner(t, kube, options, "claim-b-uid", 2)
}

func assertRetainedOwner(t *testing.T, kube client.Client, options Options, claimUID types.UID, epoch int64) {
	t.Helper()
	var secrets corev1.SecretList
	if err := kube.List(context.Background(), &secrets, client.InNamespace(options.PrivateNamespace), client.MatchingLabels{"fleet.datumapis.com/private-record": "asset"}); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("got %d retained assets, want 1", len(secrets.Items))
	}
	var record controller.AssetRecord
	if err := json.Unmarshal(secrets.Items[0].Data["record.json"], &record); err != nil {
		t.Fatal(err)
	}
	if record.ClaimUID != claimUID || record.Epoch != int64(epoch) || record.BindingID == "" {
		t.Fatalf("unexpected retained owner: %#v", record)
	}
}

func TestFinalizerRemovalRejectsRecreatedClaimAndRetriesConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: "project", Name: "allocation"}
	recreated := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: "new-uid", Finalizers: []string{controller.MachineFinalizer}}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(recreated).Build()
	runner := Runner{Client: base}
	if err := runner.removeExactFinalizer(context.Background(), key, "old-uid"); !errors.Is(err, controller.ErrRecordInvalid) {
		t.Fatalf("recreated claim error = %v", err)
	}
	var current fleetv1.Machine
	if err := base.Get(context.Background(), key, &current); err != nil {
		t.Fatal(err)
	}
	if !contains(current.Finalizers, controller.MachineFinalizer) {
		t.Fatal("recreated claim finalizer was removed")
	}

	current.UID = "exact-uid"
	if err := base.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	conflicting := &conflictOnceClient{Client: base, remaining: 1}
	runner.Client = conflicting
	if err := runner.removeExactFinalizer(context.Background(), key, "exact-uid"); err != nil {
		t.Fatal(err)
	}
	if conflicting.remaining != 0 {
		t.Fatal("conflict was not exercised")
	}
	if err := base.Get(context.Background(), key, &current); err != nil {
		t.Fatal(err)
	}
	if contains(current.Finalizers, controller.MachineFinalizer) {
		t.Fatal("exact claim finalizer remained after retry")
	}
}

func TestExactDeletionRejectsNameRecreation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "project", Name: "allocation", UID: "original-uid", ResourceVersion: "1"}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim).Build()
	runner := Runner{Client: &recreateOnDeleteClient{Client: base}}
	if err := runner.requestExactDeletion(context.Background(), claim, "original-uid", claim.ResourceVersion); !errors.Is(err, controller.ErrRecordInvalid) {
		t.Fatalf("recreation error = %v", err)
	}
	var current fleetv1.Machine
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(claim), &current); err != nil {
		t.Fatal(err)
	}
	if current.UID != "replacement-uid" {
		t.Fatalf("replacement UID = %q", current.UID)
	}
}

func fixtureObjects(o Options) []client.Object {
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: o.ClassName, UID: "class-uid"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: o.ProviderNamespace, Selector: metav1.LabelSelector{MatchLabels: map[string]string{"fleet.datumapis.com/pool": "central"}}}}}}
	minCPU := int64(24)
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: o.ProfileName, UID: "profile-uid"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: fleetv1.Constraint{Min: &minCPU}}, Memory: fleetv1.MemoryRequirement{Capacity: fleetv1.QuantityConstraint{Min: "128Gi"}}}}
	machine := &fleetv1.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Namespace: o.ProviderNamespace, Name: o.MachineName, UID: "machine-uid", Generation: 1, Labels: map[string]string{"fleet.datumapis.com/pool": "central"}}, Spec: fleetv1.MachineCapacitySpec{DeviceRef: fleetv1.LocalReference{Name: "asset-central-01"}, AllocationPolicy: "Enabled", CleaningPolicyRef: fleetv1.LocalReference{Name: "synthetic-clean"}, ReclaimPolicy: "Retain"}}
	claim := func(ns, uid string) *fleetv1.Machine {
		return &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: o.ClaimName, UID: types.UID(uid), Generation: 1}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: o.ClassName}, MachineProfileRef: fleetv1.LocalReference{Name: o.ProfileName}, LocationRef: fleetv1.LocalReference{Name: o.LocationName}, PowerState: "Off"}}
	}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: o.FirstNamespace, Name: o.InstallationName, UID: "installation-uid", Generation: 1}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: o.ClaimName}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: o.ImageName}}}
	return []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: o.PrivateNamespace, UID: "private-uid"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: o.ProviderNamespace, UID: "provider-uid"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: o.FirstNamespace, UID: "project-a-uid"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: o.SecondNamespace, UID: "project-b-uid"}}, class, profile, machine, claim(o.FirstNamespace, "claim-a-uid"), claim(o.SecondNamespace, "claim-b-uid"), installation}
}
