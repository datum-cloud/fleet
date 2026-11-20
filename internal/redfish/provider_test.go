package redfish

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
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

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

const (
	assetA = "fleet-provider/fleet.datumapis.com/Device/synthetic-device-central-01"
	assetB = "fleet-provider/fleet.datumapis.com/Device/synthetic-device-central-02"
	uuidA  = "7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0a"
	uuidB  = "7f3d0c2e-1a5b-4c6d-8e9f-0a1b2c3d4e0b"
)

// fakeSystem is BMC state plus a fake guest that behaves like the lab
// installer: it fetches its script and artifacts from the boot handler,
// prints exactly one receipt on its console, and powers off.
type fakeSystem struct {
	id, uuid, mac, console string
	power, enabled, target string
	disk                   string // receipt of the installed OS, "" when blank
	boots                  []string
}

type fakeBMC struct {
	t                                        *testing.T
	mu                                       sync.Mutex
	systems                                  map[string]*fakeSystem
	mutations                                []string
	rejectAuth, hideOn, dropOnce, ignoreOnce bool
	dropSkip                                 int
	hold                                     time.Duration
	boot                                     string // boot handler origin
	wg                                       sync.WaitGroup
}

func (b *fakeBMC) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.URL.Path == "/redfish/v1/" {
			write(map[string]any{"Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}})
			return
		}
		user, pass, ok := r.BasicAuth()
		b.mu.Lock()
		reject := b.rejectAuth
		b.mu.Unlock()
		if !ok || user != "operator" || pass != "synthetic" || reject {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/redfish/v1/Systems" {
			write(map[string]any{"Members": []any{map[string]string{"@odata.id": "/redfish/v1/Systems/System-1"}, map[string]string{"@odata.id": "/redfish/v1/Systems/System-2"}}})
			return
		}
		m := regexp.MustCompile(`^/redfish/v1/Systems/([^/]+)(/Actions/ComputerSystem\.Reset|/ResetActionInfo)?$`).FindStringSubmatch(r.URL.Path)
		if m == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b.mu.Lock()
		s := b.systems[m[1]]
		types := []string{"On", "ForceOff", "ForceRestart"}
		if b.hideOn {
			types = types[1:]
		}
		switch {
		case r.Method == http.MethodGet && m[2] == "":
			v := map[string]any{"@odata.id": r.URL.Path, "Id": s.id, "UUID": s.uuid, "PowerState": s.power,
				"Boot": map[string]any{"BootSourceOverrideEnabled": s.enabled, "BootSourceOverrideTarget": s.target,
					"BootSourceOverrideTarget@Redfish.AllowableValues": []string{"None", "Pxe", "Hdd"}, "BootSourceOverrideEnabled@Redfish.AllowableValues": []string{"Disabled", "Once"}},
				"Actions": map[string]any{"#ComputerSystem.Reset": map[string]string{"target": r.URL.Path + "/Actions/ComputerSystem.Reset", "@Redfish.ActionInfo": r.URL.Path + "/ResetActionInfo"}}}
			b.mu.Unlock()
			write(v)
		case r.Method == http.MethodGet && m[2] == "/ResetActionInfo":
			b.mu.Unlock()
			// Shaped like the DMTF emulator 1.2.2 template, including its
			// off-schema string "Required".
			write(map[string]any{"Parameters": []any{map[string]any{"Name": "ResetType", "Required": "true", "DataType": "String", "AllowableValues": types}}})
		case r.Method == http.MethodPatch && m[2] == "":
			var body struct{ Boot map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.enabled, s.target = body.Boot["BootSourceOverrideEnabled"], body.Boot["BootSourceOverrideTarget"]
			b.mutations = append(b.mutations, s.id+" PATCH "+s.target)
			b.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			write(map[string]any{})
		case r.Method == http.MethodPost && m[2] != "":
			var body struct{ ResetType string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.mutations = append(b.mutations, s.id+" POST "+body.ResetType)
			allowed := false
			for _, t := range types {
				allowed = allowed || t == body.ResetType
			}
			if !allowed {
				b.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if body.ResetType == "On" && b.ignoreOnce {
				b.ignoreOnce = false
				b.mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			drop := false
			if body.ResetType == "On" && s.power == "Off" {
				boot := "Hdd"
				if s.enabled == "Once" {
					boot = s.target
				}
				s.enabled, s.target, s.power = "Disabled", "None", "On"
				s.boots = append(s.boots, boot)
				if b.dropOnce && b.dropSkip > 0 {
					b.dropSkip--
				} else {
					drop, b.dropOnce = b.dropOnce, false
				}
				b.wg.Add(1)
				go b.guest(s, boot)
			}
			if body.ResetType == "ForceOff" {
				s.power = "Off"
			}
			b.mu.Unlock()
			if drop {
				time.Sleep(b.hold)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			b.mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func (b *fakeBMC) console(s *fakeSystem, line string) {
	f, err := os.OpenFile(s.console, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
}

func (b *fakeBMC) guest(s *fakeSystem, boot string) {
	defer b.wg.Done()
	time.Sleep(20 * time.Millisecond)
	line := ""
	if boot == "Pxe" {
		mac := strings.ReplaceAll(s.mac, ":", "-")
		script := get(b.boot + "/pxe/" + mac + "/boot.ipxe")
		f := regexp.MustCompile(`kernel (\S+) .*fleet\.receipt=([0-9a-f]{64}) fleet\.mode=(\w+)\ninitrd (\S+)`).FindStringSubmatch(script)
		if f != nil && get(f[1]) != "" && get(f[4]) != "" {
			b.mu.Lock()
			if f[3] == "wipe" {
				s.disk = ""
				line = "FLEET-WIPED:" + f[2] + ":2147483648"
			} else {
				s.disk = f[2]
				line = "FLEET-INSTALLED:" + f[2]
			}
			b.mu.Unlock()
		}
	} else if s.disk != "" {
		line = "FLEET-DISK-BOOT:" + s.disk
	}
	if line != "" {
		b.console(s, line)
	}
	time.Sleep(20 * time.Millisecond)
	b.mu.Lock()
	s.power = "Off"
	b.mu.Unlock()
}

func get(u string) string {
	resp, err := http.Get(u)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return string(b)
}

func (b *fakeBMC) mutationsFor(id string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, m := range b.mutations {
		if strings.HasPrefix(m, id+" ") {
			out = append(out, strings.TrimPrefix(m, id+" "))
		}
	}
	return out
}

type fixture struct {
	p   *Provider
	bmc *fakeBMC
	dir string
}

func newFixture(t *testing.T, timeout time.Duration) *fixture {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"journal", "boot", "console", "artifacts"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "artifacts/kernel"), []byte("kernel"), 0600)
	os.WriteFile(filepath.Join(dir, "artifacts/installer.gz"), []byte("initrd"), 0600)
	sums := ""
	for _, n := range []string{"kernel", "installer.gz"} {
		s, _ := fileSHA256(filepath.Join(dir, "artifacts", n))
		sums += s + "  " + n + "\n"
	}
	os.WriteFile(filepath.Join(dir, "artifacts/SHA256SUMS"), []byte(sums), 0600)
	image, err := verifyArtifacts(filepath.Join(dir, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	bmc := &fakeBMC{t: t, hold: 3 * timeout, systems: map[string]*fakeSystem{
		"System-1": {id: "System-1", uuid: uuidA, mac: "52:54:00:77:01:0a", console: filepath.Join(dir, "console/target-a.log"), power: "On", enabled: "Disabled", target: "None"},
		"System-2": {id: "System-2", uuid: uuidB, mac: "52:54:00:77:01:0b", console: filepath.Join(dir, "console/target-b.log"), power: "Off", enabled: "Disabled", target: "None"}}}
	rf := httptest.NewServer(bmc.handler())
	t.Cleanup(rf.Close)
	enrollment, err := ParseEnrollment([]byte(fmt.Sprintf(`{"endpoint":%q,"targets":[
	 {"assetID":%q,"systemID":"System-1","systemUUID":%q,"mac":"52:54:00:77:01:0a","diskBytes":2147483648,"console":"target-a.log"},
	 {"assetID":%q,"systemID":"System-2","systemUUID":%q,"mac":"52:54:00:77:01:0b","diskBytes":2147483648,"console":"target-b.log"}]}`, rf.URL, assetA, uuidA, assetB, uuidB)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(rf.URL, "operator", "synthetic", timeout)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{root: filepath.Join(dir, "journal"), bootRoot: filepath.Join(dir, "boot"), consoleRoot: filepath.Join(dir, "console"), artifacts: filepath.Join(dir, "artifacts"),
		image: image, enrollment: enrollment, client: client, poll: 5 * time.Millisecond, settle: 150 * time.Millisecond, offGrace: 300 * time.Millisecond, outage: time.Second}
	boot := httptest.NewServer(p.BootHandler())
	t.Cleanup(boot.Close)
	p.origin, bmc.boot = boot.URL, boot.URL
	t.Cleanup(bmc.wg.Wait)
	return &fixture{p: p, bmc: bmc, dir: dir}
}

func (f *fixture) install(epoch int64) simulator.Request {
	return simulator.Request{ID: fmt.Sprintf("install-%d", epoch), Kind: "install", AssetID: assetA, BindingID: fmt.Sprintf("binding-%d", epoch), Epoch: epoch,
		FenceToken: fmt.Sprintf("fence-%d", epoch), Inputs: json.RawMessage(`{"image":"` + f.p.image + `"}`)}
}

func cleanup(q simulator.Request) simulator.Request {
	q.ID, q.Kind, q.Inputs = strings.Replace(q.ID, "install", "cleanup", 1), "cleanup", nil
	return q
}

func (f *fixture) stage(t *testing.T, id string) string {
	j, err := f.p.read()
	if err != nil {
		t.Fatal(err)
	}
	return j.Operations[id].Stage
}

func TestInstallDiskBootCleanupAndReuseThroughRedfish(t *testing.T) {
	f := newFixture(t, time.Second)
	ctx := context.Background()
	q := f.install(1)
	r, err := f.p.Execute(ctx, q)
	if err != nil || r.Result != "installed" {
		t.Fatalf("install: %v %+v", err, r)
	}
	f.bmc.wg.Wait()
	if got := f.bmc.mutationsFor("System-1"); strings.Join(got, ",") != "POST ForceOff,PATCH Pxe,POST On,PATCH Hdd,POST On" {
		t.Fatalf("unexpected Redfish effects: %v", got)
	}
	if got := f.bmc.systems["System-1"].boots; strings.Join(got, ",") != "Pxe,Hdd" {
		t.Fatalf("expected one network install and one disk boot, got %v", got)
	}
	// Exact replay is a verified no-op.
	same := func(a, b simulator.OperationResult) bool {
		return a.ID == b.ID && a.InputDigest == b.InputDigest && a.Result == b.Result && a.CompletedAt.Equal(*b.CompletedAt)
	}
	if again, err := f.p.Execute(ctx, q); err != nil || !same(again, r) || len(f.bmc.mutationsFor("System-1")) != 5 {
		t.Fatalf("replay repeated effects: %v", err)
	}
	if got, found, err := f.p.Lookup(ctx, q.ID); !found || err != nil || !same(got, r) {
		t.Fatalf("lookup: %v %v", found, err)
	}
	c := cleanup(q)
	if r, err := f.p.Execute(ctx, c); err != nil || r.Result != "clean" {
		t.Fatalf("cleanup: %v %+v", err, r)
	}
	f.bmc.wg.Wait()
	if f.bmc.systems["System-1"].disk != "" {
		t.Fatal("cleanup did not boot the wipe environment")
	}
	// Stale authority after the closed epoch is fenced before any request.
	stale := q
	stale.ID = "install-replay"
	if _, err := f.p.Execute(ctx, stale); !errors.Is(err, executor.ErrFenced) {
		t.Fatalf("stale epoch replay: %v", err)
	}
	// Reuse in a new epoch installs again with a distinct receipt.
	if r, err := f.p.Execute(ctx, f.install(2)); err != nil || r.Result != "installed" {
		t.Fatalf("reuse install: %v", err)
	}
	f.bmc.wg.Wait()
	if got := f.bmc.mutationsFor("System-2"); len(got) != 0 {
		t.Fatalf("the other enrolled target received effects: %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "console/target-b.log")); len(b) != 0 {
		t.Fatal("the other enrolled target booted")
	}
}

func TestDiscoveryFailuresIssueNoEffects(t *testing.T) {
	for name, inject := range map[string]func(*fixture){
		"rejected credential":  func(f *fixture) { f.bmc.rejectAuth = true },
		"unsupported reset":    func(f *fixture) { f.bmc.hideOn = true },
		"renumbered system":    func(f *fixture) { f.bmc.systems["System-1"].uuid = "00000000-0000-0000-0000-000000000000" },
		"swapped system UUIDs": func(f *fixture) { f.bmc.systems["System-1"].uuid, f.bmc.systems["System-2"].uuid = uuidB, uuidA },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, time.Second)
			inject(f)
			q := f.install(1)
			if _, err := f.p.Execute(context.Background(), q); !errors.Is(err, executor.ErrOutcomeUnknown) {
				t.Fatalf("expected Unknown, got %v", err)
			}
			if got := f.bmc.mutations; len(got) != 0 {
				t.Fatalf("effects issued before verified discovery: %v", got)
			}
			if s := f.stage(t, q.ID); s != "admitted" {
				t.Fatalf("stage %s", s)
			}
			if _, found, err := f.p.Lookup(context.Background(), q.ID); found || !errors.Is(err, executor.ErrOutcomeUnknown) {
				t.Fatal("unfinished operation must look up as not found and Unknown")
			}
		})
	}
	f := newFixture(t, time.Second)
	f.bmc.rejectAuth = true
	q := f.install(1)
	f.p.Execute(context.Background(), q)
	f.bmc.rejectAuth = false
	if r, err := f.p.Execute(context.Background(), q); err != nil || r.Result != "installed" {
		t.Fatalf("repaired credential did not resume: %v", err)
	}
}

func TestLostResponseAfterEffectRecoversWithoutRepeat(t *testing.T) {
	f := newFixture(t, 100*time.Millisecond)
	f.bmc.dropOnce = true
	q := f.install(1)
	if _, err := f.p.Execute(context.Background(), q); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("lost response must be Unknown: %v", err)
	}
	if s := f.stage(t, q.ID); s != "installer-requested" {
		t.Fatalf("stage after lost response %s", s)
	}
	if r, err := f.p.Execute(context.Background(), q); err != nil || r.Result != "installed" {
		t.Fatalf("recovery: %v", err)
	}
	f.bmc.wg.Wait()
	if got := f.bmc.systems["System-1"].boots; strings.Join(got, ",") != "Pxe,Hdd" {
		t.Fatalf("lost response caused a repeated boot: %v", got)
	}
	ons := 0
	for _, m := range f.bmc.mutationsFor("System-1") {
		if m == "POST On" {
			ons++
		}
	}
	if ons != 2 {
		t.Fatalf("expected one On per phase, got %d", ons)
	}
}

func TestLostDiskBootResponseRecoversWithoutRepeat(t *testing.T) {
	f := newFixture(t, 100*time.Millisecond)
	f.bmc.dropOnce, f.bmc.dropSkip = true, 1
	q := f.install(1)
	if _, err := f.p.Execute(context.Background(), q); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("lost disk-boot response must be Unknown: %v", err)
	}
	if s := f.stage(t, q.ID); s != "disk-requested" {
		t.Fatalf("stage after lost response %s", s)
	}
	f.bmc.wg.Wait()
	if r, err := f.p.Execute(context.Background(), q); err != nil || r.Result != "installed" {
		t.Fatalf("recovery: %v", err)
	}
	j, _ := f.p.read()
	if got := f.bmc.systems["System-1"].boots; strings.Join(got, ",") != "Pxe,Hdd" || j.Operations[q.ID].PowerOn["disk"] != 1 {
		t.Fatalf("lost disk-boot response caused a repeat: %v %v", got, j.Operations[q.ID].PowerOn)
	}
}

func TestLostRequestIsRepeatedOnlyWhileArmedAndSilent(t *testing.T) {
	f := newFixture(t, time.Second)
	f.bmc.ignoreOnce = true
	q := f.install(1)
	if r, err := f.p.Execute(context.Background(), q); err != nil || r.Result != "installed" {
		t.Fatalf("install: %v", err)
	}
	f.bmc.wg.Wait()
	j, _ := f.p.read()
	if j.Operations[q.ID].PowerOn["installer"] != 2 || strings.Join(f.bmc.systems["System-1"].boots, ",") != "Pxe,Hdd" {
		t.Fatalf("expected one repeated On and one boot: %+v %v", j.Operations[q.ID].PowerOn, f.bmc.systems["System-1"].boots)
	}
}

func TestDuplicateReceiptAndIdentityChangeNeverSucceed(t *testing.T) {
	f := newFixture(t, time.Second)
	q := f.install(1)
	if _, err := f.p.Execute(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	f.bmc.wg.Wait()
	b, _ := os.ReadFile(filepath.Join(f.dir, "console/target-a.log"))
	f.bmc.console(f.bmc.systems["System-1"], strings.Split(string(b), "\n")[0])
	c := cleanup(q)
	j, _ := f.p.read()
	r := j.Operations[q.ID]
	if _, err := f.p.receipts(r, phases["install"][0]); err == nil {
		t.Fatal("duplicate installer receipt accepted")
	}
	f.bmc.systems["System-1"].uuid = "00000000-0000-0000-0000-000000000000"
	if _, err := f.p.Execute(context.Background(), c); !errors.Is(err, executor.ErrOutcomeUnknown) || len(f.bmc.mutationsFor("System-1")) != 5 {
		t.Fatalf("identity change: %v %v", err, f.bmc.mutationsFor("System-1"))
	}
}

func TestChangedRetryConflictsAndConcurrentWorkerIsExcluded(t *testing.T) {
	f := newFixture(t, time.Second)
	f.bmc.rejectAuth = true
	q := f.install(1)
	f.p.Execute(context.Background(), q)
	changed := q
	changed.FenceToken = "other"
	if _, err := f.p.Execute(context.Background(), changed); !errors.Is(err, executor.ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	other := cleanup(f.install(1))
	other.ID = "unrelated"
	if _, err := f.p.Execute(context.Background(), other); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("an unfinished operation must block later ones: %v", err)
	}
	guard, err := f.p.lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Execute(context.Background(), q); err == nil || errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("second worker was not excluded by the journal lock: %v", err)
	}
	guard.Close()
	ro := &Provider{root: f.p.root, readOnly: true}
	if _, err := ro.Execute(context.Background(), q); !errors.Is(err, executor.ErrInvalid) {
		t.Fatal("read-only provider executed")
	}
	bad := q
	bad.AssetID = "fleet-provider/fleet.datumapis.com/Device/not-enrolled"
	if _, err := f.p.Execute(context.Background(), bad); !errors.Is(err, executor.ErrInvalid) {
		t.Fatal("unenrolled asset accepted")
	}
}

func TestEnrollmentIsRetainedOnceWithoutContendingForTheJournalLock(t *testing.T) {
	f := newFixture(t, time.Second)
	guard, err := f.p.lock() // an executing worker holds the journal lock
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.p.retainEnrollment() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent open failed: %v", err)
		}
	}
	changed := *f.p
	changed.enrollment = Enrollment{Endpoint: f.p.enrollment.Endpoint, Targets: f.p.enrollment.Targets[:1]}
	if err := changed.retainEnrollment(); err == nil {
		t.Fatal("a changed enrollment was accepted for the retained journal")
	}
}

func TestBootHandlerScopesByMACCapabilityAndAdmission(t *testing.T) {
	f := newFixture(t, time.Second)
	q := f.install(1)
	j := journal{Provider: "redfish", Image: f.p.image, Epoch: 1, Binding: q.BindingID, Fence: q.FenceToken, Operations: map[string]*record{}}
	digest, _ := simulator.RequestDigest(q)
	target, _ := f.p.enrollment.target(assetA)
	r := &record{Request: q, Receipt: strings.Repeat("a", 64), Stage: "installer-requested", BootToken: strings.Repeat("b", 64), BootExpires: time.Now().Add(time.Minute), Target: target,
		Result: simulator.OperationResult{ID: q.ID, Kind: "install", InputDigest: digest, Phase: "Unknown", CreatedAt: time.Now()}}
	j.Operations[q.ID] = r
	save := func() {
		if err := f.p.save(j); err != nil {
			t.Fatal(err)
		}
	}
	save()
	base := f.p.origin + "/" + key(q.ID)[:16] + "/"
	if s := get(f.p.origin + "/pxe/52-54-00-77-01-0a/boot.ipxe"); !strings.Contains(s, "fleet.mode=install") || !strings.Contains(s, base+r.BootToken+"/kernel") {
		t.Fatalf("admitted MAC not served: %q", s)
	}
	if get(f.p.origin+"/pxe/boot.ipxe") == "" || get(base+r.BootToken+"/kernel") != "kernel" {
		t.Fatal("chain script or admitted artifact not served")
	}
	for _, u := range []string{f.p.origin + "/pxe/52-54-00-77-01-0b/boot.ipxe", base + strings.Repeat("c", 64) + "/kernel", base + r.BootToken + "/SHA256SUMS", base + r.BootToken + "/boot.ipxe"} {
		if get(u) != "" {
			t.Fatalf("served %s", u)
		}
	}
	for _, mutate := range []func(){func() { r.BootClosed = true }, func() { r.BootExpires = time.Now().Add(-time.Second) }, func() { r.Stage = "disk-requested" }, func() { j.Fence = "superseded" }} {
		saved := *r
		fence := j.Fence
		mutate()
		save()
		if get(f.p.origin+"/pxe/52-54-00-77-01-0a/boot.ipxe") != "" || get(base+r.BootToken+"/kernel") != "" {
			t.Fatal("closed, expired, non-boot or fenced admission served")
		}
		*r, j.Fence = saved, fence
	}
	if b, _ := os.ReadFile(filepath.Join(f.p.bootRoot, key(q.ID), "boot-delivery.log")); strings.Contains(string(b), r.BootToken) || strings.Contains(string(b), r.Receipt) {
		t.Fatal("delivery evidence contains a capability or receipt")
	}
}

func TestWorkerRefusesHypervisorConfiguration(t *testing.T) {
	t.Setenv("FLEET_LIBVIRT_URI", "qemu:///system?socket=/run/fleet-libvirt/libvirt-sock")
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "libvirt") {
		t.Fatalf("Open accepted a libvirt URI: %v", err)
	}
	t.Setenv("FLEET_LIBVIRT_URI", "")
	t.Setenv("FLEET_SITE_PROVIDER", "libvirt")
	if _, err := OpenReadOnly(); err == nil {
		t.Fatal("redfish provider opened outside Redfish mode")
	}
}

// The executor package must have no direct hypervisor path: no process
// execution, no libvirt client and no dependency on the lab VM provider.
func TestPackageHasNoHypervisorPath(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "os/exec" || path == "syscall" || strings.Contains(path, "libvirt") || strings.HasSuffix(path, "/internal/localvm") {
				t.Fatalf("%s imports %s", name, path)
			}
		}
		if strings.Contains(string(src), "virsh") || strings.Contains(string(src), "qemu:///") {
			t.Fatalf("%s references a hypervisor control path", name)
		}
	}
}

func TestClientNeverLeaksCredentialOrFollowsForeignPaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redfish/v1/":
			json.NewEncoder(w).Encode(map[string]any{"Systems": map[string]string{"@odata.id": "/redfish/v1/Systems"}})
		case "/redfish/v1/Systems":
			json.NewEncoder(w).Encode(map[string]any{"Members": []any{map[string]string{"@odata.id": "/other/../redfish/v1/Systems/x"}}})
		default:
			http.Redirect(w, r, "http://198.51.100.1/", http.StatusFound)
		}
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "operator", "synthetic-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Discover(context.Background(), Target{SystemID: "x", SystemUUID: uuidA})
	if !errors.Is(err, ErrIdentity) || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte("operator:synthetic-secret"))) {
		t.Fatalf("foreign member path: %v", err)
	}
	if _, err := c.Observe(context.Background(), System{Path: "/redfish/v1/Managers", ID: "x", UUID: uuidA}); !errors.As(err, new(Status)) {
		t.Fatalf("followed a redirect: %v", err)
	}
	for _, bad := range []string{"ftp://10.0.0.1:1", "http://bmc.example:80", "http://10.0.0.1:80/path", "http://user@10.0.0.1:80"} {
		if _, err := NewClient(bad, "u", "p", time.Second); err == nil {
			t.Fatalf("accepted endpoint %s", bad)
		}
	}
}
