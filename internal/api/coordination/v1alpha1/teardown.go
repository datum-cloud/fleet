package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
)

func TeardownDigest(session *ProjectedMachineManagementSession) string {
	if session.Spec.DesiredState != sessionv1.ProjectionRevoked {
		return ""
	}
	data, err := json.Marshal(struct {
		Name string                                `json:"name"`
		Spec ProjectedMachineManagementSessionSpec `json:"spec"`
	}{Name: session.Name, Spec: session.Spec})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
