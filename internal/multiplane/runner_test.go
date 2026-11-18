package multiplane

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	fleet "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type disconnected struct{ calls int }

func (d *disconnected) Execute(context.Context, simulator.Request) (simulator.OperationResult, error) {
	d.calls++
	return simulator.OperationResult{}, executor.ErrOutcomeUnknown
}
func (d *disconnected) Lookup(context.Context, string) (simulator.OperationResult, bool, error) {
	return simulator.OperationResult{}, false, executor.ErrOutcomeUnknown
}
func TestIndependentProjectsContendAndUnknownRetainsCustody(t *testing.T) {
	ctx := context.Background()
	s := Scheme()
	private := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetUID(types.UID("private-" + obj.GetName()))
		return c.Create(ctx, obj, opts...)
	}}).Build()
	var profile fleet.MachineProfile
	profile.Name = "local-profile"
	profile.UID = "source-profile"
	profile.Spec.CPU.Architecture = "amd64"
	cores := int64(2)
	profile.Spec.CPU.Cores.Min = &cores
	profile.Spec.Memory.Capacity.Min = "2Gi"
	class := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "local-class", UID: "source-class"}, Spec: fleet.MachineClassSpec{Capacity: &fleet.CapacityPolicy{MachineCapacities: fleet.MachineSelector{Namespace: "fleet-provider"}}}, Status: fleet.MachineClassStatus{EvaluatedRevision: "v1"}}
	capacity := &fleet.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "local-capacity", Namespace: "fleet-provider", UID: "capacity", Generation: 1}, Spec: fleet.MachineCapacitySpec{DeviceRef: fleet.LocalReference{Name: "synthetic-device-central-01"}, AllocationPolicy: "Enabled"}, Status: fleet.MachineCapacityStatus{Hardware: fleet.Hardware{CPU: fleet.HardwareCPU{Architecture: "amd64", Cores: 2}, Memory: fleet.HardwareMemory{Capacity: "2Gi"}}}}
	for _, kind := range []string{"Verified", "Healthy", "Prepared", "Available"} {
		capacity.Status.Conditions = append(capacity.Status.Conditions, condition(kind, metav1.ConditionTrue, 1, "Fixture"))
	}
	provider := fake.NewClientBuilder().WithScheme(s).WithObjects(class, &profile, capacity).Build()
	backend := &disconnected{}
	coordinator, err := controller.NewCoordinator(private, PrivateNamespace, backend, controller.LocalDigestImageResolver{}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := Runner{Config: Config{Cell: "cell-a"}, Private: private, Coordinator: coordinator, Provider: Project{API: provider, Identity: controller.ProjectIdentity{AuthorityID: "test", RouteName: "provider", UID: "provider-project"}}}
	for index, name := range []string{"a", "b"} {
		ns := &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace, UID: types.UID(name + "-namespace")}}
		// Identical namespace, Machine name AND UID: only project authority separates them.
		m := &fleet.Machine{ObjectMeta: metav1.ObjectMeta{Name: "allocation", Namespace: Namespace, UID: "same-machine-uid"}, Spec: fleet.MachineSpec{MachineClassRef: fleet.LocalReference{Name: "local-class"}, MachineProfileRef: fleet.LocalReference{Name: "local-profile"}, LocationRef: fleet.LocalReference{Name: "local"}, PowerState: "Off"}}
		localClass := &fleet.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: "local-class", UID: types.UID(name + "-class")}, Spec: fleet.MachineClassSpec{Source: &fleet.SourceReference{Project: "provider", Name: "local-class"}}}
		cp := profile.DeepCopyObject().(*fleet.MachineProfile)
		cp.UID = types.UID(name + "-profile")
		r.Consumers[index] = Project{API: fake.NewClientBuilder().WithScheme(s).WithObjects(ns, m, localClass, cp).Build(), Identity: controller.ProjectIdentity{AuthorityID: "test", RouteName: name, UID: types.UID(name + "-project")}}
	}
	a, err := r.request(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.request(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i, q := range []controller.AllocationRequest{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); _, results[i] = r.Coordinator.Allocate(ctx, q) }()
	}
	wg.Wait()
	winner, loser := -1, -1
	for i, err := range results {
		if err == nil {
			if winner != -1 {
				t.Fatal("both projects won")
			}
			winner = i
		} else if errors.Is(err, controller.ErrInsufficientCapacity) || errors.Is(err, controller.ErrRecordInvalid) {
			loser = i
		} else {
			t.Fatal(err)
		}
	}
	if winner < 0 || loser < 0 {
		t.Fatal("contention did not produce one winner", results)
	}
	claims := []controller.AllocationRequest{a, b}
	_, err = r.Coordinator.ReleaseScoped(ctx, claims[winner].ConsumerProject, claims[winner].Claim.UID)
	if !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("disconnected cleanup must be unknown: %v", err)
	}
	_, err = r.Coordinator.Allocate(ctx, claims[loser])
	if !(errors.Is(err, controller.ErrInsufficientCapacity) || errors.Is(err, controller.ErrRecordInvalid)) {
		t.Fatalf("unknown cleanup exposed capacity: %v", err)
	}
}
