package vmwork

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/fixture"
)

const (
	device = "fleet-provider/server-example-001"
	image  = "fleet-local-linux@sha256:" + "abababababababababababababababababababababababababababababababab"
	key    = "1111111111111111111111111111111111111111111111111111111111111111"
)

// hypervisor is a fake virsh: a created domain runs until the test lets it
// print its receipt and stop.
type hypervisor struct {
	mu      sync.Mutex
	running map[string]string // domain -> console log
	creates int
	// hold keeps created domains running.
	hold bool
	// noReceipt makes the guest print no receipt.
	noReceipt bool
}

var receiptArg = regexp.MustCompile(`fleet.receipt=([0-9a-f]{64})`)

func (h *hypervisor) run(_ context.Context, _ string, args ...string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch args[0] {
	case "create":
		h.creates++
		data, err := os.ReadFile(args[1])
		if err != nil {
			return "", err
		}
		xml := string(data)
		name := regexp.MustCompile(`<name>([^<]+)</name>`).FindStringSubmatch(xml)[1]
		log := regexp.MustCompile(`<serial type='file'><source path='([^']+)'/>`).FindStringSubmatch(xml)[1]
		marker := "FLEET-DISK-BOOT:"
		receipt := ""
		if match := receiptArg.FindStringSubmatch(xml); match != nil {
			marker, receipt = "FLEET-INSTALLED:", match[1]
		} else {
			// The disk boot prints the receipt the installer wrote.
			installed, _ := os.ReadFile(filepath.Join(filepath.Dir(log), "installer.log"))
			if match := regexp.MustCompile(`FLEET-INSTALLED:([0-9a-f]{64})`).FindStringSubmatch(string(installed)); match != nil {
				receipt = match[1]
			}
		}
		console := "booting\n" + marker + receipt + "\n"
		if h.noReceipt {
			console = "booting\n"
		}
		if err := os.WriteFile(log, []byte(console), 0o600); err != nil {
			return "", err
		}
		h.running[name] = log
		if !h.hold {
			delete(h.running, name)
		}
	case "list":
		var names []string
		for name := range h.running {
			names = append(names, name)
		}
		return strings.Join(names, "\n"), nil
	}
	return "", nil
}

type rig struct {
	t      *testing.T
	store  *site.Store
	runner *site.Runner
	config Config
	vm     *hypervisor
}

func newRig(t *testing.T) *rig {
	root := t.TempDir()
	for _, dir := range []string{"store", "disks", "boot", "artifacts"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := site.OpenStore(filepath.Join(root, "store"), nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := &hypervisor{running: map[string]string{}}
	config := Config{Artifacts: filepath.Join(root, "artifacts"), Image: image, Disks: filepath.Join(root, "disks"), BootRoot: filepath.Join(root, "boot"), Command: vm.run}
	enrollment := fixture.Enrollment{device: {BMCEndpoint: "https://[2001:db8::1]", SystemUUID: "00000000-0000-4000-8000-000000000001", BootMACAddress: "02:00:00:00:00:01", BMCPublicKeySHA256: key, Disks: []string{"disk-0"}}}
	runner := &site.Runner{Site: "site-example-a", Store: store, Results: site.NewResults(store), Access: fixture.Access{Enrollment: enrollment},
		Kinds: map[string]site.Kind{"DeviceInstallation": Installation{config}, "DeviceWipe": Wipe{config}}, Poll: 10 * time.Millisecond}
	return &rig{t: t, store: store, runner: runner, config: config, vm: vm}
}

func (r *rig) assignment(kind, uid string, fence int64, spec any) site.Assignment {
	inputs, _ := json.Marshal(spec)
	digest, _ := custody.SpecDigest(spec)
	now := time.Now().UTC()
	return site.Assignment{Device: device, Operation: ops.OperationReference{Kind: kind, Name: "op-" + uid[len(uid)-3:]}, Fence: fence, AdmittedAt: now, Deadline: now.Add(30 * time.Second),
		Snapshot: ops.DeviceSnapshot{BMCEndpoint: "https://[2001:db8::1]", SystemUUID: "00000000-0000-4000-8000-000000000001", BootMACAddress: "02:00:00:00:00:01", BMCPublicKeySHA256: key}, InputDigest: digest, Inputs: inputs}
}

func envelope() ops.OperationEnvelope {
	return ops.OperationEnvelope{Creator: ops.Creator{Username: "provider", UID: "u"}, DeviceRef: ops.DeviceReference{Name: "server-example-001"}, Authority: ops.Authority{AllocationRef: &ops.LocalReference{Name: "alloc-example"}}}
}

func installSpec() ops.DeviceInstallationSpec {
	name, digest, _ := strings.Cut(image, "@")
	return ops.DeviceInstallationSpec{OperationEnvelope: envelope(), Image: ops.ImageInput{Name: name, Digest: digest}, BootInterface: "primary", TargetDisks: []string{"disk-0"}}
}

func wipeSpec() ops.DeviceWipeSpec {
	return ops.DeviceWipeSpec{OperationEnvelope: envelope(), Step: 1, CleaningPolicyDigest: "sha256:" + key, TargetDisks: []string{"disk-0"}}
}

// TestInstallThenWipe installs directly, proves each phase by its receipt,
// and wipes every disk of the Device.
func TestInstallThenWipe(t *testing.T) {
	r := newRig(t)
	result, err := r.runner.Run(context.Background(), r.assignment("DeviceInstallation", "00000000-0000-4000-8000-000000000201", 2, installSpec()))
	if err != nil || result.Phase != ops.PhaseSucceeded {
		t.Fatalf("install = %+v, %v", result, err)
	}
	var evidence ops.InstallationEvidence
	if err := json.Unmarshal(result.Evidence, &evidence); err != nil || len(evidence.Steps) != 3 {
		t.Fatalf("evidence = %s, %v", result.Evidence, err)
	}
	if r.vm.creates != 2 {
		t.Fatalf("boots = %d, want the installer and the disk once each", r.vm.creates)
	}
	disks, _ := filepath.Glob(filepath.Join(r.config.Disks, "*", "*", "disk.raw"))
	if len(disks) != 1 {
		t.Fatalf("disks = %v", disks)
	}
	// A redelivery returns the stored result and boots nothing.
	if again, err := r.runner.Run(context.Background(), r.assignment("DeviceInstallation", "00000000-0000-4000-8000-000000000201", 2, installSpec())); err != nil || again.Phase != ops.PhaseSucceeded || r.vm.creates != 2 {
		t.Fatalf("redelivery = %+v, %v, boots %d", again, err, r.vm.creates)
	}
	wiped, err := r.runner.Run(context.Background(), r.assignment("DeviceWipe", "00000000-0000-4000-8000-000000000202", 3, wipeSpec()))
	if err != nil || wiped.Phase != ops.PhaseSucceeded {
		t.Fatalf("wipe = %+v, %v", wiped, err)
	}
	if disks, _ := filepath.Glob(filepath.Join(r.config.Disks, "*", "*", "disk.raw")); len(disks) != 0 {
		t.Fatalf("disks after the wipe = %v", disks)
	}
}

// TestAWipeWithARunningDomainIsNotProven keeps a disk in use from counting
// as wiped.
func TestAWipeWithARunningDomainIsNotProven(t *testing.T) {
	r := newRig(t)
	r.vm.running["fleet-local-0000000000000000"] = "console"
	result, err := r.runner.Run(context.Background(), r.assignment("DeviceWipe", "00000000-0000-4000-8000-000000000203", 2, wipeSpec()))
	if err != nil || result.Phase == ops.PhaseSucceeded {
		t.Fatalf("wipe with a running domain = %+v, %v", result, err)
	}
}

// TestAResumedBootIsObservedNotRepeated crashes a worker after it started
// the installer; the next worker sees the domain and never boots it again.
func TestAResumedBootIsObservedNotRepeated(t *testing.T) {
	r := newRig(t)
	r.vm.hold = true
	assignment := r.assignment("DeviceInstallation", "00000000-0000-4000-8000-000000000204", 2, installSpec())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.runner.Run(ctx, assignment)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.vm.mu.Lock()
		creates := r.vm.creates
		r.vm.mu.Unlock()
		if creates == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the installer never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	// The guest finishes while no worker runs.
	r.vm.mu.Lock()
	r.vm.hold = false
	for name := range r.vm.running {
		delete(r.vm.running, name)
	}
	r.vm.mu.Unlock()
	result, err := r.runner.Run(context.Background(), assignment)
	if err != nil || result.Phase != ops.PhaseSucceeded {
		t.Fatalf("resumed install = %+v, %v", result, err)
	}
	if r.vm.creates != 2 {
		t.Fatalf("boots = %d, want the installer once and the disk once", r.vm.creates)
	}
}

// TestTheBootServerServesOnlyAnOpenAdmissionAtTheStoredFence denies a
// closed admission, a wrong token and a superseded fence.
func TestTheBootServerServesOnlyAnOpenAdmissionAtTheStoredFence(t *testing.T) {
	r := newRig(t)
	if err := r.store.Raise(device, 4); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 64)
	boot := Boot{Operation: "00000000-0000-4000-8000-000000000205", Fence: 4, Receipt: strings.Repeat("d", 64), Token: token, Expires: time.Now().Add(time.Minute), Open: true}
	if err := r.store.PutRecord(device, 4, bootRecords, Key(boot.Operation), boot); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.config.BootHandler(r.store))
	defer server.Close()
	get := func(path string) int {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	script := "/" + Key(boot.Operation) + "/" + token + "/boot.ipxe"
	if code := get(script); code != http.StatusOK {
		t.Fatalf("open admission = %d", code)
	}
	if code := get("/" + Key(boot.Operation) + "/" + strings.Repeat("e", 64) + "/boot.ipxe"); code != http.StatusForbidden {
		t.Fatalf("wrong token = %d", code)
	}
	if code := get("/" + Key(boot.Operation) + "/" + token + "/disk.raw"); code != http.StatusForbidden {
		t.Fatalf("unsupported artifact = %d", code)
	}
	// A newer operation's fence closes this admission at once.
	if err := r.store.Raise(device, 5); err != nil {
		t.Fatal(err)
	}
	if code := get(script); code != http.StatusForbidden {
		t.Fatalf("superseded admission = %d", code)
	}
}

// TestAFailedNetworkBootClosesItsAdmission stops serving the installer as
// soon as a network-boot installation fails.
func TestAFailedNetworkBootClosesItsAdmission(t *testing.T) {
	r := newRig(t)
	r.config.PXE = true
	r.runner.Kinds["DeviceInstallation"] = Installation{r.config}
	// The guest prints no receipt, so the installer phase fails.
	r.vm.noReceipt = true
	assignment := r.assignment("DeviceInstallation", "00000000-0000-4000-8000-000000000206", 2, installSpec())
	result, err := r.runner.Run(context.Background(), assignment)
	if err != nil || result.Phase == ops.PhaseSucceeded {
		t.Fatalf("install without a receipt = %+v, %v", result, err)
	}
	var boot Boot
	if found, err := r.store.GetRecord(device, bootRecords, Key(assignment.Key()), &boot); err != nil || !found || boot.Open || !boot.Closed {
		t.Fatalf("admission after the failure = %+v, %v, %v", boot, found, err)
	}
}
