package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/sealed"
	"go.datum.net/fleet/internal/site"
)

const operationName = "alloc-example-install-1"

func courier(t *testing.T, objects ...client.Object) *Courier {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := ops.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &Courier{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), Namespace: "default"}
}

func sealedFor(t *testing.T, operation string) *ops.SealedPayload {
	t.Helper()
	payload, err := sealed.Seal(sealed.Payload{SSHAuthorizedKeys: "ssh-ed25519 AAAAExampleOnly"}, operation, "alloc-example", "default")
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assignmentNaming(payload *ops.SealedPayload) site.Assignment {
	inputs := json.RawMessage(fmt.Sprintf(`{"bootstrap":{"ref":%q,"digest":%q}}`, payload.Name, payload.Spec.Digest))
	return site.Assignment{Operation: ops.OperationReference{Kind: "DeviceInstallation", Name: operationName}, Inputs: inputs}
}

// TestTheCourierDeliversOnlyTheAdmittedPayload reads the payload the
// admitted inputs name, checks it again, and refuses a missing, altered or
// foreign one rather than deliver it.
func TestTheCourierDeliversOnlyTheAdmittedPayload(t *testing.T) {
	ctx := context.Background()
	good := sealedFor(t, operationName)
	if payload, err := courier(t, good).payload(ctx, assignmentNaming(good)); err != nil || payload == nil || payload.Digest != good.Spec.Digest {
		t.Fatalf("payload = %+v, %v", payload, err)
	}
	if payload, err := courier(t).payload(ctx, site.Assignment{Inputs: json.RawMessage(`{}`)}); err != nil || payload != nil {
		t.Fatalf("no bootstrap: %+v, %v", payload, err)
	}
	altered := sealedFor(t, operationName)
	reference := assignmentNaming(altered)
	altered.Spec.Data = []byte(`{"userData":"other"}`)
	foreign := sealedFor(t, "alloc-other-install-1")
	for name, test := range map[string]struct {
		courier    *Courier
		assignment site.Assignment
	}{
		"missing":             {courier(t), assignmentNaming(good)},
		"altered":             {courier(t, altered), reference},
		"another operation's": {courier(t, foreign), assignmentNaming(foreign)},
	} {
		if payload, err := test.courier.payload(ctx, test.assignment); err == nil || payload != nil {
			t.Errorf("%s payload delivered: %+v", name, payload)
		}
	}
}

type siteWith struct {
	Site
	carries bool
}

func (s siteWith) CarriesPayloads() bool { return s.carries }

func TestADeviceCarriesPayloadsOnlyIfEverySiteServingItDoes(t *testing.T) {
	device := &inventory.Device{ObjectMeta: metav1.ObjectMeta{Name: "server-example-001"}}
	for name, test := range map[string]struct {
		sites map[string]Site
		want  bool
	}{
		"every site carries":      {map[string]Site{"a": siteWith{carries: true}, "b": siteWith{carries: true}}, true},
		"one site does not":       {map[string]Site{"a": siteWith{carries: true}, "b": siteWith{carries: false}}, false},
		"a site that cannot tell": {map[string]Site{"a": siteWith{carries: true}, "b": nil}, false},
		"no site serves it":       {map[string]Site{}, false},
	} {
		sites := &Sites{Sites: test.sites, Devices: map[string][]string{device.Name: {"a", "b"}}}
		if got := sites.CarriesPayloads(device); got != test.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	var _ custody.PayloadCarriers = (*Sites)(nil)
}

type sweeping struct {
	siteWith
	kept *[][]site.Assignment
}

func (s sweeping) SweepPayloads(_ context.Context, keep []site.Assignment) error {
	*s.kept = append(*s.kept, keep)
	return nil
}

// TestPayloadsAreSweptOnlyAfterACompletePass never tells a site to drop
// payloads after a pass that could not reconcile or deliver every Device,
// because it would drop one an active operation needs.
func TestPayloadsAreSweptOnlyAfterACompletePass(t *testing.T) {
	var kept [][]site.Assignment
	delivered := []site.Assignment{{Operation: ops.OperationReference{Kind: "DeviceInstallation", Name: "op-active"}}}
	c := &Courier{Sites: &Sites{Sites: map[string]Site{"a": sweeping{kept: &kept}}}, delivered: delivered}
	if err := c.SweepPayloads(context.Background()); err != nil || len(kept) != 0 {
		t.Fatalf("swept after an incomplete pass: %v, %v", kept, err)
	}
	c.complete = true
	if err := c.SweepPayloads(context.Background()); err != nil || len(kept) != 1 || len(kept[0]) != 1 || kept[0][0].Operation.Name != "op-active" {
		t.Fatalf("kept = %v, %v", kept, err)
	}
}

// TestOnlyTheAdmittedOperationIsDelivered never builds an assignment from
// an operation that does not carry the fence custody admitted it at, so a
// same-name operation created later is never delivered as the admitted one.
func TestOnlyTheAdmittedOperationIsDelivered(t *testing.T) {
	state := &ops.DeviceCustody{ObjectMeta: metav1.ObjectMeta{Name: "server-example-001", Namespace: "default"}, Spec: ops.DeviceCustodySpec{
		ActiveOperation: &ops.ActiveOperation{OperationReference: ops.OperationReference{Kind: "DeviceWipe", Name: "wipe-example"}, Fence: 2}}}
	for _, tt := range []struct {
		name      string
		admission *ops.OperationAdmission
		delivered bool
	}{
		{"recreated, with no admission record", nil, false},
		{"admitted at another fence", &ops.OperationAdmission{Fence: 1}, false},
		{"admitted at the active fence", &ops.OperationAdmission{Fence: 2}, true},
	} {
		operation := &ops.DeviceWipe{ObjectMeta: metav1.ObjectMeta{Name: "wipe-example", Namespace: "default"}, Status: ops.OperationStatus{Admission: tt.admission}}
		assignment, err := Assignment(state, operation)
		if delivered := err == nil; delivered != tt.delivered {
			t.Fatalf("%s: delivered = %v, %v", tt.name, delivered, err)
		}
		if tt.delivered && assignment.Fence != 2 {
			t.Fatalf("%s: fence = %d", tt.name, assignment.Fence)
		}
	}
}
