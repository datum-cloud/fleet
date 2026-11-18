package localvm

import (
	"go.datum.net/fleet/internal/simulator"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBootAdmission(t *testing.T) {
	f := newVMFixture(t)
	p := f.p
	p.bootMode = "pxe"
	q := vmRequest("admitted-installation")
	digest, _ := simulator.RequestDigest(q)
	base := record{Request: q, Result: simulator.OperationResult{ID: q.ID, Kind: q.Kind, InputDigest: digest, Phase: "Unknown"}, Receipt: strings.Repeat("a", 64), Stage: "installer-started", BootToken: strings.Repeat("b", 64), BootExpires: time.Now().Add(time.Minute)}
	os.Mkdir(p.directory(&base), 0700)
	for _, tc := range []struct {
		name    string
		change  func(*record, *journal)
		path    string
		allowed bool
	}{
		{name: "admitted", allowed: true},
		{name: "unknown identity", path: "/unknown/" + base.BootToken + "/boot.ipxe"},
		{name: "wrong operation credential", path: "/" + key(q.ID)[:16] + "/" + strings.Repeat("c", 64) + "/boot.ipxe"},
		{name: "expired", change: func(r *record, j *journal) { r.BootExpires = time.Now().Add(-time.Second) }},
		{name: "revoked", change: func(r *record, j *journal) { r.BootClosed = true }},
		{name: "not started", change: func(r *record, j *journal) { r.Stage = "admitted" }},
		{name: "stale epoch", change: func(r *record, j *journal) { j.Epoch++ }},
		{name: "wrong fence", change: func(r *record, j *journal) { j.Fence = "other" }},
		{name: "closed predecessor", change: func(r *record, j *journal) { r.Stage = "installer-verified"; r.BootClosed = true }},
		{name: "closed allocation", change: func(r *record, j *journal) { j.Closed = true }},
		{name: "unsupported artifact", path: "/" + key(q.ID)[:16] + "/" + base.BootToken + "/disk.raw"},
		{name: "unguarded artifact", path: "/kernel"},
		{name: "traversal", path: "/" + key(q.ID)[:16] + "/" + base.BootToken + "/../journal.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			j := journal{BootMode: "pxe", Accelerator: p.mode(), Image: p.image, Epoch: q.Epoch, Binding: q.BindingID, Fence: q.FenceToken, Operations: map[string]*record{q.ID: &r}}
			if tc.change != nil {
				tc.change(&r, &j)
			}
			if err := p.save(j); err != nil {
				t.Fatal(err)
			}
			path := tc.path
			if path == "" {
				path = "/" + key(q.ID)[:16] + "/" + base.BootToken + "/boot.ipxe"
			}
			w := httptest.NewRecorder()
			p.BootHandler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			want := 403
			if tc.allowed {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("status %d want %d", w.Code, want)
			}
		})
	}
}
func TestFirmwarePXEAndModeBinding(t *testing.T) {
	p := &Provider{bootMode: "pxe"}
	r := &record{}
	x := p.domainXML(r, "installer")
	if strings.Contains(x, "<kernel>") || strings.Contains(x, "<initrd>") || strings.Contains(x, "boot dev='hd'") || !strings.Contains(x, "boot dev='network'") || !strings.Contains(x, "<interface") {
		t.Fatal(x)
	}
	if p.checkMode(journal{Image: "old", Accelerator: "kvm"}) == nil {
		t.Fatal("silently migrated direct journal")
	}
	x = p.domainXML(r, "disk")
	if strings.Contains(x, "<interface") || strings.Contains(x, "<kernel>") {
		t.Fatal(x)
	}
}

func TestCrossOperationCapabilityOverHTTP(t *testing.T) {
	f := newVMFixture(t)
	p := f.p
	p.bootMode = "pxe"
	j := journal{BootMode: "pxe", Accelerator: p.mode(), Image: p.image, Epoch: 1, Binding: "allocation", Fence: "fence", Operations: map[string]*record{}}
	for i, id := range []string{"predecessor", "successor"} {
		q := vmRequest(id)
		d, _ := simulator.RequestDigest(q)
		r := &record{Request: q, Result: simulator.OperationResult{ID: q.ID, Kind: q.Kind, InputDigest: d, Phase: "Unknown"}, Receipt: strings.Repeat("a", 64), Stage: "installer-started", BootToken: strings.Repeat(string(rune('b'+i)), 64), BootExpires: time.Now().Add(time.Minute)}
		j.Operations[id] = r
		if err := os.Mkdir(p.directory(r), 0700); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(p.BootHandler())
	defer server.Close()
	check := func(op, token string, want int) {
		t.Helper()
		resp, err := server.Client().Get(server.URL + "/" + key(op)[:16] + "/" + token + "/boot.ipxe")
		if err != nil {
			t.Fatal("HTTP request failed")
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("status %d want %d", resp.StatusCode, want)
		}
	}
	save := func() {
		t.Helper()
		if err := p.save(j); err != nil {
			t.Fatal(err)
		}
	}
	save()
	check("successor", j.Operations["predecessor"].BootToken, 403)
	check("successor", j.Operations["successor"].BootToken, 200)
	j.Operations["successor"].BootExpires = time.Now().Add(-time.Second)
	save()
	check("successor", j.Operations["successor"].BootToken, 403)
	j.Operations["predecessor"].BootClosed = true
	save()
	check("predecessor", j.Operations["predecessor"].BootToken, 403)
}
