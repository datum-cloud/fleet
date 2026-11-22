package registration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateCounter records root updates so a test can tell a stale control
// rejected before any write from one that lost API-server CAS.
type updateCounter struct {
	client.Client
	updates int
}

func (counter *updateCounter) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	counter.updates++
	return counter.Client.Update(ctx, object, options...)
}

func TestStaleSignedControlIsRejectedByItsResourceVersion(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	private := controlFixture(t, handler)
	stale := controlFor(t, handler, private, "activate")
	secret, ledger, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// An ordinary write moves the root's resourceVersion but not its epoch, so
	// only the signed resourceVersion distinguishes the old signature.
	ledger.DeviceFences = append(ledger.DeviceFences, DeviceFence{Project: "provider", UID: "moved-root", Aliases: []string{"uuid:moved"}})
	if err := handler.Store.Write(ctx, secret, ledger); err != nil {
		t.Fatal(err)
	}
	moved, route, err := handler.Store.root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ResourceVersion == stale.Request.ResourceVersion || route.Epoch != stale.Request.Epoch {
		t.Fatalf("fixture did not isolate the resourceVersion: signed %s/%d, current %s/%d", stale.Request.ResourceVersion, stale.Request.Epoch, moved.ResourceVersion, route.Epoch)
	}
	counter := &updateCounter{Client: handler.Store.Client}
	handler.Store.Client = counter
	err = handler.ApplyControl(ctx, stale)
	if err == nil || !strings.Contains(err.Error(), "stale control epoch or CAS baseline") {
		t.Fatalf("stale signed control was not rejected by its signed resourceVersion: %v", err)
	}
	if counter.updates != 0 {
		t.Fatalf("stale signed control reached a root update (%d); only API CAS would have stopped it", counter.updates)
	}
	if handler.Ready(ctx) == nil {
		t.Fatal("stale signed activation made the root admitting")
	}
	unchanged, _, err := handler.Store.root(ctx)
	if err != nil || unchanged.ResourceVersion != moved.ResourceVersion {
		t.Fatalf("stale signed control changed the root: %v", err)
	}
	// The same request signed against the current root is accepted, so the
	// rejection above came from the stale baseline and nothing else.
	if err := handler.ApplyControl(ctx, controlFor(t, handler, private, "activate")); err != nil {
		t.Fatal(err)
	}
	if err := handler.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

// registeredDeviceWithFinalizer registers the fixture Device and then gives it
// a finalizer, so a later delete leaves it carrying a deletion timestamp.
func registeredDeviceWithFinalizer(t *testing.T, handler *Handler, device *inventory.Device) (Project, *inventory.Device) {
	t.Helper()
	ctx := context.Background()
	if response := handler.Handle(ctx, registrationRequest(t, device, "bound")); !response.Allowed {
		t.Fatalf("registration denied: %s", response.Result.Message)
	}
	project, _ := handler.Resolve(ctx, "provider")
	current := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(device), current); err != nil {
		t.Fatal(err)
	}
	current.Finalizers = []string{"synthetic.example/drain"}
	if err := project.Client.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	return project, current
}

func TestRegisteredDeviceFinalizerRemovalIsDenied(t *testing.T) {
	ctx := context.Background()
	handler, device := fixture(t)
	project, live := registeredDeviceWithFinalizer(t, handler, device)
	released := live.DeepCopyObject().(*inventory.Device)
	released.Finalizers = nil
	if handler.Handle(ctx, deviceRequest(t, admissionv1.Update, live, released)).Allowed {
		t.Fatal("finalizer removal on a registered Device was admitted")
	}
	if err := project.Client.Delete(ctx, live); err != nil {
		t.Fatal(err)
	}
	deleting := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(live), deleting); err != nil || deleting.DeletionTimestamp.IsZero() {
		t.Fatalf("Device is not deleting: %v", err)
	}
	_, before, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released = deleting.DeepCopyObject().(*inventory.Device)
	released.Finalizers = nil
	response := handler.Handle(ctx, deviceRequest(t, admissionv1.Update, deleting, released))
	if response.Allowed {
		t.Fatal("finalizer removal completed deletion of a registered Device")
	}
	_, after, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("denied finalizer removal changed the ledger: before %+v after %+v", before, after)
	}
	for _, fence := range after.DeviceFences {
		if fence.UID == device.UID && fence.Deleted {
			t.Fatal("a registered Device was tombstoned while its registration stays bound")
		}
	}
	if len(after.Entries) != 1 || after.Entries[0].DeviceUID != device.UID {
		t.Fatalf("registration binding changed: %+v", after.Entries)
	}
}

func TestFinalizerRemovalOnLiveDeviceRequiresLiveNamespace(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	project, _ := handler.Resolve(ctx, "provider")
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "draining", UID: "namespace-draining", Finalizers: []string{"synthetic.example/hold"}}}
	if err := project.Client.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "held", Namespace: namespace.Name, UID: "device-held", Finalizers: []string{"synthetic.example/drain"}}, Spec: inventory.DeviceSpec{Identity: inventory.DeviceIdentity{Serial: "SERIAL-held", SystemUUID: "55555555-5555-5555-5555-555555555555", Model: "test"}}}
	if err := project.Client.Create(ctx, device); err != nil {
		t.Fatal(err)
	}
	if response := handler.Handle(ctx, deviceRequest(t, admissionv1.Create, nil, device)); !response.Allowed {
		t.Fatalf("live namespace Device create denied: %s", response.Result.Message)
	}
	current := &inventory.Device{}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(device), current); err != nil {
		t.Fatal(err)
	}
	released := current.DeepCopyObject().(*inventory.Device)
	released.Finalizers = nil
	request := deviceRequest(t, admissionv1.Update, current, released)
	if response := handler.Handle(ctx, request); !response.Allowed {
		t.Fatalf("finalizer removal in a live namespace was denied: %s", response.Result.Message)
	}
	if err := project.Client.Delete(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(namespace), namespace); err != nil || namespace.DeletionTimestamp.IsZero() {
		t.Fatalf("namespace is not terminating: %v", err)
	}
	_, before, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if handler.Handle(ctx, request).Allowed {
		t.Fatal("finalizer removal on a non-deleting Device was admitted in a terminating namespace")
	}
	_, after, err := handler.Store.Read(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("denied finalizer removal changed the ledger: %v", err)
	}
}
