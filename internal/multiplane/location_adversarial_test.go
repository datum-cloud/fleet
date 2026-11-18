package multiplane

import (
	"context"
	"errors"
	"testing"
	"time"

	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestAdversarialRejectNonlocalMachineBeforeAllocation(t *testing.T) {
	ctx := context.Background()
	scheme := Scheme()
	private := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, api client.WithWatch, object client.Object, options ...client.CreateOption) error {
		object.SetUID(types.UID("private-" + object.GetName()))
		return api.Create(ctx, object, options...)
	}}).Build()
	coordinator, err := controller.NewCoordinator(private, PrivateNamespace, &disconnected{}, controller.LocalDigestImageResolver{}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	profile := &fleet.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: "local-profile", UID: "source-profile"}}
	profile.Spec.CPU.Architecture = "amd64"
	cores := int64(2)
	profile.Spec.CPU.Cores.Min = &cores
	profile.Spec.Memory.Capacity.Min = "2Gi"
	class := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "local-class", UID: "source-class"}, Spec: fleet.MachineClassSpec{Capacity: &fleet.CapacityPolicy{MachineCapacities: fleet.MachineSelector{Namespace: "fleet-provider"}}}, Status: fleet.MachineClassStatus{EvaluatedRevision: "v1"}}
	capacity := &fleet.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "local-capacity", Namespace: "fleet-provider", UID: "capacity", Generation: 1}, Spec: fleet.MachineCapacitySpec{DeviceRef: fleet.LocalReference{Name: "synthetic-device-central-01"}, AllocationPolicy: "Enabled"}, Status: fleet.MachineCapacityStatus{Hardware: fleet.Hardware{CPU: fleet.HardwareCPU{Architecture: "amd64", Cores: 2}, Memory: fleet.HardwareMemory{Capacity: "2Gi"}}}}
	for _, kind := range []string{"Verified", "Healthy", "Prepared", "Available"} {
		capacity.Status.Conditions = append(capacity.Status.Conditions, condition(kind, metav1.ConditionTrue, 1, "Fixture"))
	}
	machine := &fleet.Machine{ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: Namespace, UID: "machine"}, Spec: fleet.MachineSpec{MachineClassRef: fleet.LocalReference{Name: "local-class"}, MachineProfileRef: fleet.LocalReference{Name: "local-profile"}, LocationRef: fleet.LocalReference{Name: "not-the-local-location"}, PowerState: "Off"}}
	namespace := &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace, UID: "consumer-namespace"}}
	localClass := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "local-class", UID: "consumer-class"}, Spec: fleet.MachineClassSpec{Source: &fleet.SourceReference{Project: "provider", Name: "local-class"}}}
	localProfile := profile.DeepCopyObject().(*fleet.MachineProfile)
	localProfile.UID = "consumer-profile"
	runner := Runner{
		Config: Config{Cell: "only-local-cell"}, Private: private, Coordinator: coordinator,
		Provider: Project{API: fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, profile, capacity).Build(), Identity: controller.ProjectIdentity{AuthorityID: "test", RouteName: "provider", UID: "provider-project"}},
	}
	runner.Consumers[0] = Project{API: fake.NewClientBuilder().WithScheme(scheme).WithObjects(machine, namespace, localClass, localProfile).WithStatusSubresource(&fleet.Machine{}).Build(), Identity: controller.ProjectIdentity{AuthorityID: "test", RouteName: "consumer", UID: "consumer-project"}}
	if _, err := runner.request(ctx, 0); !errors.Is(err, ErrLocationNotServed) {
		t.Fatalf("nonlocal Machine was not rejected by location: %v", err)
	}
	// Rejection precedes custody, synthetic authority records and finalizers.
	var secrets core.SecretList
	if err := private.List(ctx, &secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("wrong-location request retained %d private records", len(secrets.Items))
	}
	var stored fleet.Machine
	if err := runner.Consumers[0].API.Get(ctx, client.ObjectKeyFromObject(machine), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Finalizers) != 0 {
		t.Fatal("wrong-location request installed a lifecycle finalizer")
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
	req, err := runner.request(ctx, 0)
	if err != nil {
		t.Fatalf("served location rejected: %v", err)
	}
	if _, err := coordinator.ReleaseScoped(ctx, req.ConsumerProject, req.Claim.UID); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.CompleteExternalRelease(ctx, req.Claim.UID, req.Authority, req.ExternalHold, req.ConsumerProject)
	if err != nil || !result.SafeToRemoveFinalizer {
		t.Fatalf("release: %+v %v", result, err)
	}
	if err := publishRelease(ctx, runner.Consumers[0].API, req.Claim); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.request(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := runner.Consumers[0].API.Get(ctx, client.ObjectKeyFromObject(machine), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Finalizers) != 0 {
		t.Fatal("request restored finalizer after terminal custody")
	}
}

func TestAdversarialReleaseRemovesFinalizerAfterStatus(t *testing.T) {
	ctx := context.Background()
	m := &fleet.Machine{ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: Namespace, UID: "machine", Finalizers: []string{"other.example/keep", controller.MachineFinalizer}}}
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
