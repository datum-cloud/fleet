package registration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

func fixture(t *testing.T) (*Handler, *inventory.Device) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, metal.AddToScheme, inventory.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server", Namespace: "default", UID: "device-1", ResourceVersion: "1"}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "SERIAL-1", SystemUUID: "11111111-1111-1111-1111-111111111111", Model: "test"}}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-1"}}
	api := fake.NewClientBuilder().WithScheme(scheme).WithObjects(device, namespace).Build()
	encoded, _ := json.Marshal(Ledger{Authority: "milo", Projects: []string{"provider"}})
	private := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: LedgerName, Namespace: "private", UID: "ledger-1"}, Data: map[string][]byte{"ledger.json": encoded}}).Build()
	return &Handler{Store: Store{Client: private, Namespace: "private", Authority: "milo", Projects: []string{"provider"}}, Resolve: func(context.Context, string) (Project, error) {
		return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "provider", UID: "project-1"}, Client: api}, nil
	}, Authorize: func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error { return nil }}, device
}

func registrationRequest(t *testing.T, device *inventory.Device, name string) admission.Request {
	t.Helper()
	capacity := metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("capacity-" + name), Annotations: map[string]string{DeviceUIDAnnotation: string(device.UID), DeviceVersionAnnotation: device.ResourceVersion, OperationAnnotation: "client-" + name}}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device.Name}, AllocationPolicy: "Disabled", ReclaimPolicy: "Retain"}}
	raw, err := json.Marshal(capacity)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{UID: types.UID("request-" + name), Name: name, Namespace: "default", Operation: admissionv1.Create, Resource: metav1.GroupVersionResource{Group: "metal.datumapis.com", Version: "v1alpha1", Resource: "machinecapacities"}, UserInfo: authenticationv1.UserInfo{Username: "operator", UID: "actor-1", Extra: map[string]authenticationv1.ExtraValue{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"provider"}}}, Object: runtime.RawExtension{Raw: raw}}}
}

func TestConcurrentRegistrationReservesOnce(t *testing.T) {
	handler, device := fixture(t)
	var allowed atomic.Int32
	var workers sync.WaitGroup
	for index := 0; index < 20; index++ {
		request := registrationRequest(t, device, fmt.Sprintf("capacity-%d", index))
		workers.Add(1)
		go func() {
			defer workers.Done()
			if handler.Handle(context.Background(), request).Allowed {
				allowed.Add(1)
			}
		}()
	}
	workers.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("allowed %d registrations", allowed.Load())
	}
	_, ledger, err := handler.Store.Read(context.Background())
	if err != nil || len(ledger.Entries) != 1 {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
	entry := ledger.Entries[0]
	if entry.Actor.UID != "actor-1" || entry.DeviceUID != device.UID || entry.DeviceVersion != device.ResourceVersion || entry.ProjectUID != "project-1" || entry.NamespaceUID != "namespace-1" || entry.Outcome != "Unknown" {
		t.Fatalf("authority not retained: %+v", entry)
	}
	project, _ := handler.Resolve(context.Background(), "provider")
	var capacities metal.MachineCapacityList
	if err := project.Client.List(context.Background(), &capacities); err != nil || len(capacities.Items) != 0 {
		t.Fatal("admission caused public effects")
	}
}

func TestRegistrationNegatives(t *testing.T) {
	tests := map[string]func(*Handler, *inventory.Device, *admission.Request){
		"stale UID": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) { capacity.Annotations[DeviceUIDAnnotation] = "old" })
		},
		"stale RV": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) { capacity.Annotations[DeviceVersionAnnotation] = "old" })
		},
		"enabled": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) { capacity.Spec.AllocationPolicy = "Enabled" })
		},
		"invented policy": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			changeCapacity(request, func(capacity *metal.MachineCapacity) {
				capacity.Spec.CleaningPolicyRef.Name = "synthetic-tenant-reuse-v1"
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
		"status":          func(_ *Handler, _ *inventory.Device, request *admission.Request) { request.SubResource = "status" },
		"missing project": func(_ *Handler, _ *inventory.Device, request *admission.Request) { request.UserInfo.Extra = nil },
		"wrong project": func(_ *Handler, _ *inventory.Device, request *admission.Request) {
			request.UserInfo.Extra[iam.ParentNameExtraKey] = authenticationv1.ExtraValue{"consumer"}
		},
		"namespace": func(_ *Handler, _ *inventory.Device, request *admission.Request) { request.Namespace = "other" },
		"permission revoked": func(handler *Handler, _ *inventory.Device, _ *admission.Request) {
			handler.Authorize = func(context.Context, Project, authenticationv1.UserInfo, string, string, string) error {
				return errors.New("denied")
			}
		},
		"partial identity": func(handler *Handler, device *inventory.Device, request *admission.Request) {
			project, _ := handler.Resolve(context.Background(), "provider")
			device.Spec.Identity.SystemUUID = ""
			if err := project.Client.Update(context.Background(), device); err != nil {
				t.Fatal(err)
			}
			*request = registrationRequest(t, device, request.Name)
		},
		"unresolved legacy": func(handler *Handler, _ *inventory.Device, _ *admission.Request) {
			project, _ := handler.Resolve(context.Background(), "provider")
			if err := project.Client.Create(context.Background(), &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: "missing"}, AllocationPolicy: "Enabled"}}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			handler, device := fixture(t)
			request := registrationRequest(t, device, "record")
			mutate(handler, device, &request)
			if response := handler.Handle(context.Background(), request); response.Allowed {
				t.Fatal("unsafe registration allowed")
			}
			_, ledger, err := handler.Store.Read(context.Background())
			if err != nil || len(ledger.Entries) != 0 {
				t.Fatal("negative modified registration ledger", err)
			}
		})
	}
}

func changeCapacity(request *admission.Request, mutate func(*metal.MachineCapacity)) {
	var capacity metal.MachineCapacity
	_ = json.Unmarshal(request.Object.Raw, &capacity)
	mutate(&capacity)
	request.Object.Raw, _ = json.Marshal(capacity)
}

func TestUnknownRecoveryDoesNotAdoptCopiedAnnotation(t *testing.T) {
	handler, device := fixture(t)
	request := registrationRequest(t, device, "record")
	if !handler.Handle(context.Background(), request).Allowed {
		t.Fatal("registration denied")
	}
	restarted := *handler
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if handler.Handle(context.Background(), registrationRequest(t, device, "different-name")).Allowed {
		t.Fatal("unknown reservation was released")
	}
	var recreated metal.MachineCapacity
	_ = json.Unmarshal(request.Object.Raw, &recreated)
	recreated.UID = "recreated-with-copied-annotations"
	project, _ := handler.Resolve(context.Background(), "provider")
	if err := project.Client.Create(context.Background(), &recreated); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, ledger, _ := handler.Store.Read(context.Background())
	if ledger.Entries[0].Outcome != "IncarnationConflict" {
		t.Fatal("recreated capacity was adopted")
	}
}

func TestSharedAliasesAndProjectScope(t *testing.T) {
	for _, alias := range []string{"serial", "uuid"} {
		t.Run(alias, func(t *testing.T) {
			handler, device := fixture(t)
			project, _ := handler.Resolve(context.Background(), "provider")
			duplicate := device.DeepCopyObject().(*inventory.Device)
			duplicate.Name, duplicate.UID, duplicate.ResourceVersion = "alias", "alias-uid", ""
			if alias == "serial" {
				duplicate.Spec.Identity.SystemUUID = "22222222-2222-2222-2222-222222222222"
			} else {
				duplicate.Spec.Identity.Serial = "other"
			}
			if err := project.Client.Create(context.Background(), duplicate); err != nil {
				t.Fatal(err)
			}
			if handler.Handle(context.Background(), registrationRequest(t, device, "record")).Allowed {
				t.Fatal("ambiguous alias accepted")
			}
		})
	}
}

func TestDeviceDeleteRaceRetainsFence(t *testing.T) {
	handler, device := fixture(t)
	request := registrationRequest(t, device, "record")
	deletion := request
	deletion.Resource = metav1.GroupVersionResource{Group: "fleet.datumapis.com", Version: "v1alpha1", Resource: "devices"}
	deletion.Operation = admissionv1.Delete
	deletion.OldObject.Raw, _ = json.Marshal(device)
	if !handler.Handle(context.Background(), deletion).Allowed {
		t.Fatal("unbound deletion denied")
	}
	if handler.Handle(context.Background(), request).Allowed {
		t.Fatal("registration raced a pending Device deletion")
	}
	other, selected := fixture(t)
	if !other.Handle(context.Background(), registrationRequest(t, selected, "record")).Allowed {
		t.Fatal("registration denied")
	}
	if other.Handle(context.Background(), deletion).Allowed {
		t.Fatal("bound Device deletion allowed")
	}
}

func TestProjectLifetimeRecheck(t *testing.T) {
	handler, device := fixture(t)
	resolve := handler.Resolve
	var calls int
	handler.Resolve = func(ctx context.Context, name string) (Project, error) {
		project, err := resolve(ctx, name)
		calls++
		if calls > 1 {
			project.Identity.UID = "recreated"
		}
		return project, err
	}
	if handler.Handle(context.Background(), registrationRequest(t, device, "record")).Allowed {
		t.Fatal("project recreation accepted")
	}
}

func TestNamespaceRecreationAndPermissionRevocationDuringAdmission(t *testing.T) {
	for _, mode := range []string{"namespace", "permission"} {
		t.Run(mode, func(t *testing.T) {
			handler, device := fixture(t)
			var calls int
			handler.Authorize = func(ctx context.Context, project Project, _ authenticationv1.UserInfo, _, _, _ string) error {
				calls++
				if calls == 3 {
					if mode == "permission" {
						return errors.New("revoked")
					}
					var namespace corev1.Namespace
					if err := project.Client.Get(ctx, client.ObjectKey{Name: "default"}, &namespace); err != nil {
						return err
					}
					if err := project.Client.Delete(ctx, &namespace); err != nil {
						return err
					}
					return project.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "recreated"}})
				}
				return nil
			}
			response := handler.Handle(context.Background(), registrationRequest(t, device, "record"))
			if mode == "permission" && response.Allowed {
				t.Fatal("revocation accepted")
			}
			if mode == "namespace" && response.Allowed {
				t.Fatal("namespace recreation admitted")
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
			second := fake.NewClientBuilder().WithScheme(first.Client.Scheme()).WithObjects(other, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-2"}}).Build()
			secret, ledger, _ := handler.Store.Read(context.Background())
			ledger.Projects = []string{"provider", "second"}
			if err := handler.Store.Write(context.Background(), secret, ledger); err != nil {
				t.Fatal(err)
			}
			handler.Store.Projects = ledger.Projects
			handler.Resolve = func(_ context.Context, name string) (Project, error) {
				if name == "provider" {
					return first, nil
				}
				return Project{Identity: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "second", UID: "project-2"}, Client: second}, nil
			}
			firstRequest := registrationRequest(t, device, "one")
			secondRequest := registrationRequest(t, other, "two")
			secondRequest.UserInfo.Extra[iam.ParentNameExtraKey] = authenticationv1.ExtraValue{"second"}
			var workers sync.WaitGroup
			for _, request := range []admission.Request{firstRequest, secondRequest} {
				workers.Add(1)
				go func() {
					defer workers.Done()
					if handler.Handle(context.Background(), request).Allowed {
						t.Error("cross-project alias ambiguity accepted")
					}
				}()
			}
			workers.Wait()
		})
	}
}

type timeoutAfterWrite struct{ client.Client }

func (api timeoutAfterWrite) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	if err := api.Client.Update(ctx, object, options...); err != nil {
		return err
	}
	return errors.New("timeout after commit")
}

func TestLedgerWriteTimeoutRetainsReservationAcrossRestart(t *testing.T) {
	handler, device := fixture(t)
	private := handler.Store.Client
	handler.Store.Client = timeoutAfterWrite{private}
	if handler.Handle(context.Background(), registrationRequest(t, device, "record")).Allowed {
		t.Fatal("timeout became success")
	}
	handler.Store.Client = private
	restarted := *handler
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarted.Handle(context.Background(), registrationRequest(t, device, "new-name")).Allowed {
		t.Fatal("unknown reservation was reused")
	}
	_, ledger, _ := restarted.Store.Read(context.Background())
	if len(ledger.Entries) != 1 || ledger.Entries[0].Outcome != "Unknown" {
		t.Fatal("unknown reservation lost")
	}
}

func TestDeviceCreatedThroughAdmissionCanRegister(t *testing.T) {
	handler, device := fixture(t)
	request := registrationRequest(t, device, "record")
	creation := request
	creation.Resource = metav1.GroupVersionResource{Group: "fleet.datumapis.com", Resource: "devices", Version: "v1alpha1"}
	creation.Object.Raw, _ = json.Marshal(device)
	if !handler.Handle(context.Background(), creation).Allowed {
		t.Fatal("Device create admission denied")
	}
	if !handler.Handle(context.Background(), request).Allowed {
		t.Fatal("own Device creation fence prevented registration")
	}
}

func TestRootProjectDeletionAndDeviceNameReuseAreFenced(t *testing.T) {
	handler, device := fixture(t)
	request := registrationRequest(t, device, "record")
	if !handler.Handle(context.Background(), request).Allowed {
		t.Fatal("registration denied")
	}
	deletion := request
	deletion.Name = "provider"
	deletion.Operation = admissionv1.Delete
	deletion.Resource = metav1.GroupVersionResource{Group: "resourcemanager.miloapis.com", Version: "v1alpha1", Resource: "projects"}
	deletion.UserInfo.Extra = nil
	if handler.Handle(context.Background(), deletion).Allowed {
		t.Fatal("root Project deletion bypass")
	}
	replacement := device.DeepCopyObject().(*inventory.Device)
	replacement.UID = "recreated"
	replacement.Spec.Identity.Serial = "new-serial"
	replacement.Spec.Identity.SystemUUID = "22222222-2222-2222-2222-222222222222"
	creation := registrationRequest(t, device, "record")
	creation.Resource = metav1.GroupVersionResource{Group: "fleet.datumapis.com", Version: "v1alpha1", Resource: "devices"}
	creation.Object.Raw, _ = json.Marshal(replacement)
	if handler.Handle(context.Background(), creation).Allowed {
		t.Fatal("registered Device name was reused by a new incarnation")
	}
}

func TestNonConflictingLegacyCapacityAndAllocationRemainUntouched(t *testing.T) {
	handler, device := fixture(t)
	ctx := context.Background()
	project, _ := handler.Resolve(ctx, "provider")
	legacyDevice := device.DeepCopyObject().(*inventory.Device)
	legacyDevice.Name, legacyDevice.UID, legacyDevice.ResourceVersion = "legacy-device", "legacy-device-uid", ""
	legacyDevice.Spec.Identity.Serial, legacyDevice.Spec.Identity.SystemUUID = "legacy-serial", "22222222-2222-2222-2222-222222222222"
	legacy := &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default", UID: "legacy-uid"}, Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: legacyDevice.Name}, AllocationPolicy: "Enabled", ReclaimPolicy: "Retain", CleaningPolicyRef: metal.LocalReference{Name: "synthetic-tenant-reuse-v1"}}, Status: metal.MachineCapacityStatus{Conditions: []metav1.Condition{{Type: "Available", Status: metav1.ConditionFalse, Reason: "Occupied"}}}}
	allocation := &metal.MachineAllocation{ObjectMeta: metav1.ObjectMeta{Name: "occupied", Namespace: "default", UID: "allocation-uid"}}
	for _, object := range []client.Object{legacyDevice, legacy, allocation} {
		if err := project.Client.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	beforeCapacity, _ := json.Marshal(legacy)
	beforeAllocation, _ := json.Marshal(allocation)
	if !handler.Handle(ctx, registrationRequest(t, device, "record")).Allowed {
		t.Fatal("non-conflicting legacy capacity blocked registration")
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(legacy), legacy); err != nil {
		t.Fatal(err)
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(allocation), allocation); err != nil {
		t.Fatal(err)
	}
	afterCapacity, _ := json.Marshal(legacy)
	afterAllocation, _ := json.Marshal(allocation)
	if string(beforeCapacity) != string(afterCapacity) || string(beforeAllocation) != string(afterAllocation) {
		t.Fatal("registration modified legacy state")
	}
	if handler.Handle(ctx, registrationRequest(t, legacyDevice, "conflicting")).Allowed {
		t.Fatal("occupied legacy asset was registered")
	}
}

func TestRecoveryReadFailureBecomesUnknownWithoutReleasing(t *testing.T) {
	handler, device := fixture(t)
	ctx := context.Background()
	request := registrationRequest(t, device, "record")
	if !handler.Handle(ctx, request).Allowed {
		t.Fatal("registration denied")
	}
	var capacity metal.MachineCapacity
	if err := json.Unmarshal(request.Object.Raw, &capacity); err != nil {
		t.Fatal(err)
	}
	project, _ := handler.Resolve(ctx, "provider")
	if err := project.Client.Create(ctx, &capacity); err != nil {
		t.Fatal(err)
	}
	if err := handler.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	_, before, _ := handler.Store.Read(ctx)
	if before.Entries[0].Outcome != "Recorded" {
		t.Fatal("exact completion not recovered")
	}
	handler.Resolve = func(context.Context, string) (Project, error) {
		return Project{}, errors.New("read authority unavailable")
	}
	if err := handler.Recover(ctx); err == nil {
		t.Fatal("read failure became success")
	}
	_, after, _ := handler.Store.Read(ctx)
	if len(after.Entries) != 1 || after.Entries[0].Outcome != "Unknown" || after.Entries[0].CapacityUID != before.Entries[0].CapacityUID {
		t.Fatal("uncertain recovery lost its reservation or retained a positive observation")
	}
}

func TestPendingDeviceUpdateCannotRaceSelectedVersion(t *testing.T) {
	handler, device := fixture(t)
	ctx := context.Background()
	registration := registrationRequest(t, device, "record")
	update := registration
	update.Resource = metav1.GroupVersionResource{Group: "fleet.datumapis.com", Version: "v1alpha1", Resource: "devices"}
	update.Operation = admissionv1.Update
	update.OldObject.Raw, _ = json.Marshal(device)
	device.Labels = map[string]string{"display": "changed"}
	update.Object.Raw, _ = json.Marshal(device)
	if !handler.Handle(ctx, update).Allowed {
		t.Fatal("unbound Device update rejected")
	}
	if handler.Handle(ctx, registration).Allowed {
		t.Fatal("registration raced an admitted but uncompleted Device update")
	}
	project, _ := handler.Resolve(ctx, "provider")
	if err := project.Client.Update(ctx, device); err != nil {
		t.Fatal(err)
	}
	if handler.Handle(ctx, registration).Allowed {
		t.Fatal("stale UI selection silently refreshed")
	}
	if !handler.Handle(ctx, registrationRequest(t, device, "explicit-new-selection")).Allowed {
		t.Fatal("explicit new persisted baseline rejected")
	}
}
