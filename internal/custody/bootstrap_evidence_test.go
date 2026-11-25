package custody

import (
	"strings"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// TestInstallEvidenceMustProveTheAdmittedBootstrap: a success is accepted
// only if its evidence names exactly the admitted bootstrap digest, and
// names none when the operation has no bootstrap data.
func TestInstallEvidenceMustProveTheAdmittedBootstrap(t *testing.T) {
	evidence := DeviceInstallationKind().Evidence
	digest := "sha256:" + strings.Repeat("ab", 32)
	with := &ops.DeviceInstallation{Spec: ops.DeviceInstallationSpec{Bootstrap: &ops.SealedInput{Ref: "sealed-example", Digest: digest}}}
	without := &ops.DeviceInstallation{}
	for _, test := range []struct {
		name      string
		operation *ops.DeviceInstallation
		evidence  string
		ok        bool
	}{
		{"the admitted digest", with, `{"steps":[{"name":"disk","completedAt":"2026-11-25T00:00:00Z"}],"installerVerifiedBootstrap":"` + digest + `"}`, true},
		{"no bootstrap proof", with, `{"steps":[{"name":"disk","completedAt":"2026-11-25T00:00:00Z"}]}`, false},
		{"another digest", with, `{"installerVerifiedBootstrap":"sha256:` + strings.Repeat("cd", 32) + `"}`, false},
		{"none admitted, none handed", without, `{"steps":[{"name":"disk","completedAt":"2026-11-25T00:00:00Z"}]}`, true},
		{"none admitted, some handed", without, `{"installerVerifiedBootstrap":"` + digest + `"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := evidence(test.operation, []byte(test.evidence))
			if test.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
