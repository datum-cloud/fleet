package allocation

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/sealed"
	"go.datum.net/fleet/internal/site"
	"go.datum.net/fleet/internal/site/simwork"
)

// TestBootstrapReachesTheServerAndEveryCopyIsDeleted installs with bootstrap
// data through custody and the simulator site. The operation carries only a
// reference and a digest, the server is handed exactly the sealed data, and
// once the operation ends no copy remains in the provider project or at the
// site. A payload a crash left behind, and the loser of a concurrent
// admission, are swept too.
func TestBootstrapReachesTheServerAndEveryCopyIsDeleted(t *testing.T) {
	e := startEnv(t)
	device := e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	installation := e.installation(1, "debian-12", "")
	installation.Spec.BootstrapConfigRef = &fleetv1.LocalReference{Name: "example-bootstrap"}
	payload := sealed.Payload{UserData: "#cloud-config\nhostname: example\n", SSHAuthorizedKeys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleOnly example@example.com\n"}
	withBootstrap := image()
	withBootstrap.Definition.BootstrapFormats = []string{fleetv1.BootstrapNoCloud}

	// Bootstrap data for an image that takes none is refused before
	// anything is written.
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation, Image: image(), Bootstrap: &payload}); err == nil {
		t.Fatal("an image without a bootstrap format accepted bootstrap data")
	}
	e.requirePayloads(0)

	// A crash between the sealed write and the operation leaves a payload
	// that no operation names.
	name := custody.InstallationName(allocation.Name, string(installation.UID))
	orphan, err := sealed.Seal(payload, name, allocation.Name, namespace)
	if err != nil {
		t.Fatal(err)
	}
	e.create(orphan)

	// Two concurrent admissions of the same Installation: one operation.
	var wg sync.WaitGroup
	operations := make([]*ops.DeviceInstallation, 2)
	for i := range operations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			operations[i], err = e.allocator.Install(e.ctx, allocation, Installation{Installation: installation, Image: withBootstrap, Bootstrap: &payload})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	operation := operations[0]
	if operations[1].UID != operation.UID || operation.Spec.Bootstrap == nil {
		t.Fatalf("operations = %v, %v", operations[0].Spec.Bootstrap, operations[1].Spec.Bootstrap)
	}
	reference := *operation.Spec.Bootstrap
	if reference.Ref == orphan.Name {
		t.Fatal("the operation names the payload a crash left behind")
	}
	// Admission is final once the operation exists: a retry after the
	// customer edited the Secret returns the admitted operation and seals
	// nothing new.
	before := e.payloads()
	edited := sealed.Payload{UserData: "#cloud-config\nhostname: edited\n"}
	again, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation, Image: withBootstrap, Bootstrap: &edited})
	if err != nil || again.UID != operation.UID || *again.Spec.Bootstrap != reference || e.payloads() != before {
		t.Fatalf("retry = %v, %v; payloads %d, before %d", again.Spec.Bootstrap, err, e.payloads(), before)
	}

	// A pass that delivers the active operation tells the site to keep its
	// payload. A pass that fails to deliver any Device sweeps nothing.
	_ = e.loop.Pass(e.ctx)
	if kept, _ := e.site.lastSweep(); !slices.Contains(kept, operation.Name) {
		t.Fatalf("the active operation's payload was not kept: %v", kept)
	}
	e.site.mu.Lock()
	e.site.failDeliver = true
	e.site.mu.Unlock()
	_, sweeps := e.site.lastSweep()
	_ = e.loop.Pass(e.ctx)
	if _, after := e.site.lastSweep(); after != sweeps {
		t.Fatal("a pass that failed to deliver swept payloads")
	}
	e.site.mu.Lock()
	e.site.failDeliver = false
	e.site.mu.Unlock()

	e.until("the installation succeeds", func() bool {
		if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
			t.Fatal(err)
		}
		if operation.Status.Phase.Ended() && operation.Status.Phase != ops.PhaseSucceeded {
			t.Fatalf("installation ended %s: %s", operation.Status.Phase, operation.Status.Message)
		}
		return operation.Status.Phase == ops.PhaseSucceeded
	})
	server, err := simwork.ReadServer(e.store, namespace+"/"+device.Name)
	if err != nil || server.Bootstrap != reference.Digest {
		t.Fatalf("the server was handed %q, want %q: %v", server.Bootstrap, reference.Digest, err)
	}
	var evidence ops.InstallationEvidence
	if err := json.Unmarshal(operation.Status.Evidence.Raw, &evidence); err != nil || evidence.InstallerVerifiedBootstrap != reference.Digest {
		t.Fatalf("evidence = %+v, %v", evidence, err)
	}
	// The ended operation's payload, the orphan and the concurrent loser are
	// deleted, and the site kept no copy.
	e.until("no sealed payload remains", func() bool { return e.payloads() == 0 })
	// Once the operation ended, the site is told to keep nothing for it.
	_ = e.loop.Pass(e.ctx)
	if kept, _ := e.site.lastSweep(); slices.Contains(kept, operation.Name) {
		t.Fatalf("an ended operation's payload was kept: %v", kept)
	}
	if records, err := e.store.Records("payload"); err != nil || len(records) != 0 {
		t.Fatalf("site payloads = %d, %v", len(records), err)
	}
	if got, _ := (site.StorePayloads{Store: e.store}).Get(e.ctx, site.Assignment{Device: namespace + "/" + device.Name, Operation: ops.OperationReference{Kind: "DeviceInstallation", Name: operation.Name}, Fence: operation.Status.Admission.Fence}); got != nil {
		t.Fatal("the site still holds the payload")
	}
}

func (e *env) payloads() int {
	var list ops.SealedPayloadList
	if err := e.admin.List(e.ctx, &list, client.InNamespace(namespace)); err != nil {
		e.t.Fatal(err)
	}
	return len(list.Items)
}

func (e *env) requirePayloads(want int) {
	e.t.Helper()
	if got := e.payloads(); got != want {
		e.t.Fatalf("sealed payloads = %d, want %d", got, want)
	}
}
