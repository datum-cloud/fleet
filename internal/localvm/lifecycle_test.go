package localvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

type vmFixture struct {
	p         *Provider
	creates   []string
	failStart bool
	present   string
}

func newVMFixture(t *testing.T) *vmFixture {
	t.Helper()
	f := &vmFixture{p: &Provider{root: t.TempDir(), image: "local@sha256:approved"}}
	f.p.command = func(_ context.Context, name string, args ...string) (string, error) {
		if name != "virsh" || len(args) < 3 || args[0] != "--connect" || args[1] != "qemu:///system" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		switch args[2] {
		case "list":
			return f.present, nil
		case "create":
			f.creates = append(f.creates, args[3])
			if f.failStart {
				return "", errors.New("lost start response")
			}
			j, err := f.p.read()
			if err != nil {
				return "", err
			}
			for _, r := range j.Operations {
				stage := strings.TrimSuffix(filepath.Base(args[3]), ".xml")
				if args[3] != filepath.Join(f.p.directory(r), stage+".xml") {
					continue
				}
				if r.Stage != stage+"-started" {
					t.Fatalf("command preceded durable uncertainty: %s", r.Stage)
				}
				marker := "FLEET-INSTALLED:"
				if stage == "disk" {
					marker = "FLEET-DISK-BOOT:"
				}
				return "", os.WriteFile(filepath.Join(f.p.directory(r), stage+".log"), []byte(marker+r.Receipt+"\n"), 0600)
			}
			return "", errors.New("command not bound to journal")
		default:
			t.Fatalf("unexpected virsh operation %v", args)
		}
		return "", nil
	}
	return f
}
func vmRequest(id string) simulator.Request {
	return simulator.Request{ID: id, Kind: "install", AssetID: AssetID, BindingID: "allocation", Epoch: 1, FenceToken: "fence", Inputs: json.RawMessage(`{"image":"local@sha256:approved"}`)}
}
func TestLocalReplayAndAuthorityTransitions(t *testing.T) {
	f := newVMFixture(t)
	ctx := context.Background()
	q := vmRequest("install")
	result, err := f.p.Execute(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if result.Result != "installed" || len(f.creates) != 2 {
		t.Fatalf("unexpected install: %+v %v", result, f.creates)
	}
	// Reopen state through a fresh provider without changing the command fixture.
	f.p = &Provider{root: f.p.root, image: f.p.image, command: f.p.command}
	replay, err := f.p.Execute(ctx, q)
	if err != nil || replay.InputDigest != result.InputDigest || len(f.creates) != 2 {
		t.Fatalf("replay caused effects: %v", err)
	}
	for _, change := range []func(*simulator.Request){
		func(r *simulator.Request) { r.AssetID = "another" }, func(r *simulator.Request) { r.BindingID = "another" },
		func(r *simulator.Request) { r.Epoch++ }, func(r *simulator.Request) { r.FenceToken = "another" },
		func(r *simulator.Request) { r.Inputs = json.RawMessage(`{"image":"another"}`) },
	} {
		altered := q
		change(&altered)
		if _, err = f.p.Execute(ctx, altered); err == nil {
			t.Fatal("accepted substituted retained request")
		}
	}
	next := vmRequest("next")
	next.Epoch = 2
	next.BindingID = "next"
	next.FenceToken = "next"
	if _, err = f.p.Execute(ctx, next); !errors.Is(err, executor.ErrFenced) {
		t.Fatalf("advanced before cleanup: %v", err)
	}
	cleanup := q
	cleanup.ID = "cleanup"
	cleanup.Kind = "cleanup"
	cleanup.Inputs = nil
	f.present = domain(&record{Request: q})
	if _, err = f.p.Execute(ctx, cleanup); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("cleaned live domain: %v", err)
	}
	if _, err = os.Stat(f.p.directory(&record{Request: q}) + "/disk.raw"); err != nil {
		t.Fatal("deleted live disk", err)
	}
	f.present = "decoy-domain"
	decoy := filepath.Join(f.p.root, "decoy.raw")
	if err = os.WriteFile(decoy, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.Execute(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(f.p.directory(&record{Request: q}) + "/disk.raw"); !os.IsNotExist(err) {
		t.Fatalf("disk survived cleanup: %v", err)
	}
	if b, err := os.ReadFile(decoy); err != nil || string(b) != "retain" {
		t.Fatal("decoy changed")
	}
	stale := vmRequest("stale")
	if _, err = f.p.Execute(ctx, stale); !errors.Is(err, executor.ErrFenced) {
		t.Fatalf("stale authority accepted: %v", err)
	}
	if _, err = f.p.Execute(ctx, next); err != nil {
		t.Fatal(err)
	}
	count := len(f.creates)
	if _, err = f.p.Execute(ctx, cleanup); err != nil || len(f.creates) != count {
		t.Fatal("old cleanup replay changed current allocation")
	}
	if _, err = os.Stat(f.p.directory(&record{Request: next}) + "/disk.raw"); err != nil {
		t.Fatal("old cleanup deleted new disk", err)
	}
}
func TestLocalDiskDirectorySyncPrecedesPrepared(t *testing.T) {
	f := newVMFixture(t)
	ctx := context.Background()
	q := vmRequest("sync-failure")
	injected := errors.New("directory sync failed")
	f.p.syncDirectory = func(path string) error {
		j, err := f.p.read()
		if err != nil {
			t.Fatal(err)
		}
		if j.Operations[q.ID].Stage != "admitted" {
			t.Fatal("journal advanced before sync")
		}
		info, err := os.Stat(filepath.Join(path, "disk.raw"))
		if err != nil || info.Size() != 2<<30 {
			t.Fatal("disk not prepared before sync", err)
		}
		return injected
	}
	if _, err := f.p.Execute(ctx, q); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("sync failure ignored: %v", err)
	}
	j, err := f.p.read()
	if err != nil {
		t.Fatal(err)
	}
	if j.Operations[q.ID].Stage != "admitted" || len(f.creates) != 0 {
		t.Fatal("sync failure permitted boot")
	}
	path := f.p.directory(j.Operations[q.ID]) + "/disk.raw"
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("do-not-overwrite")); err != nil {
		t.Fatal(err)
	}
	file.Close()
	f.p.syncDirectory = nil
	if _, err = f.p.Execute(ctx, q); !errors.Is(err, executor.ErrOutcomeUnknown) {
		t.Fatalf("overwrote uncertain preparation: %v", err)
	}
	file, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	b := make([]byte, len("do-not-overwrite"))
	if _, err = file.Read(b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "do-not-overwrite" {
		t.Fatalf("disk overwritten: %q", b)
	}
}
func TestLocalUncertainStartRecoversReceiptWithoutReinstall(t *testing.T) {
	for _, bad := range []string{"missing", "wrong-receipt", "wrong-stage"} {
		t.Run(bad, func(t *testing.T) {
			f := newVMFixture(t)
			ctx := context.Background()
			q := vmRequest("interrupted")
			f.failStart = true
			if _, err := f.p.Execute(ctx, q); !errors.Is(err, executor.ErrOutcomeUnknown) {
				t.Fatal(err)
			}
			j, err := f.p.read()
			if err != nil {
				t.Fatal(err)
			}
			r := j.Operations[q.ID]
			if r.Stage != "installer-started" {
				t.Fatal(r.Stage)
			}
			if _, _, err = f.p.Lookup(ctx, q.ID); !errors.Is(err, executor.ErrOutcomeUnknown) {
				t.Fatal("lookup inferred completion")
			}
			log := f.p.directory(r) + "/installer.log"
			content := "FLEET-INSTALLED:stale\n"
			if bad == "wrong-stage" {
				content = "FLEET-DISK-BOOT:" + r.Receipt + "\n"
			}
			if bad != "missing" {
				if err = os.WriteFile(log, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.failStart = false
			if _, err = f.p.Execute(ctx, q); !errors.Is(err, executor.ErrOutcomeUnknown) || len(f.creates) != 1 {
				t.Fatalf("uncertain start retried or bad receipt accepted: %v", err)
			}
			if err = os.WriteFile(log, []byte("FLEET-INSTALLED:"+r.Receipt+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = f.p.Execute(ctx, q); err != nil {
				t.Fatal(err)
			}
			if len(f.creates) != 2 || !strings.HasSuffix(f.creates[1], "/disk.xml") {
				t.Fatalf("installer repeated: %v", f.creates)
			}
		})
	}
}

func TestExplicitAccelerationAndJournalBinding(t *testing.T) {
	for _, value := range []string{"auto", "qemu", "TCG", " kvm"} {
		if _, err := parseAccelerator(value); err == nil {
			t.Fatalf("accepted unknown accelerator %q", value)
		}
	}
	for _, mode := range []string{"kvm", "tcg"} {
		t.Run(mode, func(t *testing.T) {
			f := newVMFixture(t)
			f.p.accelerator = mode
			q := vmRequest("mode-bound")
			expected := "kvm"
			if mode == "tcg" {
				expected = "qemu"
			}
			if !strings.Contains(f.p.domainXML(&record{Request: q}, "disk"), "<domain type='"+expected+"'>") {
				t.Fatal("incorrect domain accelerator")
			}
			if _, err := f.p.Execute(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			j, err := f.p.read()
			if err != nil {
				t.Fatal(err)
			}
			if j.Accelerator != mode {
				t.Fatal("accelerator not retained")
			}
			other := "tcg"
			if mode == "tcg" {
				other = "kvm"
			}
			f.p.accelerator = other
			if _, err = f.p.Execute(context.Background(), q); !errors.Is(err, executor.ErrConflict) {
				t.Fatalf("mode changed on replay: %v", err)
			}
			if _, _, err = f.p.Lookup(context.Background(), q.ID); !errors.Is(err, executor.ErrConflict) {
				t.Fatalf("lookup ignored mode: %v", err)
			}
		})
	}
	if mode, err := parseAccelerator(""); err != nil || mode != "kvm" {
		t.Fatal("default changed")
	}
	f := newVMFixture(t)
	f.p.accelerator = "tcg"
	if err := f.p.checkMode(journal{Image: f.p.image}); !errors.Is(err, executor.ErrConflict) {
		t.Fatal("legacy journal was not bound to KVM")
	}
}
