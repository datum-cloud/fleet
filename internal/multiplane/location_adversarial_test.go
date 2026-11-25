package multiplane

import (
	"context"
	"errors"
	"testing"

	"go.datum.net/fleet/internal/allocation"
	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestAdversarialRejectNonlocalMachineBeforeAllocation refuses a Machine for
// another location before any allocation, finalizer or custody claim: the
// provider project and custody are never touched.
func TestAdversarialRejectNonlocalMachineBeforeAllocation(t *testing.T) {
	ctx := context.Background()
	scheme := Scheme()
	var providerWrites int
	countWrites := interceptor.Funcs{
		Create: func(ctx context.Context, api client.WithWatch, object client.Object, options ...client.CreateOption) error {
			providerWrites++
			return api.Create(ctx, object, options...)
		},
		Update: func(ctx context.Context, api client.WithWatch, object client.Object, options ...client.UpdateOption) error {
			providerWrites++
			return api.Update(ctx, object, options...)
		},
	}
	profile := &fleet.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "local-profile", UID: "source-profile"}}
	profile.Spec.CPU.Architecture = "amd64"
	profile.Spec.CPU.Cores = 2
	profile.Spec.Memory.Capacity = "2Gi"
	class := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "local-class", UID: "source-class"}, Spec: fleet.MachineClassSpec{Capacity: &fleet.CapacityPolicy{MachineCapacities: fleet.MachineSelector{Namespace: DeviceNamespace}}}, Status: fleet.MachineClassStatus{EvaluatedRevision: "v1"}}
	machine := &fleet.Machine{ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: Namespace, UID: "machine"}, Spec: fleet.MachineSpec{MachineClassRef: fleet.LocalReference{Name: "local-class"}, MachineProfileRef: fleet.LocalReference{Name: "local-profile"}, LocationRef: fleet.LocalReference{Name: "not-the-local-location"}, PowerState: "Off"}}
	localClass := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "local-class", UID: "consumer-class"}, Spec: fleet.MachineClassSpec{Source: &fleet.SourceReference{Project: "provider", Name: "local-class"}}}
	localProfile := profile.DeepCopyObject().(*fleet.MachineProfile)
	localProfile.UID = "consumer-profile"
	runner := Runner{
		Config:   Config{Cell: "only-local-cell"},
		Provider: Project{API: fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, profile).WithInterceptorFuncs(countWrites).Build(), Identity: controller.ProjectIdentity{AuthorityID: "test", RouteName: "provider", UID: "provider-project"}},
	}
	runner.Consumers[0] = Project{API: fake.NewClientBuilder().WithScheme(scheme).WithObjects(machine, localClass, localProfile).WithStatusSubresource(&fleet.Machine{}).Build(), Identity: controller.ProjectIdentity{AuthorityID: "test", RouteName: "consumer", UID: "consumer-project"}}
	// Allocate runs the location check first; it never reaches the
	// allocator, which this runner does not have.
	if err := runner.Allocate(ctx); !errors.Is(err, ErrLocationNotServed) {
		t.Fatalf("nonlocal Machine was not rejected by location: %v", err)
	}
	if _, err := runner.request(ctx, 0); !errors.Is(err, ErrLocationNotServed) {
		t.Fatalf("nonlocal Machine was not rejected by location: %v", err)
	}
	var stored fleet.Machine
	if err := runner.Consumers[0].API.Get(ctx, client.ObjectKeyFromObject(machine), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Finalizers) != 0 || providerWrites != 0 {
		t.Fatalf("wrong-location request left a finalizer %v or wrote the provider project %d times", stored.Finalizers, providerWrites)
	}
	// The served location is still refused when no cell is configured.
	stored.Spec.LocationRef.Name = Location
	if err := runner.Consumers[0].API.Update(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	runner.Config.Cell = ""
	if _, err := runner.request(ctx, 0); !errors.Is(err, ErrLocationNotServed) {
		t.Fatalf("location accepted without a bound cell: %v", err)
	}
	runner.Config.Cell = "only-local-cell"
	if _, err := runner.request(ctx, 0); err != nil {
		t.Fatalf("served location rejected: %v", err)
	}
}

func TestAdversarialReleaseRemovesFinalizerAfterStatus(t *testing.T) {
	ctx := context.Background()
	m := &fleet.Machine{ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: Namespace, UID: "machine", Finalizers: []string{"other.example/keep", allocation.MachineFinalizer}}}
	m.Status.CurrentInstallationRef = &fleet.LocalReference{Name: "installed"}
	install := &fleet.Installation{ObjectMeta: metav1.ObjectMeta{Name: "installed", Namespace: Namespace}, Status: fleet.InstallationStatus{Phase: "Ready", Conditions: []metav1.Condition{condition("Ready", metav1.ConditionTrue, 0, "Installed")}}}
	api := fake.NewClientBuilder().WithScheme(Scheme()).WithObjects(m, install).WithStatusSubresource(&fleet.Machine{}, &fleet.Installation{}).Build()
	var stored fleet.Machine
	if err := api.Get(ctx, client.ObjectKeyFromObject(m), &stored); err != nil {
		t.Fatal(err)
	}
	if err := publishRelease(ctx, api, &stored); err != nil {
		t.Fatal(err)
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(m), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.CurrentInstallationRef != nil {
		t.Fatal("released machine retains current installation")
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(install), install); err != nil {
		t.Fatal(err)
	}
	if meta.IsStatusConditionTrue(install.Status.Conditions, "Ready") || install.Status.Phase != "Released" {
		t.Fatal("cleaned installation still ready")
	}
	if stored.Status.Phase != "Released" || len(stored.Finalizers) != 1 || stored.Finalizers[0] != "other.example/keep" {
		t.Fatalf("release not published exactly: phase %q finalizers %v", stored.Status.Phase, stored.Finalizers)
	}
}
