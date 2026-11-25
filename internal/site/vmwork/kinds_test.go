package vmwork

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	// bootstrap, given the receipt, is the extra line the installer prints
	// after it reads the seed back from the target disk.
	bootstrap func(receipt string) string
	// networkReceipt is the receipt a network boot's script carries, as the
	// boot server reads it from the open admission.
	networkReceipt func() string
	// failNetwork makes creating the provisioning network fail.
	failNetwork bool
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
		} else if strings.Contains(xml, "<boot dev='network'/>") && h.networkReceipt != nil {
			marker, receipt = "FLEET-INSTALLED:", h.networkReceipt()
		} else {
			// The disk boot prints the receipt the installer wrote.
			installed, _ := os.ReadFile(filepath.Join(filepath.Dir(log), "installer.log"))
			if match := regexp.MustCompile(`FLEET-INSTALLED:([0-9a-f]{64})`).FindStringSubmatch(string(installed)); match != nil {
				receipt = match[1]
			}
		}
		console := "booting\n" + marker + receipt + "\n"
		if marker == "FLEET-INSTALLED:" && h.bootstrap != nil {
			console = "booting\n" + h.bootstrap(receipt) + "\n" + marker + receipt + "\n"
		}
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
	case "net-create":
		if h.failNetwork {
			return "", errors.New("net-create failed")
		}
	case "net-dhcp-leases":
		return guestMAC + " 192.168.77.2/24", nil
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
	return ops.DeviceInstallationSpec{OperationEnvelope: envelope(), Image: ops.ImageInput{Name: name, Digest: digest, FirmwareModes: []string{"BIOS"}}, BootInterface: "primary", TargetDisks: []string{"disk-0"}}
}

func wipeSpec() ops.DeviceWipeSpec {
	return ops.DeviceWipeSpec{OperationEnvelope: envelope(), Step: 1, CleaningPolicyDigest: "sha256:" + key, TargetDisks: []string{"disk-0"}}
}

// TestAUEFIOnlyImageSendsNothing refuses an image the local guest's BIOS
// cannot boot, before any disk or boot.
func TestAUEFIOnlyImageSendsNothing(t *testing.T) {
	r := newRig(t)
	spec := installSpec()
	spec.Image.FirmwareModes = []string{"UEFI"}
	result, err := r.runner.Run(context.Background(), r.assignment("DeviceInstallation", "00000000-0000-4000-8000-000000000209", 2, spec))
	if err != nil || result.Phase != ops.PhaseFailed || result.Reason != custody.ReasonImageIncompatible || r.vm.creates != 0 {
		t.Fatalf("result = %+v, %v, %d boots", result, err, r.vm.creates)
	}
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

// sealedInstall is an install whose admitted inputs name a sealed payload,
// with the payload stored at the site as the site API stores it.
func (r *rig) sealedInstall(uid string, fence int64) (site.Assignment, site.Payload) {
	r.t.Helper()
	salt := bytes.Repeat([]byte{9}, 32)
	data := []byte(`{"userData":"#cloud-config\n","sshAuthorizedKeys":"ssh-ed25519 AAAAExampleOnly\n"}`)
	hash := sha256.New()
	hash.Write(salt)
	hash.Write(data)
	payload := site.Payload{Ref: "sealed-example", Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Salt: salt, Data: data}
	spec := installSpec()
	spec.Bootstrap = &ops.SealedInput{Ref: payload.Ref, Digest: payload.Digest}
	assignment := r.assignment("DeviceInstallation", uid, fence, spec)
	if err := r.store.Raise(device, fence); err != nil {
		r.t.Fatal(err)
	}
	if err := (site.StorePayloads{Store: r.store}).Put(assignment, payload); err != nil {
		r.t.Fatal(err)
	}
	// The boot server's delivery evidence for every network boot file.
	dir := filepath.Join(r.config.BootRoot, Key(assignment.Key()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	evidence := ""
	for _, name := range []string{"boot.ipxe", "kernel", "installer.gz", "seed.cpio"} {
		evidence += "2026-11-25T00:00:00Z write-complete " + name + " bytes=1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "boot-delivery.log"), []byte(evidence), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return assignment, payload
}

// TestTheInstallerVerifiesBootstrapDataOnDisk: a network install loads the
// seed as a second initrd, the installer reads it back from the target disk
// and reports its salted digest, and the evidence records it as
// installer-verified. An installer that reads back a wrong or truncated
// seed, or reports nothing, fails the install before the disk boot, and the
// seed is dropped either way. The installed OS is not asked to prove
// anything.
func TestTheInstallerVerifiesBootstrapDataOnDisk(t *testing.T) {
	truncated := func(receipt, _ string) string {
		hash := sha256.New()
		hash.Write(bytes.Repeat([]byte{9}, 32))
		hash.Write([]byte(`{"userData":"#cloud-config\n"`))
		return site.BootstrapLine(receipt, "sha256:"+hex.EncodeToString(hash.Sum(nil)))
	}
	for name, test := range map[string]struct {
		line func(receipt, digest string) string
		ok   bool
	}{
		"the admitted seed":      {func(receipt, digest string) string { return site.BootstrapLine(receipt, digest) }, true},
		"a wrong seed read back": {func(receipt, _ string) string { return site.BootstrapLine(receipt, "sha256:"+strings.Repeat("0", 64)) }, false},
		"a truncated seed":       {truncated, false},
		"no verification line":   {func(string, string) string { return "" }, false},
		"another receipt":        {func(_, digest string) string { return site.BootstrapLine(strings.Repeat("e", 64), digest) }, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.config.PXE = true
			r.runner.Kinds["DeviceInstallation"] = Installation{r.config}
			r.runner.Payloads = site.StorePayloads{Store: r.store}
			assignment, payload := r.sealedInstall("00000000-0000-4000-8000-000000000211", 2)
			r.vm.bootstrap = func(receipt string) string { return test.line(receipt, payload.Digest) }
			r.vm.networkReceipt = func() string {
				var boot Boot
				_, _ = r.store.GetRecord(device, bootRecords, Key(assignment.Key()), &boot)
				return boot.Receipt
			}
			result, err := r.runner.Run(context.Background(), assignment)
			if err != nil {
				t.Fatal(err)
			}
			var boot Boot
			if found, err := r.store.GetRecord(device, bootRecords, Key(assignment.Key()), &boot); err != nil || !found || boot.Open || len(boot.Seed) != 0 {
				t.Fatalf("admission after the install = open %v, seed %d bytes, %v, %v", boot.Open, len(boot.Seed), found, err)
			}
			if !test.ok {
				if result.Phase == ops.PhaseSucceeded || r.vm.creates != 1 {
					t.Fatalf("result = %+v after %d boots; a failed check must stop before the disk boot", result, r.vm.creates)
				}
				return
			}
			var evidence ops.InstallationEvidence
			if result.Phase != ops.PhaseSucceeded || json.Unmarshal(result.Evidence, &evidence) != nil || evidence.InstallerVerifiedBootstrap != payload.Digest {
				t.Fatalf("result = %+v, evidence %+v", result, evidence)
			}
		})
	}
}

// TestADirectBootRefusesBootstrapData: a direct kernel boot has one initrd,
// so it cannot hand the seed to the guest, and sends nothing.
func TestADirectBootRefusesBootstrapData(t *testing.T) {
	r := newRig(t)
	r.runner.Payloads = site.StorePayloads{Store: r.store}
	assignment, _ := r.sealedInstall("00000000-0000-4000-8000-000000000212", 2)
	result, err := r.runner.Run(context.Background(), assignment)
	if err != nil || result.Phase != ops.PhaseFailed || result.Reason != custody.ReasonBootstrapUnsupported || r.vm.creates != 0 {
		t.Fatalf("result = %+v, %v, %d boots", result, err, r.vm.creates)
	}
}

// TestTheBootServerServesTheSeedOnlyWhileOpen: the script loads the seed as
// a second initrd, and the seed is served only under the open admission.
func TestTheBootServerServesTheSeedOnlyWhileOpen(t *testing.T) {
	r := newRig(t)
	if err := r.store.Raise(device, 4); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 64)
	boot := Boot{Operation: "00000000-0000-4000-8000-000000000213", Fence: 4, Receipt: strings.Repeat("d", 64), Token: token, Expires: time.Now().Add(time.Minute), Open: true, Seed: []byte("seed-bytes")}
	if err := r.store.PutRecord(device, 4, bootRecords, Key(boot.Operation), boot); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.config.BootHandler(r.store))
	defer server.Close()
	get := func(name string) (int, string) {
		response, err := http.Get(server.URL + "/" + Key(boot.Operation) + "/" + token + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	if code, script := get("boot.ipxe"); code != http.StatusOK || !strings.Contains(script, "/seed.cpio\n") {
		t.Fatalf("script = %d %q", code, script)
	}
	if code, body := get("seed.cpio"); code != http.StatusOK || body != "seed-bytes" {
		t.Fatalf("seed = %d %q", code, body)
	}
	closeBoot(&boot)
	if err := r.store.PutRecord(device, 4, bootRecords, Key(boot.Operation), boot); err != nil {
		t.Fatal(err)
	}
	if code, _ := get("seed.cpio"); code != http.StatusForbidden {
		t.Fatalf("seed after close = %d", code)
	}
}

// TestEveryEndDropsTheSeed: a seed is customer data in the site store, so
// an install that fails before its network boot opens, or whose admission
// expires, drops it as surely as a clean close does, and a newer fence
// drops a superseded operation's.
func TestEveryEndDropsTheSeed(t *testing.T) {
	seed := func(r *rig, uid string) []byte {
		var boot Boot
		if _, err := r.store.GetRecord(device, bootRecords, Key(uid), &boot); err != nil {
			t.Fatal(err)
		}
		return boot.Seed
	}
	for name, setup := range map[string]func(*rig){
		"a failure before the admission opens": func(r *rig) { r.vm.failNetwork = true },
		"an admission that expires":            func(r *rig) { r.config.BootWindow = time.Nanosecond },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.config.PXE = true
			setup(r)
			r.runner.Kinds["DeviceInstallation"] = Installation{r.config}
			r.runner.Payloads = site.StorePayloads{Store: r.store}
			uid := "00000000-0000-4000-8000-000000000221"
			assignment, _ := r.sealedInstall(uid, 2)
			result, err := r.runner.Run(context.Background(), assignment)
			if err != nil || result.Phase == ops.PhaseSucceeded {
				t.Fatalf("result = %+v, %v", result, err)
			}
			if got := seed(r, uid); len(got) != 0 {
				t.Fatalf("the site kept %d bytes of seed after the install ended", len(got))
			}
		})
	}
	t.Run("a newer fence", func(t *testing.T) {
		r := newRig(t)
		uid := "00000000-0000-4000-8000-000000000222"
		boot := Boot{Operation: uid, Fence: 2, Receipt: strings.Repeat("d", 64), Token: strings.Repeat("c", 64), Expires: time.Now().Add(time.Minute), Seed: []byte("seed-bytes")}
		if err := r.store.Raise(device, 2); err != nil {
			t.Fatal(err)
		}
		if err := r.store.PutRecord(device, 2, bootRecords, Key(uid), boot); err != nil {
			t.Fatal(err)
		}
		// A recovery only raises the fence; nothing of its own ends.
		recovery := r.assignment("DeviceRecovery", "00000000-0000-4000-8000-000000000223", 3, wipeSpec())
		if err := r.runner.Fence(context.Background(), recovery); err != nil {
			t.Fatal(err)
		}
		if got := seed(r, uid); len(got) != 0 {
			t.Fatalf("a superseded admission kept %d bytes of seed", len(got))
		}
	})
}
