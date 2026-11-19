package simulator

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAdversarialReplayCannotRetargetAuthorityAfterRestart(t *testing.T) {
	for name, mutate := range map[string]func(*Request){
		"asset":   func(r *Request) { r.AssetID = "other-asset" },
		"binding": func(r *Request) { r.BindingID = "other-binding" },
		"epoch":   func(r *Request) { r.Epoch++ },
		"fence":   func(r *Request) { r.FenceToken = "other-fence" },
		"image":   func(r *Request) { r.Inputs = []byte(`{"image":"different-image"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			store := openStore(t, path)
			request := operationRequest("retained-install", "install", 1, `{"image":"synthetic-image"}`)
			if _, _, err := store.Submit(request); err != nil {
				t.Fatal(err)
			}
			store = openStore(t, path)
			before := store.Snapshot()
			mutate(&request)
			if _, _, err := store.Submit(request); !errors.Is(err, ErrConflict) {
				t.Fatalf("retargeted replay: %v", err)
			}
			if !reflect.DeepEqual(before, store.Snapshot()) {
				t.Fatal("rejected replay changed authority or effects")
			}
		})
	}
}
