package simwork

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
)

const (
	device   = "fleet-provider/server-example-001"
	endpoint = "https://[2001:db8::1]"
	uuid     = "00000000-0000-4000-8000-0000000000a1"
	key      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func powerRunner(t *testing.T) *site.Runner {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := site.OpenStore(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &site.Runner{Site: "site-example-a", Store: store, Results: site.NewResults(store), Poll: 10 * time.Millisecond,
		Access: fixture.Access{Enrollment: fixture.Enrollment{device: {BMCEndpoint: endpoint, SystemUUID: uuid, BMCPublicKeySHA256: key, Disks: []string{"disk-0"}}}},
		Kinds:  map[string]site.Kind{"DevicePowerOperation": Power{}}}
}

func powerAssignment(uid string, fence int64, action ops.PowerAction) site.Assignment {
	inputs, _ := json.Marshal(ops.DevicePowerOperationSpec{Action: action, MachineGeneration: 2,
		OperationEnvelope: ops.OperationEnvelope{Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: "alloc-example"}}}})
	sum := sha256.Sum256(inputs)
	return site.Assignment{Device: device, Operation: ops.OperationReference{Kind: "DevicePowerOperation", Name: "alloc-example-power-" + uid}, Fence: fence,
		AdmittedAt: time.Now(), Deadline: time.Now().Add(time.Minute), InputDigest: "sha256:" + hex.EncodeToString(sum[:]), Inputs: inputs,
		Snapshot: ops.DeviceSnapshot{BMCEndpoint: endpoint, SystemUUID: uuid, BMCPublicKeySHA256: key}}
}

// TestARestartIsProvenOnlyByItsResetTime requires a simulated restart to move
// the server's reset time: a server that ignores the reset stays on, which
// proves nothing.
func TestARestartIsProvenOnlyByItsResetTime(t *testing.T) {
	for name, unresponsive := range map[string]bool{"the server restarts": false, "the server ignores the reset": true} {
		t.Run(name, func(t *testing.T) {
			r := powerRunner(t)
			if err := WriteServer(r.Store, device, Server{Power: "On", Unresponsive: unresponsive}); err != nil {
				t.Fatal(err)
			}
			result, err := r.Run(context.Background(), powerAssignment("op-restart", 1, ops.PowerForceRestart))
			if err != nil {
				t.Fatal(err)
			}
			want := ops.PhaseSucceeded
			if unresponsive {
				want = ops.PhaseOutcomeUnknown
			}
			if result.Phase != want {
				t.Fatalf("result = %+v, want %s", result, want)
			}
			if result.Phase == ops.PhaseSucceeded {
				spec := ops.DevicePowerOperationSpec{Action: ops.PowerForceRestart}
				if _, err := custody.DevicePowerOperationKind().Evidence(&ops.DevicePowerOperation{Spec: spec}, result.Evidence); err != nil {
					t.Fatalf("the coordinator would refuse the evidence %s: %v", result.Evidence, err)
				}
			}
		})
	}
}
