package localvm

import (
	"encoding/json"
	"go.datum.net/fleet/internal/simulator"
	"strings"
	"testing"
)

func TestBoundedInputs(t *testing.T) {
	p := &Provider{image: "approved@sha256:abc"}
	base := simulator.Request{ID: "installation", Kind: "install", AssetID: AssetID, BindingID: "allocation", Epoch: 1, FenceToken: "fence", Inputs: json.RawMessage(`{"image":"approved@sha256:abc"}`)}
	if e := p.validate(base); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*simulator.Request){func(q *simulator.Request) { q.AssetID = "other" }, func(q *simulator.Request) { q.Epoch = 0 }, func(q *simulator.Request) { q.FenceToken = "" }, func(q *simulator.Request) { q.Inputs = json.RawMessage(`{"image":"unapproved"}`) }, func(q *simulator.Request) {
		q.Inputs = json.RawMessage(`{"image":"approved@sha256:abc","path":"/host"}`)
	}, func(q *simulator.Request) { q.Kind = "cleanup"; q.Inputs = json.RawMessage(`{"target":"/host"}`) }, func(q *simulator.Request) { q.Kind = "power" }} {
		q := base
		mutate(&q)
		if p.validate(q) == nil {
			t.Fatalf("accepted unsupported request: %#v", q)
		}
	}
}
func TestDiskBootHasNoInstallerAndNamesAreOpaque(t *testing.T) {
	r := &record{Request: simulator.Request{ID: "../../host' <xml>"}, Receipt: strings.Repeat("a", 64)}
	xml := (&Provider{}).domainXML(r, "disk")
	if strings.Contains(xml, "<kernel>") || strings.Contains(xml, "<initrd>") || strings.Contains(xml, "<interface") || strings.Contains(xml, r.Request.ID) {
		t.Fatal("disk boot contains caller input or installer/network")
	}
	if !strings.Contains(xml, "<boot dev='hd'/>") {
		t.Fatal("missing disk boot")
	}
}
