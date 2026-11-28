package staging

import (
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/tenancy"
)

func assertPhase(t *testing.T, claim *fleetv1.Machine, phase string) {
	t.Helper()
	if claim.Status.Phase != phase {
		t.Fatalf("phase = %q, want %q", claim.Status.Phase, phase)
	}
}

func assertCondition(t *testing.T, claim *fleetv1.Machine, kind string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	got := apiMeta.FindStatusCondition(claim.Status.Conditions, kind)
	if got == nil || got.Status != status || (reason != "" && got.Reason != reason) {
		t.Fatalf("%s condition = %#v, want %s %q", kind, got, status, reason)
	}
}

func TestOneMachineAllocatesExclusivelyThenReusesAfterRelease(t *testing.T) {
	s := startStack(t)
	device := s.server(1)
	handler := s.handler()
	first, second := s.claim("first"), s.claim("second")

	// The first event adds the finalizer; the second allocates.
	drive(t, s.ctx, handler, consumerProject, first, 2)
	got := s.get(first)
	assertPhase(t, got, "Ready")
	assertCondition(t, got, "Bound", metav1.ConditionTrue, "MachineAllocationActive")
	assertCondition(t, got, "Ready", metav1.ConditionTrue, "")
	firstAllocation, err := s.allocator.Get(s.ctx, machineIdentity(consumerProject, first))
	if err != nil || firstAllocation == nil {
		t.Fatalf("first allocation = %v, %v", firstAllocation, err)
	}
	if firstAllocation.Spec.DeviceRef.Name != device.Name || firstAllocation.Status.Phase != fleetv1.AllocationActive {
		t.Fatalf("first allocation = %s %+v", firstAllocation.Status.Phase, firstAllocation.Spec.DeviceRef)
	}
	// Allocated hardware is the allocation's, and status.power belongs to
	// the power projector alone.
	if got.Status.AllocatedHardware == nil || !reflect.DeepEqual(got.Status.AllocatedHardware.Hardware, firstAllocation.Spec.Hardware) || got.Status.AllocatedHardware.Hardware.CPU.Cores != 24 {
		t.Fatalf("allocated hardware = %#v, allocation %#v", got.Status.AllocatedHardware, firstAllocation.Spec.Hardware)
	}
	if got.Status.Power != nil {
		t.Fatalf("handler wrote status.power = %#v", got.Status.Power)
	}
	if holder := s.custody(device.Name).Spec.Holder; holder.Type != ops.HolderAllocation || holder.AllocationRef == nil || holder.AllocationRef.Name != firstAllocation.Name {
		t.Fatalf("holder = %+v", holder)
	}

	drive(t, s.ctx, handler, consumerProject, second, 2)
	got = s.get(second)
	assertPhase(t, got, "Pending")
	assertCondition(t, got, "Bound", metav1.ConditionFalse, "InsufficientCapacity")
	if found, err := s.allocator.Get(s.ctx, machineIdentity(consumerProject, second)); err != nil || found != nil {
		t.Fatalf("second allocation = %v, %v", found, err)
	}

	if err := s.admin.Delete(s.ctx, s.get(first)); err != nil {
		t.Fatal(err)
	}
	// The release is repeated while the coordinator wipes the Device, and
	// the finalizer stays until the allocation is Released.
	s.until("the first Machine is released", func() bool {
		if out := handler.ReconcileEvent(s.ctx, projectRequest(consumerProject, first)); out.RetryError != nil {
			t.Logf("release: %v", out.RetryError)
		}
		err := s.admin.Get(s.ctx, client.ObjectKeyFromObject(first), &fleetv1.Machine{})
		return apierrors.IsNotFound(err)
	})
	wipe := &ops.DeviceWipe{}
	if err := s.admin.Get(s.ctx, client.ObjectKey{Namespace: providerNamespace, Name: custody.WipeName(firstAllocation.Name, 1)}, wipe); err != nil || wipe.Status.Phase != ops.PhaseSucceeded {
		t.Fatalf("wipe = %+v, %v", wipe.Status, err)
	}
	if err := s.admin.Get(s.ctx, client.ObjectKeyFromObject(firstAllocation), firstAllocation); err != nil || firstAllocation.Status.Phase != fleetv1.AllocationReleased {
		t.Fatalf("released allocation = %v, %v", firstAllocation.Status.Phase, err)
	}

	drive(t, s.ctx, handler, consumerProject, second, 1)
	got = s.get(second)
	assertPhase(t, got, "Ready")
	secondAllocation, err := s.allocator.Get(s.ctx, machineIdentity(consumerProject, second))
	if err != nil || secondAllocation == nil || secondAllocation.Spec.DeviceRef.Name != device.Name {
		t.Fatalf("second allocation = %v, %v", secondAllocation, err)
	}
}

// The claim never releases a Machine whose allocation custody no longer
// names: it stays Pending with its own reason for an operator to resolve.
func TestLostCustodyKeepsTheMachinePendingWithItsOwnReason(t *testing.T) {
	s := startStack(t)
	device := s.server(1)
	handler := s.handler()
	claim := s.claim("lost")
	drive(t, s.ctx, handler, consumerProject, claim, 2)
	assertPhase(t, s.get(claim), "Ready")
	active, err := s.allocator.Get(s.ctx, machineIdentity(consumerProject, claim))
	if err != nil || active == nil {
		t.Fatalf("allocation = %v, %v", active, err)
	}
	// Custody moves to Release behind the Machine's back, so the active
	// allocation can no longer claim the Device.
	if err := s.allocator.Custody.BeginRelease(s.ctx, device.Name, active.Name); err != nil {
		t.Fatal(err)
	}

	drive(t, s.ctx, handler, consumerProject, claim, 1)
	got := s.get(claim)
	assertPhase(t, got, "Pending")
	assertCondition(t, got, "Bound", metav1.ConditionFalse, "CustodyLost")
	assertCondition(t, got, "Ready", metav1.ConditionFalse, "CustodyLost")
}

// Every status the handler writes replaces Machine status wholesale, so each
// path must carry the power projector's field forward.
func TestHandlerStatusWritesPreserveStatusPower(t *testing.T) {
	s := startStack(t)
	s.server(1)
	handler := s.handler()
	setPower := func(claim *fleetv1.Machine) {
		t.Helper()
		current := s.get(claim)
		current.Status.Power = &fleetv1.PowerStatus{State: "On"}
		if err := s.admin.Status().Update(s.ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	unsupported := func(claim *fleetv1.Machine) {
		t.Helper()
		current := s.get(claim)
		current.Spec.PowerState = "On"
		if err := s.admin.Update(s.ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name      string
		claim     string
		prepare   func(*fleetv1.Machine)
		phase     string
		condition string
		reason    string
		status    metav1.ConditionStatus
	}{
		{"allocated", "power-allocated", nil, "Ready", "Ready", "PoweredOffHandoff", metav1.ConditionTrue},
		{"pending for capacity", "power-pending", nil, "Pending", "Bound", "InsufficientCapacity", metav1.ConditionFalse},
		{"rejected", "power-rejected", unsupported, "Pending", "Accepted", "UnsupportedSyntheticRequest", metav1.ConditionFalse},
	}
	for _, tt := range tests {
		claim := s.claim(tt.claim)
		if tt.prepare != nil {
			tt.prepare(claim)
		}
		setPower(claim)
		drive(t, s.ctx, handler, consumerProject, claim, 2)
		got := s.get(claim)
		assertPhase(t, got, tt.phase)
		assertCondition(t, got, tt.condition, tt.status, tt.reason)
		if got.Status.Power == nil || got.Status.Power.State != "On" {
			t.Fatalf("%s: status.power = %#v, want On preserved", tt.name, got.Status.Power)
		}
	}
}

func TestPublishedConsumerClaimAllocatesProviderMachine(t *testing.T) {
	s := startStack(t)
	device := s.server(1)
	consumer := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "project-consumer", UID: types.UID(uid(12))}
	provider := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: types.UID(uid(13))}

	// The consumer sees the publisher's copies of the provider's class and
	// profile, each naming the exact source lifetime.
	sourceClass, sourceProfile := s.class, s.profile
	revision, err := publicationRevision(sourceClass, provider)
	if err != nil {
		t.Fatal(err)
	}
	provenance := func(source client.Object) map[string]string {
		return map[string]string{publicationManagedByAnnotation: publicationManager, publicationProviderUIDAnnotation: string(provider.UID), publicationSourceUIDAnnotation: string(source.GetUID()), publicationSourceNameAnnotation: source.GetName()}
	}
	localClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: sourceClass.Name, UID: "local-class-uid", Generation: 1, Annotations: provenance(sourceClass)}, Spec: fleetv1.MachineClassSpec{Source: &fleetv1.SourceReference{PublisherRef: tenancy.Project(provider.RouteName), Name: sourceClass.Name}}, Status: fleetv1.MachineClassStatus{EvaluatedRevision: revision, ResolvedClass: &fleetv1.ResolvedClass{ProfilePolicy: *sourceClass.Spec.ProfilePolicy}, Conditions: []metav1.Condition{publicationCondition("SourceResolved", metav1.ConditionTrue, 1, "AuthorizedSource", "resolved")}}}
	localProfile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: sourceProfile.Name, UID: "local-profile-uid", Generation: 1, Annotations: provenance(sourceProfile)}, Spec: sourceProfile.Spec}
	localProfile.Status.Conditions = []metav1.Condition{publicationCondition("Published", metav1.ConditionTrue, 1, "SourceCopied", "copied")}
	claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cross-project", UID: types.UID(uid(901))}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: localClass.Name}, MachineProfileRef: fleetv1.LocalReference{Name: localProfile.Name}, LocationRef: fleetv1.LocalReference{Name: locationName}, PowerState: "Off"}}
	consumerKube := fake.NewClientBuilder().WithScheme(stackScheme(t)).WithStatusSubresource(&fleetv1.Machine{}).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid"}}, localClass, localProfile, claim).Build()
	handler := Handler{Public: consumerKube, Provider: s.admin, Allocator: s.allocator, Project: consumer, ProviderProject: provider, Location: locationName, Now: time.Now}

	drive(t, s.ctx, handler, consumer, claim, 2)
	var got fleetv1.Machine
	if err := consumerKube.Get(s.ctx, client.ObjectKeyFromObject(claim), &got); err != nil {
		t.Fatal(err)
	}
	assertPhase(t, &got, "Ready")
	assertCondition(t, &got, "Accepted", metav1.ConditionTrue, "SyntheticCrossProjectAdmission")
	// The allocation lives in the provider project and names the consumer's
	// exact Machine; the provider's Device is held for it.
	machine := machineIdentity(consumer, claim)
	allocated, err := s.allocator.Get(s.ctx, machine)
	if err != nil || allocated == nil {
		t.Fatalf("allocation = %v, %v", allocated, err)
	}
	if allocated.Spec.ConsumerRef != tenancy.Project(consumer.RouteName) || allocated.Labels[fleetv1.LabelConsumerUID] != string(consumer.UID) || allocated.Spec.DeviceRef.Name != device.Name || got.Status.AllocatedHardware == nil || !reflect.DeepEqual(got.Status.AllocatedHardware.Hardware, allocated.Spec.Hardware) {
		t.Fatalf("allocation = %+v, allocated hardware %#v", allocated.Spec, got.Status.AllocatedHardware)
	}
	if holder := s.custody(device.Name).Spec.Holder; holder.Type != ops.HolderAllocation || holder.AllocationRef.Name != allocated.Name {
		t.Fatalf("holder = %+v", holder)
	}
	capacity := s.capacity(device.Name)
	if c := apiMeta.FindStatusCondition(capacity.Status.Conditions, "Available"); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("provider capacity not marked allocated: %#v", capacity.Status.Conditions)
	}

	if err := consumerKube.Delete(s.ctx, &got); err != nil {
		t.Fatal(err)
	}
	s.until("the cross-project Machine is released", func() bool {
		if out := handler.ReconcileEvent(s.ctx, projectRequest(consumer, claim)); out.RetryError != nil {
			t.Logf("release: %v", out.RetryError)
		}
		return apierrors.IsNotFound(consumerKube.Get(s.ctx, client.ObjectKeyFromObject(claim), &fleetv1.Machine{}))
	})
	if holder := s.custody(device.Name).Spec.Holder; holder.Type != ops.HolderNone {
		t.Fatalf("holder after release = %+v", holder)
	}
	capacity = s.capacity(device.Name)
	if c := apiMeta.FindStatusCondition(capacity.Status.Conditions, "Available"); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("provider capacity did not become reusable: %#v", capacity.Status.Conditions)
	}
}

func (s *stack) capacity(device string) *fleetv1.MachineCapacity {
	s.t.Helper()
	capacity := &fleetv1.MachineCapacity{}
	if err := s.admin.Get(s.ctx, client.ObjectKey{Namespace: providerNamespace, Name: "capacity-" + device}, capacity); err != nil {
		s.t.Fatal(err)
	}
	return capacity
}

func TestStagingPreservesPinnedRequestAndRejectsConflictingProfile(t *testing.T) {
	s := startStack(t)
	s.server(1)
	owner := manager.NewLifecycleOwner(s.handler(), manager.RequestedHardwareProjector{Client: s.admin, Project: consumerProject}, nil)
	ready := s.claim("ready")
	// This Machine was accepted against an earlier same-name profile that
	// requested 8 cores; the current profile requests 24.
	conflicting := s.claim("conflicting")
	pinned := s.get(conflicting)
	pinned.Status.RequestedHardware = &fleetv1.RequestedHardwareStatus{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 8}, Memory: fleetv1.MemoryRequirement{Capacity: "32Gi"}, Source: fleetv1.RequestedHardwareSource{MachineGeneration: pinned.Generation, ProfileRef: fleetv1.LocalReference{Name: profileName}, ProfileGeneration: 1, ProfileDigest: "sha256:" + strings.Repeat("0", 64)}}
	if err := s.admin.Status().Update(s.ctx, pinned); err != nil {
		t.Fatal(err)
	}

	drive(t, s.ctx, owner, consumerProject, ready, 3)
	got := s.get(ready)
	resolved := apiMeta.FindStatusCondition(got.Status.Conditions, controller.ConditionRequestResolved)
	if got.Status.Phase != "Ready" || got.Status.RequestedHardware == nil || got.Status.RequestedHardware.CPU.Cores != 24 || got.Status.AllocatedHardware == nil || resolved == nil || resolved.Status != metav1.ConditionTrue {
		t.Fatalf("ready status = %#v", got.Status)
	}

	drive(t, s.ctx, owner, consumerProject, conflicting, 3)
	got = s.get(conflicting)
	accepted := apiMeta.FindStatusCondition(got.Status.Conditions, "Accepted")
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != "RequestedHardwareConflict" || got.Status.AllocatedHardware != nil {
		t.Fatalf("conflicting status = %#v", got.Status)
	}
	if got.Status.RequestedHardware == nil || got.Status.RequestedHardware.CPU.Cores != 8 || got.Status.RequestedHardware.Memory.Capacity != "32Gi" {
		t.Fatalf("pinned snapshot rewritten: %#v", got.Status.RequestedHardware)
	}
	if found, err := s.allocator.Get(s.ctx, machineIdentity(consumerProject, conflicting)); err != nil || found != nil {
		t.Fatalf("conflicting Machine was allocated: %v, %v", found, err)
	}
}

// TestAClassFromAnotherPublisherIsRejected never allocates through a
// published class whose publisherRef names anything but the provider
// project that published it.
func TestAClassFromAnotherPublisherIsRejected(t *testing.T) {
	s := startStack(t)
	s.server(1)
	consumer := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "project-consumer", UID: types.UID(uid(12))}
	provider := controller.ProjectIdentity{AuthorityID: "staging", RouteName: "datum-cloud", UID: types.UID(uid(13))}
	sourceClass, sourceProfile := s.class, s.profile
	revision, err := publicationRevision(sourceClass, provider)
	if err != nil {
		t.Fatal(err)
	}
	provenance := func(source client.Object) map[string]string {
		return map[string]string{publicationManagedByAnnotation: publicationManager, publicationProviderUIDAnnotation: string(provider.UID), publicationSourceUIDAnnotation: string(source.GetUID()), publicationSourceNameAnnotation: source.GetName()}
	}
	for i, publisher := range []fleetv1.TypedReference{tenancy.Project("another-provider"), tenancy.Namespace(provider.RouteName)} {
		localClass := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: sourceClass.Name, UID: "local-class-uid", Generation: 1, Annotations: provenance(sourceClass)}, Spec: fleetv1.MachineClassSpec{Source: &fleetv1.SourceReference{PublisherRef: publisher, Name: sourceClass.Name}}, Status: fleetv1.MachineClassStatus{EvaluatedRevision: revision, ResolvedClass: &fleetv1.ResolvedClass{ProfilePolicy: *sourceClass.Spec.ProfilePolicy}, Conditions: []metav1.Condition{publicationCondition("SourceResolved", metav1.ConditionTrue, 1, "AuthorizedSource", "resolved")}}}
		localProfile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: sourceProfile.Name, UID: "local-profile-uid", Generation: 1, Annotations: provenance(sourceProfile)}, Spec: sourceProfile.Spec}
		localProfile.Status.Conditions = []metav1.Condition{publicationCondition("Published", metav1.ConditionTrue, 1, "SourceCopied", "copied")}
		claim := &fleetv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cross-project", UID: types.UID(uid(910 + i))}, Spec: fleetv1.MachineSpec{MachineClassRef: fleetv1.LocalReference{Name: localClass.Name}, MachineProfileRef: fleetv1.LocalReference{Name: localProfile.Name}, LocationRef: fleetv1.LocalReference{Name: locationName}, PowerState: "Off"}}
		consumerKube := fake.NewClientBuilder().WithScheme(stackScheme(t)).WithStatusSubresource(&fleetv1.Machine{}).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid"}}, localClass, localProfile, claim).Build()
		handler := Handler{Public: consumerKube, Provider: s.admin, Allocator: s.allocator, Project: consumer, ProviderProject: provider, Location: locationName, Now: time.Now}
		handler.ReconcileEvent(s.ctx, projectRequest(consumer, claim))
		var got fleetv1.Machine
		if err := consumerKube.Get(s.ctx, client.ObjectKeyFromObject(claim), &got); err != nil {
			t.Fatal(err)
		}
		assertCondition(t, &got, "Accepted", metav1.ConditionFalse, "UnresolvedPublication")
		if allocated, err := s.allocator.Get(s.ctx, machineIdentity(consumer, claim)); err != nil || allocated != nil {
			t.Fatalf("publisher %+v: allocation = %v, %v", publisher, allocated, err)
		}
	}
}
