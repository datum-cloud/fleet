package executor

import (
	"context"
	"net/http"
	"testing"
)

func TestAdversarialLookupRejectsDotSegmentsBeforeTransport(t *testing.T) {
	for _, id := range []string{".", ".."} {
		t.Run(id, func(t *testing.T) {
			called := false
			client := clientForHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				t.Logf("unexpected lookup path: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}), "synthetic-token")
			if _, _, err := client.Lookup(context.Background(), id); err == nil || called {
				t.Fatalf("dot-segment ID reached transport or was accepted: called=%v err=%v", called, err)
			}
		})
	}
}
