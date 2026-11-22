package registration

import (
	"context"
	"encoding/json"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// terminatingDevice creates a Device in a provider-project namespace other
// than default, records its fence while the namespace is live, and then
// deletes the namespace while a finalizer still holds it in Terminating.
func terminatingDevice(t *testing.T, handler *Handler, name, uuid string) (Project, *inventory.Device) {
	t.Helper()
	ctx := context.Background()
	project, _ := handler.Resolve(ctx, "provider")
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "decommissioned-" + name, UID: types.UID("namespace-" + name), Finalizers: []string{"synthetic.example/hold"}}}
	if err := project.Client.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace.Name, UID: types.UID("device-" + name)}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "SERIAL-" + name, SystemUUID: uuid, Model: "test"}}}
	if err := project.Client.Create(ctx, device); err != nil {
		t.Fatal(err)
	}
	if response := handler.Handle(ctx, deviceRequest(t, admissionv1.Create, nil, device)); !response.Allowed {
		t.Fatalf("live namespace Device create denied: %s", response.Result.Message)
	}
	if err := project.Client.Delete(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(namespace), namespace); err != nil || namespace.DeletionTimestamp.IsZero() {
		t.Fatalf("namespace is not terminating: %v", err)
	}
	return project, device
}

func deviceRequest(t *testing.T, operation admissionv1.Operation, old, current *inventory.Device) admission.Request {
	t.Helper()
	request := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{UID: types.UID("device-request-" + string(operation)), Operation: operation, Resource: metav1.GroupVersionResource{Group: "fleet.datumapis.com", Version: "v1alpha1", Resource: "devices"}, UserInfo: authenticationv1.UserInfo{Username: "operator", UID: "actor-1", Extra: map[string]authenticationv1.ExtraValue{iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {"provider"}}}}}
	for _, pair := range []struct {
		device *inventory.Device
		raw    *runtime.RawExtension
	}{{old, &request.OldObject}, {current, &request.Object}} {
		if pair.device == nil {
			continue
		}
		encoded, err := json.Marshal(pair.device)
		if err != nil {
			t.Fatal(err)
		}
		pair.raw.Raw = encoded
		request.Name, request.Namespace = pair.device.Name, pair.device.Namespace
	}
	return request
}

func retainedFence(t *testing.T, handler *Handler, uid types.UID) DeviceFence {
	t.Helper()
	_, ledger, err := handler.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found []DeviceFence
	for _, fence := range ledger.DeviceFences {
		if fence.UID == uid {
			found = append(found, fence)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one retained fence for %s: %+v", uid, ledger.DeviceFences)
	}
	return found[0]
}

func TestDeviceDeleteInTerminatingNamespaceRetainsTombstone(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	_, device := terminatingDevice(t, handler, "retired", "22222222-2222-2222-2222-222222222222")
	created := retainedFence(t, handler, device.UID)
	if created.NamespaceUID == "" || len(created.Aliases) == 0 {
		t.Fatalf("create did not record namespace lifetime and aliases: %+v", created)
	}
	if response := handler.Handle(ctx, deviceRequest(t, admissionv1.Delete, device, nil)); !response.Allowed {
		t.Fatalf("Device delete in a terminating namespace was denied, so the namespace cannot finish terminating: %s", response.Result.Message)
	}
	tombstone := retainedFence(t, handler, device.UID)
	if !tombstone.Deleted || tombstone.NamespaceUID != created.NamespaceUID || tombstone.Version != "" || !preservesReservations(Ledger{DeviceFences: []DeviceFence{created}}, Ledger{DeviceFences: []DeviceFence{tombstone}}) {
		t.Fatalf("tombstone lost retained lifetime: before %+v after %+v", created, tombstone)
	}
	recreated := device.DeepCopyObject().(*inventory.Device)
	recreated.Namespace, recreated.UID = "default", "recreated"
	if handler.Handle(ctx, deviceRequest(t, admissionv1.Create, nil, recreated)).Allowed {
		t.Fatal("deleted hardware aliases were released by the terminating namespace path")
	}
}

func TestDeviceDeleteAfterNamespaceRemovalUsesRecordedLifetime(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	project, device := terminatingDevice(t, handler, "removed", "33333333-3333-3333-3333-333333333333")
	created := retainedFence(t, handler, device.UID)
	namespace := &corev1.Namespace{}
	if err := project.Client.Get(ctx, client.ObjectKey{Name: device.Namespace}, namespace); err != nil {
		t.Fatal(err)
	}
	namespace.Finalizers = nil
	if err := project.Client.Update(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(namespace), namespace); err == nil {
		t.Fatal("namespace was not removed")
	}
	if response := handler.Handle(ctx, deviceRequest(t, admissionv1.Delete, device, nil)); !response.Allowed {
		t.Fatalf("Device delete after namespace removal was denied: %s", response.Result.Message)
	}
	tombstone := retainedFence(t, handler, device.UID)
	if !tombstone.Deleted || tombstone.NamespaceUID != created.NamespaceUID || !preservesReservations(Ledger{DeviceFences: []DeviceFence{created}}, Ledger{DeviceFences: []DeviceFence{tombstone}}) {
		t.Fatalf("tombstone did not keep the recorded namespace lifetime: before %+v after %+v", created, tombstone)
	}
}

func TestDeviceFinalizerRemovalInTerminatingNamespaceRetainsTombstone(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	project, device := terminatingDevice(t, handler, "finalized", "44444444-4444-4444-4444-444444444444")
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
		t.Fatal(err)
	}
	device.Finalizers = []string{"synthetic.example/drain"}
	if err := project.Client.Update(ctx, device); err != nil {
		t.Fatal(err)
	}
	if err := project.Client.Delete(ctx, device); err != nil {
		t.Fatal(err)
	}
	deleting := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(device), deleting); err != nil || deleting.DeletionTimestamp.IsZero() {
		t.Fatalf("Device is not deleting: %v", err)
	}
	if response := handler.Handle(ctx, deviceRequest(t, admissionv1.Delete, deleting, nil)); !response.Allowed {
		t.Fatalf("Device delete in a terminating namespace was denied: %s", response.Result.Message)
	}
	released := deleting.DeepCopyObject().(*inventory.Device)
	released.Finalizers = nil
	if response := handler.Handle(ctx, deviceRequest(t, admissionv1.Update, deleting, released)); !response.Allowed {
		t.Fatalf("finalizer removal in a terminating namespace was denied: %s", response.Result.Message)
	}
	tombstone := retainedFence(t, handler, device.UID)
	if !tombstone.Deleted || tombstone.NamespaceUID == "" || len(tombstone.Aliases) == 0 {
		t.Fatalf("finalizer removal lost the tombstone: %+v", tombstone)
	}
	edited := deleting.DeepCopyObject().(*inventory.Device)
	edited.Finalizers, edited.Labels = nil, map[string]string{"edited": "true"}
	if handler.Handle(ctx, deviceRequest(t, admissionv1.Update, deleting, edited)).Allowed {
		t.Fatal("a non-finalizer edit was admitted without a live namespace")
	}
}
