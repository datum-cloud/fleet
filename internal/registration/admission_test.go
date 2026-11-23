package registration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, metal.AddToScheme, inventory.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func fixture(t *testing.T) (*Handler, *inventory.Device) {
	t.Helper()
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default", UID: "device-1", ResourceVersion: "1"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "SERIAL-1", SystemUUID: "11111111-1111-1111-1111-111111111111", Model: "test"}}}
	api := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(device).Build()
	return &Handler{Projects: []string{"provider"}, Resolve: func(context.Context, string) (Project, error) {
		return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "provider", UID: "project-1"}, Client: api}, nil
	}, Authorize: func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error { return nil }}, device
}

func registrationRequest(t *testing.T, device *inventory.Device) admission.Request {
	t.Helper()
	name := CapacityName(device.UID)
	capacity := metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("capacity-" + name), Annotations: map[string]string{DeviceUIDAnnotation: string(device.UID), DeviceVersionAnnotation: device.ResourceVersion, OperationAnnotation: "client-" + name}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	raw, err := json.Marshal(capacity)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{UID: types.UID("request-" + name), Name: name, Namespace: "default", Operation: admissionv1.Create, Resource: metav1.GroupVersionResource{Group: "metal.datumapis.com", Version: "v1alpha1", Resource: "machinecapacities"}, UserInfo: authenticationv1.UserInfo{Username: "operator", UID: "actor-1", Extra: map[string]authenticationv1.ExtraValue{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"provider"}}}, Object: runtime.RawExtension{Raw: raw}}}
}

func changeCapacity(request *admission.Request, mutate func(*metal.MachineCapacity)) {
	var capacity metal.MachineCapacity
	_ = json.Unmarshal(request.Object.Raw, &capacity)
	mutate(&capacity)
	request.Name = capacity.Name
	request.Object.Raw, _ = json.Marshal(capacity)
}

func providerClient(t *testing.T, handler *Handler) client.Client {
	t.Helper()
	project, err := handler.Resolve(context.Background(), "provider")
	if err != nil {
		t.Fatal(err)
	}
	return project.Client
}

func TestRegistrationAllowsLiveEligibleDeviceWithoutEffects(t *testing.T) {
	handler, device := fixture(t)
	request := registrationRequest(t, device)
	dryRun := true
	request.DryRun = &dryRun
	if response := handler.Handle(context.Background(), request); !response.Allowed {
		t.Fatalf("eligible registration denied: %s", response.Result.Message)
	}
	var capacities metal.MachineCapacityList
	if err := providerClient(t, handler).List(context.Background(), &capacities); err != nil || len(capacities.Items) != 0 {
		t.Fatal("admission caused public effects", err)
	}
}

func TestRegistrationNegatives(t *testing.T) {
	tests := map[string]func(*Handler, *inventory.Device, *admission.Request){
		"stale UID": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) {
				capacity.Annotations[DeviceUIDAnnotation] = "old"
				capacity.Name = CapacityName("old")
			})
		},
		"stale RV": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) { capacity.Annotations[DeviceVersionAnnotation] = "old" })
		},
		"authored name": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) { capacity.Name = "authored-name" })
		},
		"enabled": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) { capacity.Spec.AllocationPolicy = "Enabled" })
		},
		"invented policy": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) {
				capacity.Spec.CleaningPolicyRef.Name = "synthetic-tenant-reuse-v1"
			})
		},
		"owner reference": func(_ *Handler, device *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) {
				capacity.OwnerReferences = []metav1.OwnerReference{{APIVersion: inventory.GroupVersion.String(), Kind: "Device", Name: device.Name, UID: device.UID}}
			})
		},
		"no baseline": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) { capacity.Annotations = nil })
		},
		"direct update": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			request.Operation = admissionv1.Update
		},
		"direct delete": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			request.Operation = admissionv1.Delete
		},
		"status": func(_ *Handler, _ *inventory.Device, request *admission.Request) { request.SubResource = "status" },
		"other resource": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			request.Resource.Resource = "devices"
		},
		"missing project": func(_ *Handler, _ *inventory.Device, request *admission.Request) { request.UserInfo.Extra = nil },
		"missing actor":   func(_ *Handler, _ *inventory.Device, request *admission.Request) { request.UserInfo.UID = "" },
		"organization context": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			request.UserInfo.Extra[iam.ParentKindExtraKey] = authenticationv1.ExtraValue{"Organization"}
		},
		"wrong project": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			request.UserInfo.Extra[iam.ParentNameExtraKey] = authenticationv1.ExtraValue{"consumer"}
		},
		"namespace": func(_ *Handler, _ *inventory.Device, request *admission.Request) { request.Namespace = "other" },
		"project lifetime unavailable": func(handler *Handler, _ *inventory.Device, _ *admission.Request) {
			handler.Resolve = func(context.Context, string) (Project, error) { return Project{}, errors.New("deleting") }
		},
		"capacity permission revoked": func(handler *Handler, _ *inventory.Device, _ *admission.Request) {
			handler.Authorize = func(_ context.Context, _ Project, _ authenticationv1.UserInfo, _, resource, _ string) error {
				if resource == "machinecapacities" {
					return errors.New("denied")
				}
				return nil
			}
		},
		"device permission revoked": func(handler *Handler, _ *inventory.Device, _ *admission.Request) {
			handler.Authorize = func(_ context.Context, _ Project, _ authenticationv1.UserInfo, _, resource, _ string) error {
				if resource == "devices" {
					return errors.New("denied")
				}
				return nil
			}
		},
		"missing device": func(handler *Handler, device *inventory.Device, _ *admission.Request) {
			if err := providerClient(t, handler).Delete(context.Background(), device); err != nil {
				t.Fatal(err)
			}
		},
		"partial identity": func(handler *Handler, device *inventory.Device, request *admission.Request) {
			device.Spec.Identity.SystemUUID = ""
			if err := providerClient(t, handler).Update(context.Background(), device); err != nil {
				t.Fatal(err)
			}
			*request = registrationRequest(t, device)
		},
		"unresolved legacy": func(handler *Handler, _ *inventory.Device, _ *admission.Request) {
			if err := providerClient(t, handler).Create(context.Background(), &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "missing"}, AllocationPolicy: "Enabled"}}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			handler, device := fixture(t)
			request := registrationRequest(t, device)
			mutate(handler, device, &request)
			if response := handler.Handle(context.Background(), request); response.Allowed {
				t.Fatal("unsafe registration allowed")
			}
		})
	}
}

func TestMissingDeviceIsNamed(t *testing.T) {
	handler, device := fixture(t)
	if err := providerClient(t, handler).Delete(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	response := handler.Handle(context.Background(), registrationRequest(t, device))
	if response.Allowed || !strings.Contains(response.Result.Message, "does not exist") {
		t.Fatalf("missing Device not reported: %+v", response.Result)
	}
}

func TestUnauthorizedCallerCannotProbeDeviceExistence(t *testing.T) {
	handler, device := fixture(t)
	if err := providerClient(t, handler).Delete(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	handler.Authorize = func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error {
		return errors.New("current get permission on devices is required")
	}
	response := handler.Handle(context.Background(), registrationRequest(t, device))
	if response.Allowed || strings.Contains(response.Result.Message, "does not exist") {
		t.Fatalf("unauthorized caller learned Device existence: %+v", response.Result)
	}
}

func TestSharedAliasesAcrossLiveDevices(t *testing.T) {
	for _, alias := range []string{"serial", "uuid", "normalized serial", "provider"} {
		t.Run(alias, func(t *testing.T) {
			handler, device := fixture(t)
			api := providerClient(t, handler)
			duplicate := device.DeepCopyObject().(*inventory.Device)
			duplicate.Name, duplicate.UID, duplicate.ResourceVersion = "alias", "alias-uid", ""
			duplicate.Spec.Identity.Serial = "other"
			duplicate.Spec.Identity.SystemUUID = "22222222-2222-2222-2222-222222222222"
			switch alias {
			case "serial":
				duplicate.Spec.Identity.Serial = device.Spec.Identity.Serial
			case "normalized serial":
				duplicate.Spec.Identity.Serial = "  serial-1 "
			case "uuid":
				duplicate.Spec.Identity.SystemUUID = device.Spec.Identity.SystemUUID
			case "provider":
				device.Spec.ProviderHostRef = &inventory.ExternalReference{System: "Provider", Type: "server", ID: "shared-asset"}
				duplicate.Spec.ProviderHostRef = &inventory.ExternalReference{System: "provider", Type: " SERVER", ID: "shared-asset"}
				if err := api.Update(context.Background(), device); err != nil {
					t.Fatal(err)
				}
			}
			if err := api.Create(context.Background(), duplicate); err != nil {
				t.Fatal(err)
			}
			for _, selected := range []*inventory.Device{device, duplicate} {
				if err := api.Get(context.Background(), client.ObjectKeyFromObject(selected), selected); err != nil {
					t.Fatal(err)
				}
				response := handler.Handle(context.Background(), registrationRequest(t, selected))
				if response.Allowed || !strings.Contains(response.Result.Message, "shares recorded hardware identity") {
					t.Fatalf("duplicate identity accepted for %s: %+v", selected.Name, response.Result)
				}
			}
			if err := api.Delete(context.Background(), duplicate); err != nil {
				t.Fatal(err)
			}
			if err := api.Get(context.Background(), client.ObjectKeyFromObject(device), device); err != nil {
				t.Fatal(err)
			}
			if response := handler.Handle(context.Background(), registrationRequest(t, device)); !response.Allowed {
				t.Fatalf("resolved duplicate still blocks registration: %s", response.Result.Message)
			}
		})
	}
}

func TestCrossProjectAmbiguityAndSharedProviderAlias(t *testing.T) {
	for _, shared := range []string{"serial", "uuid", "provider"} {
		t.Run(shared, func(t *testing.T) {
			handler, device := fixture(t)
			first, _ := handler.Resolve(context.Background(), "provider")
			other := device.DeepCopyObject().(*inventory.Device)
			other.Name, other.UID = "second", "device-2"
			other.Spec.Identity.Serial = "second-serial"
			other.Spec.Identity.SystemUUID = "22222222-2222-2222-2222-222222222222"
			switch shared {
			case "serial":
				other.Spec.Identity.Serial = " serial-1 "
			case "uuid":
				other.Spec.Identity.SystemUUID = device.Spec.Identity.SystemUUID
			case "provider":
				device.Spec.ProviderHostRef = &inventory.ExternalReference{System: "provider", Type: "server", ID: "shared-asset"}
				other.Spec.ProviderHostRef = device.Spec.ProviderHostRef
				if err := first.Client.Update(context.Background(), device); err != nil {
					t.Fatal(err)
				}
			}
			second := fake.NewClientBuilder().WithScheme(first.Client.Scheme()).WithObjects(other).Build()
			handler.Projects = []string{"provider", "second"}
			handler.Resolve = func(_ context.Context, name string) (Project, error) {
				if name == "provider" {
					return first, nil
				}
				return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "second", UID: "project-2"}, Client: second}, nil
			}
			firstRequest := registrationRequest(t, device)
			secondRequest := registrationRequest(t, other)
			secondRequest.UserInfo.Extra[iam.ParentNameExtraKey] = authenticationv1.ExtraValue{"second"}
			var workers sync.WaitGroup
			for _, request := range []admission.Request{firstRequest, secondRequest} {
				workers.Add(1)
				go func() {
					defer workers.Done()
					response := handler.Handle(context.Background(), request)
					if response.Allowed {
						t.Error("cross-project alias ambiguity accepted")
					}
					if strings.Contains(response.Result.Message, "second/") || strings.Contains(response.Result.Message, "default/server") {
						t.Errorf("conflict in another provider project was named: %s", response.Result.Message)
					}
				}()
			}
			workers.Wait()
		})
	}
}

func TestDeviceAlreadyBackingCapacity(t *testing.T) {
	for _, binding := range []string{"device reference", "device UID annotation"} {
		t.Run(binding, func(t *testing.T) {
			handler, device := fixture(t)
			existing := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "earlier-record", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
			if binding == "device UID annotation" {
				// A capacity whose Device was renamed away still binds the UID.
				other := device.DeepCopyObject().(*inventory.Device)
				other.Name, other.UID, other.ResourceVersion = "renamed", "device-renamed", ""
				other.Spec.Identity.Serial, other.Spec.Identity.SystemUUID = "renamed-serial", "33333333-3333-3333-3333-333333333333"
				if err := providerClient(t, handler).Create(context.Background(), other); err != nil {
					t.Fatal(err)
				}
				existing.Spec.DeviceRef.Name = other.Name
				existing.Annotations = map[string]string{DeviceUIDAnnotation: string(device.UID)}
			}
			if err := providerClient(t, handler).Create(context.Background(), existing); err != nil {
				t.Fatal(err)
			}
			response := handler.Handle(context.Background(), registrationRequest(t, device))
			if response.Allowed || !strings.Contains(response.Result.Message, "already backs capacity earlier-record") {
				t.Fatalf("second capacity for one Device accepted: %+v", response.Result)
			}
		})
	}
}

func TestReplacementDeviceUnderSameNameCannotRegister(t *testing.T) {
	handler, device := fixture(t)
	api := providerClient(t, handler)
	registered := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: CapacityName(device.UID), Namespace: "default", Annotations: map[string]string{DeviceUIDAnnotation: string(device.UID)}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	if err := api.Create(context.Background(), registered); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	replacement := device.DeepCopyObject().(*inventory.Device)
	replacement.UID, replacement.ResourceVersion = "device-replacement", ""
	replacement.Spec.Identity.Serial, replacement.Spec.Identity.SystemUUID = "replacement-serial", "44444444-4444-4444-4444-444444444444"
	if err := api.Create(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if handler.Handle(context.Background(), registrationRequest(t, replacement)).Allowed {
		t.Fatal("replacement Device adopted capacity that names its predecessor")
	}
}

// Capacity whose Device was deleted has no resolvable identity, so it blocks
// every registration in the provider scope, not only a same-name
// replacement. The operator guide documents this and its remediation.
func TestOrphanedCapacityBlocksUnrelatedRegistration(t *testing.T) {
	handler, device := fixture(t)
	api := providerClient(t, handler)
	registered := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: CapacityName(device.UID), Namespace: "default", Annotations: map[string]string{DeviceUIDAnnotation: string(device.UID)}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	if err := api.Create(context.Background(), registered); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	unrelated := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default", UID: "device-unrelated"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "unrelated-serial", SystemUUID: "55555555-5555-5555-5555-555555555555", Model: "test"}}}
	if err := api.Create(context.Background(), unrelated); err != nil {
		t.Fatal(err)
	}
	response := handler.Handle(context.Background(), registrationRequest(t, unrelated))
	if response.Allowed || !strings.Contains(response.Result.Message, "capacity "+registered.Namespace+"/"+registered.Name+" has unresolved physical identity") {
		t.Fatalf("registration beside orphaned capacity: %+v", response.Result)
	}

	// Remediation: re-record the deleted machine under its old name and
	// identity as Retired. The capacity resolves again and stays Disabled,
	// the re-recorded Device cannot register, and other Devices can.
	restored := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: device.Name, Namespace: "default", UID: "device-restored"}, Spec: inventory.DeviceSpec{Lifecycle: inventory.DeviceRetired, Identity: device.Spec.Identity}}
	if err := api.Create(context.Background(), restored); err != nil {
		t.Fatal(err)
	}
	if response := handler.Handle(context.Background(), registrationRequest(t, unrelated)); !response.Allowed {
		t.Fatalf("registration still blocked after re-recording the Device: %s", response.Result.Message)
	}
	if handler.Handle(context.Background(), registrationRequest(t, restored)).Allowed {
		t.Fatal("re-recorded Retired Device registered")
	}
}

func TestNonConflictingLegacyCapacityAndAllocationRemainUntouched(t *testing.T) {
	handler, device := fixture(t)
	ctx := context.Background()
	api := providerClient(t, handler)
	legacyDevice := device.DeepCopyObject().(*inventory.Device)
	legacyDevice.Name, legacyDevice.UID, legacyDevice.ResourceVersion = "legacy-device", "legacy-device-uid", ""
	legacyDevice.Spec.Identity.Serial, legacyDevice.Spec.Identity.SystemUUID = "legacy-serial", "22222222-2222-2222-2222-222222222222"
	legacy := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default", UID: "legacy-uid"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: legacyDevice.Name}, AllocationPolicy: "Enabled", ReclaimPolicy: "Retain", CleaningPolicyRef: metal.LocalReference{Name: "synthetic-tenant-reuse-v1"}}, Status: metal.MachineCapacityStatus{Conditions: []metav1.Condition{{Type: "Available", Status: metav1.ConditionFalse, Reason: "Occupied"}}}}
	allocation := &metal.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: "occupied", Namespace: "default", UID: "allocation-uid"}}
	for _, object := range []client.Object{legacyDevice, legacy, allocation} {
		if err := api.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	beforeCapacity, _ := json.Marshal(legacy)
	beforeAllocation, _ := json.Marshal(allocation)
	if response := handler.Handle(ctx, registrationRequest(t, device)); !response.Allowed {
		t.Fatalf("non-conflicting legacy capacity blocked registration: %s", response.Result.Message)
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(legacy), legacy); err != nil {
		t.Fatal(err)
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
		t.Fatal(err)
	}
	afterCapacity, _ := json.Marshal(legacy)
	afterAllocation, _ := json.Marshal(allocation)
	if string(beforeCapacity) != string(afterCapacity) || string(beforeAllocation) != string(afterAllocation) {
		t.Fatal("registration modified legacy state")
	}
	if err := api.Get(ctx, client.ObjectKeyFromObject(legacyDevice), legacyDevice); err != nil {
		t.Fatal(err)
	}
	if handler.Handle(ctx, registrationRequest(t, legacyDevice)).Allowed {
		t.Fatal("occupied legacy asset was registered")
	}
}

func TestCapacityNameIsDerivedFromDeviceUID(t *testing.T) {
	name := CapacityName("0b1f0a52-6f0e-4f43-9c1e-4c8b7f3d2a10")
	if name != "device-0b1f0a52-6f0e-4f43-9c1e-4c8b7f3d2a10" || len(name) > 63 {
		t.Fatalf("unexpected capacity name %q", name)
	}
}
