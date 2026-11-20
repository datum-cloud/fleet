package localvm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

func reviewBootRecord(t *testing.T) (*Provider, journal, *record) {
	t.Helper()
	p := newVMFixture(t).p
	p.bootMode = "pxe"
	q := vmRequest("independent-review")
	digest, err := simulator.RequestDigest(q)
	if err != nil {
		t.Fatal(err)
	}
	r := &record{Request: q, Result: simulator.OperationResult{ID: q.ID, Kind: q.Kind, InputDigest: digest, Phase: "Unknown"}, Receipt: strings.Repeat("a", 64), Stage: "installer-started", BootToken: strings.Repeat("b", 64), BootExpires: time.Now().Add(time.Minute)}
	j := journal{BootMode: "pxe", Accelerator: p.mode(), Image: p.image, Epoch: q.Epoch, Binding: q.BindingID, Fence: q.FenceToken, Operations: map[string]*record{q.ID: r}}
	if err := os.Mkdir(p.directory(r), 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.save(j); err != nil {
		t.Fatal(err)
	}
	return p, j, r
}

type reviewFailedResponse struct {
	header http.Header
	writes int
	short  bool
}

func (w *reviewFailedResponse) Header() http.Header { return w.header }
func (w *reviewFailedResponse) WriteHeader(int)     {}
func (w *reviewFailedResponse) Write(data []byte) (int, error) {
	w.writes++
	if w.short {
		return len(data) - 1, nil
	}
	return 0, errors.New("client disconnected before body")
}

func TestReviewIncompleteHTTPWriteCannotRecordCompletion(t *testing.T) {
	for _, short := range []bool{false, true} {
		p, _, r := reviewBootRecord(t)
		w := &reviewFailedResponse{header: make(http.Header), short: short}
		p.BootHandler().ServeHTTP(w, httptest.NewRequest("GET", "/"+key(r.Request.ID)[:16]+"/"+r.BootToken+"/boot.ipxe", nil))
		if w.writes != 1 {
			t.Fatal("did not exercise body write")
		}
		b, err := os.ReadFile(p.directory(r) + "/boot-delivery.log")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "admitted boot.ipxe bytes=") || strings.Contains(string(b), "write-complete") {
			t.Fatal("incomplete write counted as completion")
		}
		if err := p.verifyNetworkBoot(context.Background(), r); err == nil {
			t.Fatal("attempt counted as network boot evidence")
		}
	}
}
func TestReviewCompleteHTTPWriteRecordsCompletion(t *testing.T) {
	p, _, r := reviewBootRecord(t)
	w := httptest.NewRecorder()
	p.BootHandler().ServeHTTP(w, httptest.NewRequest("GET", "/"+key(r.Request.ID)[:16]+"/"+r.BootToken+"/boot.ipxe", nil))
	b, err := os.ReadFile(p.directory(r) + "/boot-delivery.log")
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !strings.Contains(string(b), "write-complete boot.ipxe bytes=") {
		t.Fatal("successful write not recorded")
	}
}

func TestReviewCorruptDigestAndHTTPPathVariantsDenied(t *testing.T) {
	p, j, r := reviewBootRecord(t)
	base := "/" + key(r.Request.ID)[:16] + "/" + r.BootToken
	for _, path := range []string{base + "/kernel/extra", base + "/%2e%2e", base + "/boot.ipxe?token=x", base + "/%2fkernel"} {
		w := httptest.NewRecorder()
		p.BootHandler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("path variant allowed: status %d", w.Code)
		}
	}
	r.Result.InputDigest = "substituted"
	if err := p.save(j); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	p.BootHandler().ServeHTTP(w, httptest.NewRequest("GET", base+"/boot.ipxe", nil))
	if w.Code != http.StatusForbidden {
		t.Fatal("corrupt request digest admitted")
	}
}

func TestReviewClosedAdmissionDoesNotSettleLiveInstaller(t *testing.T) {
	p, j, r := reviewBootRecord(t)
	r.BootClosed = true
	r.BootExpires = time.Now().Add(-time.Minute)
	if err := p.save(j); err != nil {
		t.Fatal(err)
	}
	// Even an exact receipt cannot settle a domain still present.
	if err := os.WriteFile(p.directory(r)+"/installer.log", []byte("FLEET-INSTALLED:"+r.Receipt+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.command = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[2] != "list" {
			t.Fatalf("unexpected effect %s", args[2])
		}
		return domain(r), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Execute(ctx, r.Request); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("live expired installer settled: %v", err)
	}
	got, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Operations[r.Request.ID].Stage != "installer-started" {
		t.Fatal("advanced live installer")
	}
}

func TestReviewNetworkTeardownFailureBlocksDiskBootAndRecovers(t *testing.T) {
	p, j, r := reviewBootRecord(t)
	r.Stage = "installer-verified"
	r.BootClosed = true
	if err := p.save(j); err != nil {
		t.Fatal(err)
	}
	fail := true
	creates := 0
	p.command = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[2] {
		case "net-list":
			return "fleet-pxe", nil
		case "net-destroy":
			if fail {
				return "", errors.New("uncertain teardown")
			}
			return "", nil
		case "create":
			creates++
			if !strings.HasSuffix(args[3], "/disk.xml") {
				t.Fatal("installer replay")
			}
			return "", os.WriteFile(p.directory(r)+"/disk.log", []byte("FLEET-DISK-BOOT:"+r.Receipt+"\n"), 0600)
		case "list":
			return "", nil
		default:
			t.Fatalf("unexpected command %s", args[2])
			return "", nil
		}
	}
	if _, err := p.Execute(context.Background(), r.Request); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("teardown failure not unknown: %v", err)
	}
	if creates != 0 {
		t.Fatal("booted despite uncertain teardown")
	}
	got, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Operations[r.Request.ID].Stage != "installer-verified" {
		t.Fatal("advanced after failure")
	}
	fail = false
	if _, err := p.Execute(context.Background(), r.Request); err != nil {
		t.Fatal(err)
	}
	if creates != 1 {
		t.Fatal("unexpected disk boot count")
	}
}
