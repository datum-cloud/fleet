package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
)

func TestTeardownDigestBindsRevocationNotClusterMetadata(t *testing.T) {
	session := &ProjectedMachineManagementSession{ObjectMeta: metav1.ObjectMeta{Name: "opaque-session", Namespace: "hub", UID: "hub-uid", Generation: 8}, Spec: ProjectedMachineManagementSessionSpec{DesiredState: sessionv1.ProjectionActive, MachineCapacityRef: sessionv1.LocalReference{Name: "capacity"}}}
	if TeardownDigest(session) != "" {
		t.Fatal("active projection has teardown proof")
	}
	session.Spec.DesiredState = sessionv1.ProjectionRevoked
	digest := TeardownDigest(session)
	session.Namespace, session.UID, session.Generation = "cell", "cell-uid", 3
	if digest == "" || TeardownDigest(session) != digest {
		t.Fatal("API-local metadata changed proof identity")
	}
	session.Spec.MachineCapacityRef.Name = "other-capacity"
	if TeardownDigest(session) == digest {
		t.Fatal("proof survived a target change")
	}
	session.Spec.MachineCapacityRef.Name = "capacity"
	session.Name = "another-session"
	if TeardownDigest(session) == digest {
		t.Fatal("proof survived session replacement")
	}
}
