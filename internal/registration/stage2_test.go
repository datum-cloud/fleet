package registration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/capacity"
	"go.datum.net/fleet/internal/controller"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// stageTwo is a handler whose cleaning resolver knows one policy and whose
// projector is "capacity-projector".
func stageTwo(t *testing.T) (*Handler, *inventory.Device) {
	t.Helper()
	handler, device := fixture(t)
	handler.Cleaning = capacity.Fixed(map[string]string{"tenant-reuse": "sha256:" + strings.Repeat("a", 64)})
	handler.Projectors = []string{"capacity-projector"}
	return handler, device
}

// update builds an update of registered capacity from old to the mutated
// copy.
func update(t *testing.T, device *inventory.Device, old func(*metal.MachineCapacity), next func(*metal.MachineCapacity)) admission.Request {
	t.Helper()
	request := registrationRequest(t, device)
	if old != nil {
		changeCapacity(&request, old)
	}
	previous := request.Object
	changeCapacity(&request, next)
	request.Operation, request.OldObject = admissionv1.Update, previous
	return request
}

func enable(capacity *metal.MachineCapacity) {
	capacity.Spec.AllocationPolicy, capacity.Spec.CleaningPolicyRef.Name = "Enabled", "tenant-reuse"
	capacity.Labels = map[string]string{PoolLabel: "general"}
}

func TestOnlyTheStageTwoFieldsChange(t *testing.T) {
	for name, mutate := range map[string]func(*metal.MachineCapacity){
		"finalizers": func(c *metal.MachineCapacity) { c.Finalizers = []string{"example.com/hold"} },
		"owner references": func(c *metal.MachineCapacity) {
			c.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "owner-uid"}}
		},
		"reclaim policy":   func(c *metal.MachineCapacity) { c.Spec.ReclaimPolicy = "Delete" },
		"Device reference": func(c *metal.MachineCapacity) { c.Spec.DeviceRef.Name = "another-server" },
		"another label":    func(c *metal.MachineCapacity) { c.Labels = map[string]string{"edited": "true"} },
		"another annotation": func(c *metal.MachineCapacity) {
			c.Annotations["example.com/note"] = "edited"
		},
	} {
		for _, state := range []string{"Disabled", "Enabled"} {
			t.Run(name+" while "+state, func(t *testing.T) {
				handler, device := stageTwo(t)
				var start func(*metal.MachineCapacity)
				if state == "Enabled" {
					start = enable
				}
				request := update(t, device, start, func(c *metal.MachineCapacity) {
					if start != nil {
						start(c)
					}
					mutate(c)
				})
				if response := handler.Handle(context.Background(), request); response.Allowed || response.Result.Message != mutationDisabled {
					t.Fatalf("an update of %s was %+v", name, response.Result)
				}
			})
		}
	}
	t.Run("an identical update of enabled capacity", func(t *testing.T) {
		handler, device := stageTwo(t)
		if response := handler.Handle(context.Background(), update(t, device, enable, enable)); !response.Allowed {
			t.Fatalf("an unchanged apply was denied: %+v", response.Result)
		}
	})
}

func TestEnablingChecksTheRegisteredDevice(t *testing.T) {
	handler, device := stageTwo(t)
	if response := handler.Handle(context.Background(), update(t, device, nil, enable)); !response.Allowed {
		t.Fatalf("enabling an eligible Device: %+v", response.Result)
	}
	for name, test := range map[string]struct {
		device func(*inventory.Device)
		want   string
	}{
		"another incarnation": {func(d *inventory.Device) { d.UID = "device-2" }, "another incarnation"},
		"deleting": {func(d *inventory.Device) {
			d.Finalizers, d.DeletionTimestamp = []string{"example.com/hold"}, &metav1.Time{}
		}, "another incarnation"},
		"placeholder serial": {func(d *inventory.Device) { d.Spec.Identity.Serial = "To Be Filled By O.E.M." }, "complete, non-placeholder identity"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, device := stageTwo(t)
			api := providerClient(t, handler)
			current := device.DeepCopyObject().(*inventory.Device)
			test.device(current)
			replace(t, api, device, current)
			response := handler.Handle(context.Background(), update(t, device, nil, enable))
			if response.Allowed || !strings.Contains(response.Result.Message, test.want) {
				t.Fatalf("enabling with %s: %+v", name, response.Result)
			}
		})
	}
	t.Run("a duplicate in another provider project", func(t *testing.T) {
		handler, device := stageTwo(t)
		twin := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "twin", Namespace: "default", UID: "twin-1"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "serial-1", SystemUUID: "22222222-2222-2222-2222-222222222222", Model: "test"}}}
		other := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(twin).Build()
		resolve := handler.Resolve
		handler.Projects = []string{"other-provider", "provider"}
		handler.Resolve = func(ctx context.Context, name string) (Project, error) {
			if name == "other-provider" {
				return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: name, UID: "project-2"}, Client: other}, nil
			}
			return resolve(ctx, name)
		}
		response := handler.Handle(context.Background(), update(t, device, nil, enable))
		if response.Allowed || !strings.Contains(response.Result.Message, "in another provider project") {
			t.Fatalf("enabling beside a duplicate in another project: %+v", response.Result)
		}
	})
}

// replace swaps the stored Device for another version of it.
func replace(t *testing.T, api client.Client, device, current *inventory.Device) {
	t.Helper()
	if err := api.Delete(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	current.ResourceVersion = ""
	deleting := current.DeletionTimestamp
	current.DeletionTimestamp = nil
	if err := api.Create(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if deleting != nil {
		if err := api.Delete(context.Background(), current); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrphanDeletionChecksTheIncarnation(t *testing.T) {
	deletion := func(t *testing.T, device *inventory.Device) admission.Request {
		request := registrationRequest(t, device)
		request.Operation, request.OldObject, request.Object = admissionv1.Delete, request.Object, runtime.RawExtension{}
		return request
	}
	t.Run("the same Active incarnation keeps its capacity", func(t *testing.T) {
		handler, device := stageTwo(t)
		if response := handler.Handle(context.Background(), deletion(t, device)); response.Allowed {
			t.Fatal("capacity of an Active Device was deleted")
		}
	})
	t.Run("another incarnation under the name lets it go", func(t *testing.T) {
		handler, device := stageTwo(t)
		current := device.DeepCopyObject().(*inventory.Device)
		current.UID = types.UID("device-2")
		replace(t, providerClient(t, handler), device, current)
		if response := handler.Handle(context.Background(), deletion(t, device)); !response.Allowed {
			t.Fatalf("orphaned capacity: %+v", response.Result)
		}
	})
}

func TestStatusWritesComeOnlyFromTheProjector(t *testing.T) {
	status := func(t *testing.T, device *inventory.Device, user authenticationv1.UserInfo) admission.Request {
		request := registrationRequest(t, device)
		request.Operation, request.SubResource, request.OldObject, request.UserInfo = admissionv1.Update, "status", request.Object, user
		return request
	}
	handler, device := stageTwo(t)
	extras := registrationRequest(t, device).UserInfo.Extra
	if response := handler.Handle(context.Background(), status(t, device, authenticationv1.UserInfo{Username: "capacity-projector", UID: "projector-uid", Extra: extras})); !response.Allowed {
		t.Fatalf("the projector's status write: %+v", response.Result)
	}
	if response := handler.Handle(context.Background(), status(t, device, authenticationv1.UserInfo{Username: "operator", UID: "actor-1", Extra: extras})); response.Allowed {
		t.Fatal("an operator wrote registered capacity status")
	}
	// The projector writes through the provider project, which sets the
	// project context; without it the write is refused.
	if response := handler.Handle(context.Background(), status(t, device, authenticationv1.UserInfo{Username: "capacity-projector", UID: "projector-uid"})); response.Allowed {
		t.Fatal("a status write without a project context was admitted")
	}
	handler.Projectors = nil
	if response := handler.Handle(context.Background(), status(t, device, authenticationv1.UserInfo{Username: "capacity-projector", UID: "projector-uid", Extra: extras})); response.Allowed {
		t.Fatal("a status write was admitted with no projector configured")
	}
}

var _ = json.Marshal
