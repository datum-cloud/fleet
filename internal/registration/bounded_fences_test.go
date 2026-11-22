package registration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func deviceUpdate(t *testing.T, device *inventory.Device) admission.Request {
	t.Helper()
	request := registrationRequest(t, device, "unused")
	request.Resource = metav1.GroupVersionResource{Group: "fleet.datumapis.com", Version: "v1alpha1", Resource: "devices"}
	request.Name = device.Name
	request.Operation = admissionv1.Update
	request.OldObject.Raw, _ = json.Marshal(device)
	changed := device.DeepCopyObject().(*inventory.Device)
	changed.Labels = map[string]string{"observation": "changed"}
	request.Object.Raw, _ = json.Marshal(changed)
	return request
}

func TestCoalescingRetainsPendingAndUnknownVersions(t *testing.T) {
	ctx := context.Background()
	handler, device := fixture(t)
	project, _ := handler.Resolve(ctx, "provider")
	oldRequest := deviceUpdate(t, device)
	for attempt := 0; attempt < 10; attempt++ {
		if response := handler.Handle(ctx, oldRequest); !response.Allowed {
			t.Fatal(response.Result)
		}
	}
	if handler.Handle(ctx, registrationRequest(t, device, "pending-first")).Allowed {
		t.Fatal("pending concurrent updates did not fence current version")
	}
	device.Labels = map[string]string{"committed": "first"}
	if err := project.Client.Update(ctx, device); err != nil {
		t.Fatal(err)
	}
	newRequest := deviceUpdate(t, device)
	if response := handler.Handle(ctx, newRequest); !response.Allowed {
		t.Fatal(response.Result)
	}
	if handler.Handle(ctx, oldRequest).Allowed {
		t.Fatal("late stale admission replaced the current pending fence")
	}
	_, ledger, err := handler.Store.Read(ctx)
	if err != nil || len(ledger.DeviceFences) != 1 || ledger.DeviceFences[0].Version != device.ResourceVersion {
		t.Fatalf("not coalesced at current pending baseline: %+v %v", ledger, err)
	}
	restarted := *handler
	if restarted.Handle(ctx, registrationRequest(t, device, "unknown-second")).Allowed {
		t.Fatal("unknown or rejected write released after restart")
	}
	noop := newRequest
	noop.Object = noop.OldObject
	if !restarted.Handle(ctx, noop).Allowed || restarted.Handle(ctx, registrationRequest(t, device, "after-noop")).Allowed {
		t.Fatal("no-op must neither fail nor release a pending baseline")
	}
	device.Labels["committed"] = "second"
	if err := project.Client.Update(ctx, device); err != nil {
		t.Fatal(err)
	}
	if response := restarted.Handle(ctx, registrationRequest(t, device, "fresh")); !response.Allowed {
		t.Fatal(response.Result)
	}
}

func TestCoalescingLegacyFencesPreservesAliasesAndTombstones(t *testing.T) {
	ctx := context.Background()
	handler, device := fixture(t)
	secret, ledger, _ := handler.Store.Read(ctx)
	ledger.DeviceFences = []DeviceFence{
		{Project: "provider", UID: device.UID, Aliases: aliases(device)},
		{Project: "provider", UID: device.UID, Version: "older"},
		{Project: "provider", UID: device.UID, Version: device.ResourceVersion},
		{Project: "other", UID: device.UID, Version: "unrelated"},
	}
	if err := handler.Store.Write(ctx, secret, ledger); err != nil {
		t.Fatal(err)
	}
	update := deviceUpdate(t, device)
	if response := handler.Handle(ctx, update); !response.Allowed {
		t.Fatal(response.Result)
	}
	_, ledger, _ = handler.Store.Read(ctx)
	if len(ledger.DeviceFences) != 2 || len(ledger.DeviceFences[1].Aliases) != len(aliases(device)) || ledger.DeviceFences[0].Project != "other" {
		t.Fatalf("lost aliases or unrelated fences: %+v", ledger)
	}
	deletion := update
	deletion.Operation = admissionv1.Delete
	if !handler.Handle(ctx, deletion).Allowed || handler.Handle(ctx, update).Allowed {
		t.Fatal("delete must permanently dominate pending update fences")
	}
	create := update
	create.Operation = admissionv1.Create
	recreated := device.DeepCopyObject().(*inventory.Device)
	recreated.UID = "recreated"
	create.Object.Raw, _ = json.Marshal(recreated)
	if handler.Handle(ctx, create).Allowed {
		t.Fatal("never-registered deleted hardware aliases were released")
	}
	_, ledger, _ = handler.Store.Read(ctx)
	if len(ledger.DeviceFences) != 2 || !ledger.DeviceFences[1].Deleted || ledger.DeviceFences[1].Version != "" {
		t.Fatalf("delete did not coalesce into permanent tombstone: %+v", ledger)
	}
}

func TestLedgerExactSizeCap(t *testing.T) {
	ctx := context.Background()
	handler, _ := fixture(t)
	secret, ledger, _ := handler.Store.Read(ctx)
	ledger.DeviceFences = []DeviceFence{{Project: "provider", UID: "device", Aliases: []string{""}}}
	encoded, _ := json.Marshal(ledger)
	ledger.DeviceFences[0].Aliases[0] = strings.Repeat("x", TailBudget-len(encoded))
	if err := handler.Store.Write(ctx, secret, ledger); err != nil {
		t.Fatalf("exact tail budget must fit: %v", err)
	}
	before := secret.ResourceVersion
	ledger.DeviceFences[0].Aliases[0] += "x"
	if err := handler.Store.Write(ctx, secret, ledger); err == nil || !strings.Contains(err.Error(), "ledger is full") {
		t.Fatalf("tail budget plus one must fail closed: %v", err)
	}
	current, _, err := handler.Store.Read(ctx)
	if err != nil || current.ResourceVersion != before || len(current.Data["ledger.json"]) != TailBudget {
		t.Fatal("oversized write changed durable reservations")
	}
}

func TestDryRunCannotReserve(t *testing.T) {
	ctx := context.Background()
	handler, device := fixture(t)
	before, _, _ := handler.Store.Read(ctx)
	for _, request := range []admission.Request{registrationRequest(t, device, "dry"), deviceUpdate(t, device)} {
		dry := true
		request.DryRun = &dry
		if response := handler.Handle(ctx, request); response.Allowed || !strings.Contains(response.Result.Message, "dry-run") {
			t.Fatalf("dry run was not explicitly denied: %+v", response)
		}
	}
	after, ledger, _ := handler.Store.Read(ctx)
	if before.ResourceVersion != after.ResourceVersion || len(ledger.Entries) != 0 || len(ledger.DeviceFences) != 0 {
		t.Fatal("dry-run retained effects")
	}
	project, _ := handler.Resolve(ctx, "provider")
	if err := project.Client.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil || device.ResourceVersion != "1" {
		t.Fatal("dry-run changed Device")
	}
}

type afterDeviceReadClient struct {
	client.Client
	after func()
}

func (api *afterDeviceReadClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	err := api.Client.Get(ctx, key, object, options...)
	if _, device := object.(*inventory.Device); err == nil && device && api.after != nil {
		after := api.after
		api.after = nil
		after()
	}
	return err
}

func TestConcurrentNewerPendingFenceForcesBaselineRecheck(t *testing.T) {
	ctx := context.Background()
	handler, device := fixture(t)
	project, _ := handler.Resolve(ctx, "provider")
	oldRequest := deviceUpdate(t, device)
	interleaved := &afterDeviceReadClient{Client: project.Client}
	interleaved.after = func() {
		device.Labels = map[string]string{"committed": "first"}
		if err := project.Client.Update(ctx, device); err != nil {
			t.Fatal(err)
		}
		if response := handler.Handle(ctx, deviceUpdate(t, device)); !response.Allowed {
			t.Fatal(response.Result)
		}
	}
	handler.Resolve = func(context.Context, string) (Project, error) {
		return Project{Identity: project.Identity, Client: interleaved}, nil
	}
	if handler.Handle(ctx, oldRequest).Allowed {
		t.Fatal("stale admission survived newer pending fence CAS")
	}
	_, ledger, err := handler.Store.Read(ctx)
	if err != nil || len(ledger.DeviceFences) != 1 || ledger.DeviceFences[0].Version != device.ResourceVersion {
		t.Fatalf("newer pending fence lost: %+v %v", ledger, err)
	}
	if handler.Handle(ctx, registrationRequest(t, device, "still-pending")).Allowed {
		t.Fatal("newer pending version became registrable")
	}
}
