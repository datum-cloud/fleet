package allocation

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/sealed"
)

// TestAnInstallationNameHeldByAnotherOperationIsAConflict: Install returns
// an operation that already holds the Installation's name only if it is
// this request's. One for another Device incarnation, or one that disagrees
// on whether bootstrap data was admitted, is a conflict, whether Install
// finds it first or loses the create race to it.
func TestAnInstallationNameHeldByAnotherOperationIsAConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ops.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	allocation := &fleetv1.MachineAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: "alloc-example", Labels: map[string]string{fleetv1.LabelDeviceUID: "device-1"}},
		Spec: fleetv1.MachineAllocationSpec{
			DeviceRef:     fleetv1.AllocatedDeviceReference{Name: "server-example-001"},
			Hardware:      fleetv1.Hardware{CPU: fleetv1.HardwareCPU{Architecture: "amd64"}, Firmware: &fleetv1.HardwareFirmware{BootMode: fleetv1.FirmwareUEFI}},
			BootInterface: "primary",
			TargetDisks:   []string{"disk-0"},
		},
		Status: fleetv1.MachineAllocationStatus{Phase: fleetv1.AllocationActive},
	}
	request := func(bootstrap bool) Installation {
		installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Name: "example-os", Namespace: "default", UID: "installation-1"}, Spec: fleetv1.InstallationSpec{ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}}}
		resolved := image()
		resolved.Definition.BootstrapFormats = []string{fleetv1.BootstrapNoCloud}
		r := Installation{Installation: installation, Image: resolved}
		if bootstrap {
			installation.Spec.BootstrapConfigRef = &fleetv1.LocalReference{Name: "example-bootstrap"}
			r.Bootstrap = &sealed.Payload{SSHAuthorizedKeys: "ssh-ed25519 AAAAExampleOnly"}
		}
		return r
	}
	// admitted is the operation Install writes for a request into an empty
	// provider project.
	admitted := func(bootstrap bool) *ops.DeviceInstallation {
		a := &Allocator{Provider: fake.NewClientBuilder().WithScheme(scheme).Build(), Namespace: "default"}
		operation, err := a.Install(context.Background(), allocation, request(bootstrap))
		if err != nil {
			t.Fatal(err)
		}
		operation.ResourceVersion = ""
		return operation
	}
	for name, test := range map[string]struct {
		change    func(*ops.DeviceInstallation)
		held      bool
		bootstrap bool
		ok        bool
	}{
		"this request's operation":                 {change: func(*ops.DeviceInstallation) {}, ok: true},
		"this request's operation with bootstrap":  {held: true, bootstrap: true, change: func(*ops.DeviceInstallation) {}, ok: true},
		"another Device incarnation":               {change: func(o *ops.DeviceInstallation) { o.Labels[ops.LabelDeviceUID] = "device-0" }},
		"another Installation incarnation":         {change: func(o *ops.DeviceInstallation) { o.Labels[ops.LabelInstallationUID] = "installation-0" }},
		"other inputs":                             {change: func(o *ops.DeviceInstallation) { o.Spec.TargetDisks = []string{"disk-1"} }},
		"bootstrap data the request does not have": {held: true, change: func(*ops.DeviceInstallation) {}},
		"no bootstrap data where the request has":  {bootstrap: true, change: func(*ops.DeviceInstallation) {}},
	} {
		for _, race := range []bool{false, true} {
			label := name + ", found first"
			if race {
				label = name + ", after a lost create race"
			}
			t.Run(label, func(t *testing.T) {
				existing := admitted(test.held)
				test.change(existing)
				// A lost race: the first read finds nothing, and the
				// create then meets the other admission's operation.
				hidden := race
				c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
						if _, ok := object.(*ops.DeviceInstallation); ok && hidden {
							hidden = false
							return apierrors.NewNotFound(ops.GroupVersion.WithResource("deviceinstallations").GroupResource(), key.Name)
						}
						return c.Get(ctx, key, object, opts...)
					},
				}).Build()
				a := &Allocator{Provider: c, Namespace: "default"}
				operation, err := a.Install(context.Background(), allocation, request(test.bootstrap))
				if test.ok {
					if err != nil || operation == nil || operation.Name != existing.Name {
						t.Fatalf("operation = %v, %v", operation, err)
					}
					return
				}
				if !errors.Is(err, ErrConflict) || operation != nil {
					t.Fatalf("operation = %v, err = %v; want a conflict", operation, err)
				}
				if hidden {
					t.Fatal("the race was not exercised")
				}
			})
		}
	}
}
