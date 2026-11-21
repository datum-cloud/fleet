package localvm

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

func TestSiteBootOriginAndLibvirtURIValidation(t *testing.T) {
	for value, want := range map[string]string{"": defaultBootOrigin, "http://10.96.12.7:8080": "http://10.96.12.7:8080"} {
		if got, err := parseBootOrigin(value); err != nil || got != want {
			t.Fatalf("%q: %q %v", value, got, err)
		}
	}
	for _, bad := range []string{"https://10.0.0.1:8080", "http://boot.local:8080", "http://10.0.0.1", "http://10.0.0.1:8080/x", "http://u@10.0.0.1:8080", "http://[::1]:8080", "http://10.0.0.1:0"} {
		if _, err := parseBootOrigin(bad); err == nil {
			t.Fatalf("accepted boot origin %q", bad)
		}
	}
	for _, good := range []string{"", "qemu:///system", "qemu:///system?socket=/run/fleet-libvirt/libvirt-sock"} {
		if _, err := parseLibvirtURI(good); err != nil {
			t.Fatalf("%q: %v", good, err)
		}
	}
	for _, bad := range []string{"qemu+tcp://runtime/system", "qemu:///session", "qemu:///system?socket=relative", "qemu:///system?socket=/run/x&no_verify=1", "qemu:///system?socket=/run/../etc"} {
		if _, err := parseLibvirtURI(bad); err == nil {
			t.Fatalf("accepted libvirt URI %q", bad)
		}
	}
}

// The site network forwards only when the boot Service is outside the
// hypervisor namespace, and the DHCP boot file names that exact origin.
func TestSiteNetworkUsesBootServiceOrigin(t *testing.T) {
	var defined string
	p := &Provider{root: t.TempDir(), bootMode: "pxe", bootOrigin: "http://10.96.0.40:8080", uri: "qemu:///system?socket=/run/fleet-libvirt/libvirt-sock"}
	p.command = func(_ context.Context, name string, args ...string) (string, error) {
		if args[1] != p.uri {
			t.Fatalf("libvirt URI not used: %v", args)
		}
		if args[2] == "net-create" {
			b, err := os.ReadFile(args[3])
			defined = string(b)
			return "", err
		}
		return "", nil
	}
	r := &record{Request: vmRequest("site-net"), BootToken: strings.Repeat("b", 64)}
	if err := p.prepareNetwork(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(defined, "<forward mode='nat'/>") || !strings.Contains(defined, "file='http://10.96.0.40:8080/"+key(r.Request.ID)[:16]+"/"+r.BootToken+"/boot.ipxe'") {
		t.Fatal(defined)
	}
	single := &Provider{root: t.TempDir(), bootMode: "pxe"}
	single.command = func(_ context.Context, name string, args ...string) (string, error) {
		if args[2] == "net-create" {
			b, _ := os.ReadFile(args[3])
			defined = string(b)
		}
		return "", nil
	}
	if err := single.prepareNetwork(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(defined, "<forward") {
		t.Fatal("single-container network gained forwarding")
	}
}

// The boot service and observer open the provider read-only: they cannot
// execute, and boot evidence lives outside the journal directory.
func TestSiteReadOnlyProviderAndSeparateBootEvidence(t *testing.T) {
	f := newVMFixture(t)
	f.p.bootMode = "pxe"
	q := vmRequest("site-boot")
	digest, _ := simulator.RequestDigest(q)
	r := record{Request: q, Result: simulator.OperationResult{ID: q.ID, Kind: q.Kind, InputDigest: digest, Phase: "Unknown"}, Receipt: strings.Repeat("a", 64), Stage: "installer-started", BootToken: strings.Repeat("b", 64), BootExpires: time.Now().Add(time.Minute)}
	if err := f.p.save(journal{BootMode: "pxe", Accelerator: f.p.mode(), Image: f.p.image, Epoch: q.Epoch, Binding: q.BindingID, Fence: q.FenceToken, Operations: map[string]*record{q.ID: &r}}); err != nil {
		t.Fatal(err)
	}
	boot := &Provider{root: f.p.root, bootRoot: t.TempDir(), image: f.p.image, bootMode: "pxe", readOnly: true, bootOrigin: "http://10.96.0.40:8080"}
	if err := os.Chmod(f.p.root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(f.p.root, 0700)
	if _, err := boot.Execute(context.Background(), q); !errors.Is(err, executor.ErrInvalid) {
		t.Fatalf("read-only provider executed: %v", err)
	}
	if _, found, err := boot.Lookup(context.Background(), q.ID); found || !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("read-only lookup of unfinished operation: %v %v", found, err)
	}
	w := httptest.NewRecorder()
	boot.BootHandler().ServeHTTP(w, httptest.NewRequest("GET", "/"+key(q.ID)[:16]+"/"+r.BootToken+"/boot.ipxe", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "http://10.96.0.40:8080/"+key(q.ID)[:16]) {
		t.Fatalf("boot script: %d %s", w.Code, w.Body.String())
	}
	log, err := os.ReadFile(filepath.Join(boot.bootDirectory(&r), "boot-delivery.log"))
	if err != nil || !strings.Contains(string(log), "write-complete boot.ipxe") {
		t.Fatalf("boot evidence: %v %s", err, log)
	}
	if _, err := os.Stat(filepath.Join(f.p.directory(&r), "boot-delivery.log")); !os.IsNotExist(err) {
		t.Fatal("boot service wrote into the journal directory")
	}
	if strings.Contains(string(log), r.BootToken) {
		t.Fatal("boot evidence contains the capability")
	}
}
