package inventoryobserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	inventoryv1 "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	"go.datum.net/fleet/internal/netbox"
)

const namespace = "fleet-system"

type stubSource struct {
	mu         sync.Mutex
	devices    map[int64]*netbox.Device
	interfaces map[int64][]netbox.Interface
	deviceErr  error
	ifaceErr   error
	calls      int
}

func (s *stubSource) Device(_ context.Context, id int64) (*netbox.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.deviceErr != nil {
		return nil, s.deviceErr
	}
	device, ok := s.devices[id]
	if !ok {
		return nil, netbox.ErrNotFound
	}
	copied := *device
	return &copied, nil
}

func (s *stubSource) Interfaces(_ context.Context, id int64) ([]netbox.Interface, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ifaceErr != nil {
		return nil, s.ifaceErr
	}
	return s.interfaces[id], nil
}

func ptr[T any](v T) *T { return &v }

func syntheticSource() *stubSource {
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	device := &netbox.Device{ID: 1234, Name: "server-01.rack-01.example", Serial: "SYNTHETIC-0001", Position: ptr(12.0), LastUpdated: &updated}
	device.DeviceType.Model = "Example 1U"
	device.DeviceType.Manufacturer = netbox.Ref{Name: "Example Vendor"}
	device.Role = &netbox.Ref{Name: "Compute"}
	device.Site = &netbox.Ref{Name: "Example Site", Slug: "example-site"}
	device.Rack = &netbox.Ref{Name: "rack-01.example"}
	device.Face = &netbox.Ref{Value: "front"}
	device.Status = &netbox.Ref{Value: "active"}
	device.PrimaryIP4 = &netbox.IPRef{Address: "192.0.2.10/28"}
	var iface netbox.Interface
	_ = json.Unmarshal([]byte(`{"name":"eth1","type":{"value":"25gbase-x-sfp28"},"speed":25000000,"enabled":true,"lag":{"name":"bond0"},
	  "mac_address":"00:00:5E:00:53:01","mac_addresses":[{"mac_address":"00:00:5e:00:53:01"},{"mac_address":"00:00:5e:00:53:02"},{"mac_address":"not-a-mac"}],
	  "link_peers_type":"dcim.interface","link_peers":[{"name":"Ethernet1/1","device":{"name":"switch-02.example"}},{"name":"Ethernet1/1","device":{"name":"switch-01.example"}}]}`), &iface)
	mgmt := netbox.Interface{Name: "bmc0", Enabled: true}
	return &stubSource{devices: map[int64]*netbox.Device{1234: device}, interfaces: map[int64][]netbox.Interface{1234: {iface, mgmt}}}
}

func observation(name string, id int64, expectedName string) *inventoryv1.InventoryObservation {
	return &inventoryv1.InventoryObservation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec:       inventoryv1.InventoryObservationSpec{Source: inventoryv1.ObservationSource{System: inventoryv1.SourceSystemNetBox, Kind: inventoryv1.SourceKindDevice, ID: id, ExpectedName: expectedName}},
	}
}

type recorder struct {
	mu     sync.Mutex
	writes []string
}

func (r *recorder) add(op string, obj client.Object) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, op+" "+obj.GetName())
}

func newClient(t *testing.T, rec *recorder, funcs interceptor.Funcs, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := inventoryv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&inventoryv1.InventoryObservation{}).WithObjects(objects...).Build()
	record := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			rec.add("create", obj)
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			rec.add("update", obj)
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			rec.add("patch", obj)
			return c.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			rec.add("delete", obj)
			return c.Delete(ctx, obj, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			rec.add(sub+"-update", obj)
			if funcs.SubResourceUpdate != nil {
				return funcs.SubResourceUpdate(ctx, c, sub, obj, opts...)
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	}
	return interceptor.NewClient(base, record)
}

func get(t *testing.T, c client.Client, name string) *inventoryv1.InventoryObservation {
	t.Helper()
	var out inventoryv1.InventoryObservation
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func observed(t *testing.T, obs *inventoryv1.InventoryObservation) *metav1.Condition {
	t.Helper()
	condition := apimeta.FindStatusCondition(obs.Status.Conditions, inventoryv1.ConditionObserved)
	if condition == nil {
		t.Fatal("Observed condition missing")
	}
	return condition
}

func clock(at time.Time) func() time.Time { return func() time.Time { return at } }

func TestRefreshRecordsObservedOnlyFactsAndWritesOnlyStatus(t *testing.T) {
	rec := &recorder{}
	c := newClient(t, rec, interceptor.Funcs{}, observation("server-01", 1234, "server-01.rack-01.example"))
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	results, err := (&Observer{Client: c, Namespace: namespace, NetBox: syntheticSource(), Now: clock(now)}).RefreshAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Succeeded() {
		t.Fatalf("results = %+v", results)
	}
	if strings.Join(rec.writes, ",") != "status-update server-01" {
		t.Fatalf("observer writes = %v, want only one status update", rec.writes)
	}
	obs := get(t, c, "server-01")
	if condition := observed(t, obs); condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != 1 {
		t.Fatalf("condition = %+v", condition)
	}
	if obs.Status.TrustProfile != inventoryv1.TrustProfileObservedOnly || !obs.Status.ObservedAt.Time.Equal(now) || obs.Status.SourceUpdatedAt == nil || !strings.HasPrefix(obs.Status.FactsDigest, "sha256:") {
		t.Fatalf("status = %+v", obs.Status)
	}
	facts := obs.Status.Facts
	if facts.Serial != "SYNTHETIC-0001" || facts.Placement.Site != "example-site" || facts.Placement.Position != "12" || facts.Manufacturer != "Example Vendor" {
		t.Fatalf("facts = %+v", facts)
	}
	if len(facts.Interfaces) != 2 || facts.Interfaces[0].Name != "bmc0" {
		t.Fatalf("interfaces not sorted: %+v", facts.Interfaces)
	}
	eth := facts.Interfaces[1]
	if eth.SpeedKbps != 25000000 || eth.LAG != "bond0" || strings.Join(eth.MACAddresses, ",") != "00:00:5e:00:53:01,00:00:5e:00:53:02" {
		t.Fatalf("interface = %+v", eth)
	}
	if len(eth.Peers) != 2 || eth.Peers[0].Device != "switch-01.example" || eth.Peers[0].Kind != "dcim.interface" {
		t.Fatalf("peers = %+v", eth.Peers)
	}
	if len(facts.Addresses) != 1 || facts.Addresses[0].Role != "PrimaryIPv4" {
		t.Fatalf("addresses = %+v", facts.Addresses)
	}
	if obs.Spec.Source.ID != 1234 || obs.Spec.Source.ExpectedName != "server-01.rack-01.example" {
		t.Fatalf("spec changed: %+v", obs.Spec)
	}
}

func TestRefreshIsDeterministic(t *testing.T) {
	c := newClient(t, &recorder{}, interceptor.Funcs{}, observation("server-01", 1234, ""))
	observer := &Observer{Client: c, Namespace: namespace, NetBox: syntheticSource(), Now: clock(time.Unix(100, 0))}
	if _, err := observer.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := get(t, c, "server-01").Status.FactsDigest
	observer.Now = clock(time.Unix(200, 0))
	if _, err := observer.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	obs := get(t, c, "server-01")
	if obs.Status.FactsDigest != first || !obs.Status.ObservedAt.Time.Equal(time.Unix(200, 0)) {
		t.Fatalf("digest %s -> %s, observedAt %v", first, obs.Status.FactsDigest, obs.Status.ObservedAt)
	}
}

func TestSourceFailuresRetainPreviousFactsAndNeverReportObserved(t *testing.T) {
	for name, tc := range map[string]struct {
		deviceErr, ifaceErr error
		reason              string
	}{
		"unavailable":       {deviceErr: netbox.ErrUnavailable, reason: inventoryv1.ReasonSourceUnavailable},
		"unauthorized":      {deviceErr: netbox.ErrUnauthorized, reason: inventoryv1.ReasonSourceUnauthorized},
		"missing":           {deviceErr: netbox.ErrNotFound, reason: inventoryv1.ReasonSourceObjectMissing},
		"interfaces failed": {ifaceErr: errors.New("synthetic timeout"), reason: inventoryv1.ReasonSourceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, &recorder{}, interceptor.Funcs{}, observation("server-01", 1234, ""))
			source := syntheticSource()
			observer := &Observer{Client: c, Namespace: namespace, NetBox: source, Now: clock(time.Unix(100, 0))}
			if _, err := observer.RefreshAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := get(t, c, "server-01").Status
			source.deviceErr, source.ifaceErr = tc.deviceErr, tc.ifaceErr
			observer.Now = clock(time.Unix(200, 0))
			results, err := observer.RefreshAll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if results[0].Succeeded() || results[0].Reason != tc.reason {
				t.Fatalf("result = %+v", results[0])
			}
			after := get(t, c, "server-01")
			if condition := observed(t, after); condition.Status != metav1.ConditionFalse || condition.Reason != tc.reason {
				t.Fatalf("condition = %+v", condition)
			}
			if after.Status.FactsDigest != before.FactsDigest || !after.Status.ObservedAt.Equal(before.ObservedAt) || !after.Status.LastAttemptTime.Time.Equal(time.Unix(200, 0)) {
				t.Fatalf("failure changed retained facts or attempt time: %+v", after.Status)
			}
		})
	}
}

func TestExpectedNameMismatchDoesNotRecordFacts(t *testing.T) {
	c := newClient(t, &recorder{}, interceptor.Funcs{}, observation("server-01", 1234, "server-02.rack-01.example"))
	results, err := (&Observer{Client: c, Namespace: namespace, NetBox: syntheticSource()}).RefreshAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Reason != inventoryv1.ReasonIdentityMismatch {
		t.Fatalf("result = %+v", results[0])
	}
	if obs := get(t, c, "server-01"); obs.Status.Facts != nil || obs.Status.ObservedAt != nil {
		t.Fatalf("mismatched object recorded facts: %+v", obs.Status)
	}
}

func TestSerialChangeIsIdentityMismatchNotAnUpdate(t *testing.T) {
	c := newClient(t, &recorder{}, interceptor.Funcs{}, observation("server-01", 1234, ""))
	source := syntheticSource()
	observer := &Observer{Client: c, Namespace: namespace, NetBox: source}
	if _, err := observer.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.devices[1234].Serial = "SYNTHETIC-0002"
	results, err := observer.RefreshAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Reason != inventoryv1.ReasonIdentityMismatch {
		t.Fatalf("result = %+v", results[0])
	}
	if got := get(t, c, "server-01").Status.Facts.Serial; got != "SYNTHETIC-0001" {
		t.Fatalf("serial replaced with %q", got)
	}
}

func TestConflictingStatusWriteDoesNotOverwriteNewerState(t *testing.T) {
	conflict := interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		return apierrors.NewConflict(schema.GroupResource{Group: inventoryv1.GroupVersion.Group, Resource: "inventoryobservations"}, obj.GetName(), errors.New("synthetic concurrent write"))
	}}
	c := newClient(t, &recorder{}, conflict, observation("server-01", 1234, ""))
	results, err := (&Observer{Client: c, Namespace: namespace, NetBox: syntheticSource()}).RefreshAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Succeeded() || !apierrors.IsConflict(results[0].WriteErr) {
		t.Fatalf("result = %+v", results[0])
	}
	if obs := get(t, c, "server-01"); obs.Status.Facts != nil {
		t.Fatal("conflicting write was persisted")
	}
}

func TestStaleResourceVersionIsRejected(t *testing.T) {
	c := newClient(t, &recorder{}, interceptor.Funcs{}, observation("server-01", 1234, ""))
	stale := get(t, c, "server-01")
	fresh := stale.DeepCopy()
	fresh.Labels = map[string]string{"example": "concurrent"}
	if err := c.Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	result := (&Observer{Client: c, Namespace: namespace, NetBox: syntheticSource()}).refresh(context.Background(), stale)
	if !apierrors.IsConflict(result.WriteErr) {
		t.Fatalf("stale write err = %v, want conflict", result.WriteErr)
	}
}

func TestOneFailureDoesNotBlockOtherObservations(t *testing.T) {
	c := newClient(t, &recorder{}, interceptor.Funcs{}, observation("a-missing", 999, ""), observation("b-present", 1234, ""))
	results, err := (&Observer{Client: c, Namespace: namespace, NetBox: syntheticSource()}).RefreshAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Reason != inventoryv1.ReasonSourceObjectMissing || !results[1].Succeeded() {
		t.Fatalf("results = %+v", results)
	}
}

func TestOtherNamespacesAreIgnored(t *testing.T) {
	other := observation("elsewhere", 1234, "")
	other.Namespace = "other"
	rec := &recorder{}
	c := newClient(t, rec, interceptor.Funcs{}, other)
	source := syntheticSource()
	results, err := (&Observer{Client: c, Namespace: namespace, NetBox: source}).RefreshAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 || len(rec.writes) != 0 || source.calls != 0 {
		t.Fatalf("results=%v writes=%v calls=%d", results, rec.writes, source.calls)
	}
}

func TestUnsupportedSourceDoesNotReadNetBox(t *testing.T) {
	obs := observation("server-01", 1234, "")
	obs.Spec.Source.Kind = "Rack"
	source := syntheticSource()
	c := newClient(t, &recorder{}, interceptor.Funcs{}, obs)
	results, err := (&Observer{Client: c, Namespace: namespace, NetBox: source}).RefreshAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Reason != inventoryv1.ReasonUnsupportedSource || source.calls != 0 {
		t.Fatalf("result=%+v calls=%d", results[0], source.calls)
	}
}

func TestFactsNeverCarryFreeFormSourceData(t *testing.T) {
	var device netbox.Device
	raw := `{"id":1,"name":"server-01","serial":"SYNTHETIC-0001","custom_fields":{"oob_password":"synthetic-sentinel"},"comments":"synthetic-sentinel","config_context":{"k":"synthetic-sentinel"},"local_context_data":{"k":"synthetic-sentinel"}}`
	if err := json.Unmarshal([]byte(raw), &device); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(Normalize(&device, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-sentinel") {
		t.Fatalf("free-form source data leaked into facts: %s", encoded)
	}
}
