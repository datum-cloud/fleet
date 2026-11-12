package gateway

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1alpha1 "go.datum.net/fleet/api/v1alpha1"
)

func TestBindingInputsCannotChangeAfterGatewayAcceptance(t *testing.T) {
	a := fleetv1alpha1.ProjectedMachineManagementSessionSpec{
		SessionUID: "session", ClaimUID: "claim", MachineUID: "machine",
		BindingID: "binding", BindingEpoch: 1, LocationUID: "location", LocationRevision: "r1",
		BareMetalMachineUID: "bmm", AccessProfileRevision: "profile", ClientPublicKey: "key",
		ConnectBefore: metav1.NewTime(time.Unix(100, 0)), ExpiresAt: metav1.NewTime(time.Unix(200, 0)),
		AuthorityFreshUntil: metav1.NewTime(time.Unix(110, 0)), AdapterRoute: "adapter", BMCSystemID: "physical",
	}
	b := a
	b.AuthorityFreshUntil = metav1.NewTime(time.Unix(120, 0))
	if !sameBinding(a, b) {
		t.Fatal("authority freshness was not refreshable")
	}
	b.AdapterRoute = "changed"
	if sameBinding(a, b) {
		t.Fatal("changed adapter route was accepted")
	}
	b = a
	b.BindingEpoch++
	if sameBinding(a, b) {
		t.Fatal("changed binding epoch was accepted")
	}
}
