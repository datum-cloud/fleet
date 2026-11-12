package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAdmissionIsDurableAndDeduplicated(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "op-1", MachineUID: "machine-1", Method: "POST", VirtualTarget: "/reset", AcceptedBody: json.RawMessage(`{"ResetType":"On"}`)}
	if _, fresh, err := s.Admit(op); err != nil || !fresh {
		t.Fatalf("admit fresh=%v err=%v", fresh, err)
	}
	if _, fresh, err := s.Admit(op); err != nil || fresh {
		t.Fatalf("duplicate fresh=%v err=%v", fresh, err)
	}
	info, err := os.Stat(filepath.Join(s.dir, "op-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}
