package manager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

type requestedFixture struct {
	client       client.Client
	projector    RequestedHardwareProjector
	request      ProjectRequest
	statusWrites *int
}

func newRequestedFixture(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) requestedFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	machine := &fleetv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "example", UID: "machine-uid", Generation: 1},
		Spec:       fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: "standard"}, MachineProfileRef: fleetv1.LocalReference{Name: "general-8c-32g"}, LocationRef: fleetv1.LocalReference{Name: "us-central-1"}, PowerState: "Off"},
	}
	writes := 0
	statusUpdate := funcs.SubResourceUpdate
	funcs.SubResourceUpdate = func(ctx context.Context, c client.Client, subResource string, object client.Object, options ...client.SubResourceUpdateOption) error {
		writes++
		if statusUpdate != nil {
			return statusUpdate(ctx, c, subResource, object, options...)
		}
		return c.SubResource(subResource).Update(ctx, object, options...)
	}
	built := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&fleetv1.Machine{}).WithObjects(append([]client.Object{machine}, objects...)...).WithInterceptorFuncs(funcs).Build()
	project := testProject("milo", "project-uid")
	return requestedFixture{
		client:       built,
		projector:    RequestedHardwareProjector{Client: built, Project: project},
		request:      ProjectRequest{AuthorityID: "milo", ProjectUID: "project-uid", Namespace: "default", Name: "example", UID: "machine-uid"},
		statusWrites: &writes,
	}
}

func requestedProfile(generation, cores int64, memory string) *fleetv1.MachineProfile {
	return &fleetv1.MachineProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "general-8c-32g", UID: "profile-uid", Generation: generation},
		Spec:       fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: cores}, Memory: fleetv1.MemoryRequirement{Capacity: memory}},
	}
}

func samePinned(accepted fleetv1.RequestedHardwareStatus, current *fleetv1.RequestedHardwareStatus) bool {
	return current != nil && reflect.DeepEqual(accepted, *current)
}

func (f requestedFixture) reconcile(t *testing.T) *fleetv1.Machine {
	t.Helper()
	if outcome := f.projector.ReconcileEvent(context.Background(), f.request); outcome.RetryError != nil || outcome.Deadline.Mode != DeadlineUnchanged {
		t.Fatalf("outcome = %#v", outcome)
	}
	var machine fleetv1.Machine
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "example"}, &machine); err != nil {
		t.Fatal(err)
	}
	return &machine
}

func assertResolved(t *testing.T, machine *fleetv1.Machine, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := apiMeta.FindStatusCondition(machine.Status.Conditions, controller.ConditionRequestResolved)
	if condition == nil || condition.Status != status || condition.Reason != reason || condition.ObservedGeneration != machine.Generation {
		t.Fatalf("RequestResolved = %#v, want %s/%s at generation %d", condition, status, reason, machine.Generation)
	}
}

func TestRequestedHardwareResolvesExactProfileWithProvenance(t *testing.T) {
	f := newRequestedFixture(t, interceptor.Funcs{}, requestedProfile(4, 8, "32Gi"))
	machine := f.reconcile(t)
	snapshot := machine.Status.RequestedHardware
	if snapshot == nil || snapshot.CPU.Cores != 8 || snapshot.CPU.Architecture != "amd64" || snapshot.Memory.Capacity != "32Gi" {
		t.Fatalf("requestedHardware = %#v", snapshot)
	}
	wantDigest, _ := controller.ProfileSpecDigest(requestedProfile(4, 8, "32Gi").Spec)
	if snapshot.Source.MachineGeneration != 1 || snapshot.Source.ProfileGeneration != 4 || snapshot.Source.ProfileRef.Name != "general-8c-32g" || snapshot.Source.ProfileDigest != wantDigest {
		t.Fatalf("provenance = %#v", snapshot.Source)
	}
	if machine.Status.AllocatedHardware != nil || machine.Status.Phase != "" {
		t.Fatalf("resolution wrote allocation state: %#v", machine.Status)
	}
	assertResolved(t, machine, metav1.ConditionTrue, controller.ReasonRequestResolved)
	f.reconcile(t)
	if *f.statusWrites != 1 {
		t.Fatalf("status writes = %d, want one idempotent write", *f.statusWrites)
	}
}

func TestRequestedHardwareMissingProfileIsExplicitAndUnresolved(t *testing.T) {
	f := newRequestedFixture(t, interceptor.Funcs{})
	machine := f.reconcile(t)
	if machine.Status.RequestedHardware != nil {
		t.Fatalf("missing profile produced snapshot %#v", machine.Status.RequestedHardware)
	}
	assertResolved(t, machine, metav1.ConditionFalse, controller.ReasonProfileNotFound)

	// The profile's Add event routes the Machine again and resolves it.
	if err := f.client.Create(context.Background(), requestedProfile(1, 8, "32Gi")); err != nil {
		t.Fatal(err)
	}
	machine = f.reconcile(t)
	if machine.Status.RequestedHardware == nil || machine.Status.RequestedHardware.CPU.Cores != 8 {
		t.Fatalf("late profile did not resolve: %#v", machine.Status.RequestedHardware)
	}
	assertResolved(t, machine, metav1.ConditionTrue, controller.ReasonRequestResolved)
}

func TestRequestedHardwareRejectsInexactOrDeletingProfile(t *testing.T) {
	deleting := requestedProfile(1, 8, "32Gi")
	deleting.Finalizers = []string{"example.com/hold"}
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	for name, test := range map[string]struct {
		profile *fleetv1.MachineProfile
		reason  string
	}{
		"zero cores":        {requestedProfile(1, 0, "32Gi"), controller.ReasonProfileInvalid},
		"missing memory":    {requestedProfile(1, 8, ""), controller.ReasonProfileInvalid},
		"fractional memory": {requestedProfile(1, 8, "1.5Gi"), controller.ReasonProfileInvalid},
		"deleting":          {deleting, controller.ReasonProfileDeleting},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRequestedFixture(t, interceptor.Funcs{}, test.profile)
			machine := f.reconcile(t)
			if machine.Status.RequestedHardware != nil {
				t.Fatalf("snapshot invented from %s profile: %#v", name, machine.Status.RequestedHardware)
			}
			assertResolved(t, machine, metav1.ConditionFalse, test.reason)
		})
	}
}

func TestRequestedHardwareStaysPinnedAfterProfileChanges(t *testing.T) {
	ctx := context.Background()
	f := newRequestedFixture(t, interceptor.Funcs{}, requestedProfile(1, 8, "32Gi"))
	accepted := *f.reconcile(t).Status.RequestedHardware

	// An in-place edit (blocked by CRD immutability, but stored objects can
	// still differ) must not change the snapshot.
	var profile fleetv1.MachineProfile
	if err := f.client.Get(ctx, client.ObjectKey{Name: "general-8c-32g"}, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Spec.CPU.Cores, profile.Spec.Memory.Capacity, profile.Generation = 16, "64Gi", 2
	if err := f.client.Update(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	machine := f.reconcile(t)
	if !samePinned(accepted, machine.Status.RequestedHardware) {
		t.Fatalf("edited profile rewrote snapshot: %#v", machine.Status.RequestedHardware)
	}

	// Deletion and same-name recreation with different contents are ignored too.
	if err := f.client.Delete(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	machine = f.reconcile(t)
	if !samePinned(accepted, machine.Status.RequestedHardware) {
		t.Fatalf("deleted profile changed snapshot: %#v", machine.Status.RequestedHardware)
	}
	recreated := requestedProfile(1, 32, "128Gi")
	recreated.UID = "recreated-profile-uid"
	if err := f.client.Create(ctx, recreated); err != nil {
		t.Fatal(err)
	}
	machine = f.reconcile(t)
	if !samePinned(accepted, machine.Status.RequestedHardware) {
		t.Fatalf("recreated profile changed snapshot: %#v", machine.Status.RequestedHardware)
	}
	assertResolved(t, machine, metav1.ConditionTrue, controller.ReasonRequestResolved)

	// A later Machine generation (powerState intent) keeps the accepted
	// provenance while the condition tracks the current generation.
	machine.Spec.PowerState, machine.Generation = "On", 2
	if err := f.client.Update(ctx, machine); err != nil {
		t.Fatal(err)
	}
	machine = f.reconcile(t)
	if !samePinned(accepted, machine.Status.RequestedHardware) || machine.Status.RequestedHardware.Source.MachineGeneration != 1 {
		t.Fatalf("generation change rewrote snapshot: %#v", machine.Status.RequestedHardware)
	}
	assertResolved(t, machine, metav1.ConditionTrue, controller.ReasonRequestResolved)
}

func TestRequestedHardwareRetriesUnknownProfileReads(t *testing.T) {
	readErr := errors.New("profile read unavailable")
	f := newRequestedFixture(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
		if _, ok := object.(*fleetv1.MachineProfile); ok {
			return readErr
		}
		return c.Get(ctx, key, object, options...)
	}}, requestedProfile(1, 8, "32Gi"))
	if outcome := f.projector.ReconcileEvent(context.Background(), f.request); !errors.Is(outcome.RetryError, readErr) {
		t.Fatalf("outcome = %#v", outcome)
	}
	if *f.statusWrites != 0 {
		t.Fatal("unknown profile read was converted into a status result")
	}
}

func TestRequestedHardwareIgnoresOtherLifetimesAndFailsClosedWithoutWiring(t *testing.T) {
	f := newRequestedFixture(t, interceptor.Funcs{}, requestedProfile(1, 8, "32Gi"))
	stale := f.request
	stale.UID = "previous-machine-uid"
	if outcome := f.projector.ReconcileEvent(context.Background(), stale); outcome.RetryError != nil || *f.statusWrites != 0 {
		t.Fatalf("stale lifetime outcome = %#v writes=%d", outcome, *f.statusWrites)
	}
	other := f.request
	other.ProjectUID = "other-project"
	if outcome := f.projector.ReconcileEvent(context.Background(), other); !errors.Is(outcome.RetryError, ErrEventWiringUnavailable) {
		t.Fatalf("cross-project outcome = %#v", outcome)
	}
	if outcome := (RequestedHardwareProjector{}).ReconcileEvent(context.Background(), f.request); !errors.Is(outcome.RetryError, ErrEventWiringUnavailable) {
		t.Fatalf("unwired outcome = %#v", outcome)
	}
}

// The default production manager keeps every lifecycle handler blocked. The
// projector must still resolve requests through the shared lifecycle owner.
func TestRequestedHardwareResolvesWhileLifecycleIsBlocked(t *testing.T) {
	f := newRequestedFixture(t, interceptor.Funcs{}, requestedProfile(1, 8, "32Gi"))
	blocked := HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} })
	owner := NewLifecycleOwner(blocked, f.projector, nil)
	if outcome := owner.ReconcileEvent(context.Background(), f.request); outcome.RetryError != nil {
		t.Fatal(outcome.RetryError)
	}
	var machine fleetv1.Machine
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "example"}, &machine); err != nil {
		t.Fatal(err)
	}
	if machine.Status.RequestedHardware == nil || machine.Status.AllocatedHardware != nil || len(machine.Finalizers) != 0 {
		t.Fatalf("inert-mode resolution = %#v finalizers=%v", machine.Status, machine.Finalizers)
	}
}

func TestNewMachineProjectorDefaultEnabledPins(t *testing.T) {
	f := newRequestedFixture(t, interceptor.Funcs{}, requestedProfile(1, 8, "32Gi"))
	projector := NewMachineProjector(true, f.client, f.projector.Project)
	if _, ok := projector.(RequestedHardwareProjector); !ok {
		t.Fatalf("enabled projector = %T, want RequestedHardwareProjector", projector)
	}
	if outcome := projector.ReconcileEvent(context.Background(), f.request); outcome.RetryError != nil {
		t.Fatal(outcome.RetryError)
	}
	if *f.statusWrites != 1 {
		t.Fatalf("status writes = %d, want the enabled projector to pin once", *f.statusWrites)
	}
}

func TestDisabledMachineProjectorNeverReadsOrWritesMachines(t *testing.T) {
	reads := 0
	f := newRequestedFixture(t, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		reads++
		return c.Get(ctx, key, obj, opts...)
	}}, requestedProfile(1, 8, "32Gi"))
	projector := NewMachineProjector(false, f.client, f.projector.Project)
	// The disabled projector runs inside the lifecycle owner exactly where the
	// enabled one would, after the (blocked) coordinator.
	owner := NewLifecycleOwner(HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} }), projector, nil)
	if outcome := owner.ReconcileEvent(context.Background(), f.request); outcome.RetryError != nil {
		t.Fatal(outcome.RetryError)
	}
	if reads != 0 || *f.statusWrites != 0 {
		t.Fatalf("disabled projector reads=%d writes=%d, want none", reads, *f.statusWrites)
	}
	machine := &fleetv1.Machine{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "example"}, machine); err != nil {
		t.Fatal(err)
	}
	if machine.Status.RequestedHardware != nil || apiMeta.FindStatusCondition(machine.Status.Conditions, controller.ConditionRequestResolved) != nil {
		t.Fatalf("disabled projector wrote status: %#v", machine.Status)
	}
}
