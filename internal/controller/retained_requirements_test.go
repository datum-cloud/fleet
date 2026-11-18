package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// legacyAssetRecordFixture is a synthetic asset record in the shape written
// before MachineProfile CPU cores and memory capacity became exact values.
const legacyAssetRecordFixture = `{"schemaVersion":1,"state":"Committed","assetID":"provider/fleet.datumapis.com/Device/host-1","machineNamespace":"provider","machineName":"machine-1","machineUID":"machine-uid","claimNamespace":"project","claimName":"claim-legacy","claimUID":"legacy","claimScope":{"authorityID":"local","projectUID":"namespace-uid"},"classUID":"class-uid","profileUID":"deleted-profile-uid","localClassUID":"class-uid","localProfileUID":"deleted-profile-uid","bindingID":"binding-legacy","epoch":1,"fenceToken":"fence-legacy","currentInstallationName":"None","acceptedRequirements":{"cpu":{"architecture":"amd64","cores":{"min":24}},"memory":{"capacity":{"min":"128Gi"}}},"hardware":{"cpu":{"architecture":"amd64","cores":24},"memory":{"capacity":"128Gi"}}}`

func TestRetainedRequirementsDecodesLegacyShapeVerbatim(t *testing.T) {
	var record AssetRecord
	if err := json.Unmarshal([]byte(legacyAssetRecordFixture), &record); err != nil {
		t.Fatalf("legacy fixture does not decode: %v", err)
	}
	if !record.AcceptedRequirements.Legacy() || validateAssetRecord(&record) != nil {
		t.Fatalf("legacy fixture decoded as %#v", record.AcceptedRequirements)
	}
	if _, err := record.AcceptedRequirements.Exact(); !errors.Is(err, ErrLegacyRecord) || !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("legacy Exact() error = %v", err)
	}
	const legacyRequirements = `{"cpu":{"architecture":"amd64","cores":{"min":24}},"memory":{"capacity":{"min":"128Gi"}}}`
	encoded, err := json.Marshal(record)
	if err != nil || !bytes.Contains(encoded, []byte(`"acceptedRequirements":`+legacyRequirements)) {
		t.Fatalf("legacy requirements did not re-encode verbatim: %s (err %v)", encoded, err)
	}
	var roundTrip AssetRecord
	if err := json.Unmarshal(encoded, &roundTrip); err != nil || !sameRecord(&record, &roundTrip) || !roundTrip.AcceptedRequirements.Legacy() {
		t.Fatalf("legacy record round trip = %#v, %v", roundTrip, err)
	}

	for name, raw := range map[string]string{
		"exact constraint": `{"cpu":{"architecture":"amd64","cores":{"exact":24}},"memory":{"capacity":{"exact":"128Gi"}}}`,
		"range":            `{"cpu":{"architecture":"amd64","cores":{"min":16,"max":32}},"memory":{"capacity":{"min":"64Gi","max":"256Gi"}},"disks":[{"name":"root","count":{"min":1},"capacity":{"min":"480Gi"}}]}`,
		"indented":         "{\n  \"cpu\": {\"architecture\": \"amd64\", \"cores\": {\"min\": 24}},\n  \"memory\": {\"capacity\": {\"min\": \"128Gi\"}}\n}",
	} {
		var retained RetainedRequirements
		if err := json.Unmarshal([]byte(raw), &retained); err != nil || !retained.Legacy() {
			t.Fatalf("%s: legacy=%v err=%v", name, retained.Legacy(), err)
		}
		var want bytes.Buffer
		_ = json.Compact(&want, []byte(raw))
		if got, err := json.Marshal(retained); err != nil || !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("%s: re-encoded %s, want %s (err %v)", name, got, want.Bytes(), err)
		}
	}

	for name, raw := range map[string]string{
		"mixed exact cores and legacy memory": `{"cpu":{"architecture":"amd64","cores":24},"memory":{"capacity":{"min":"128Gi"}}}`,
		"mixed legacy cores and exact memory": `{"cpu":{"architecture":"amd64","cores":{"min":24}},"memory":{"capacity":"128Gi"}}`,
		"unknown legacy field":                `{"cpu":{"architecture":"amd64","cores":{"min":24,"preferred":32}},"memory":{"capacity":{"min":"128Gi"}}}`,
		"not an object":                       `"24c-128g"`,
	} {
		var retained RetainedRequirements
		if err := json.Unmarshal([]byte(raw), &retained); err == nil {
			t.Fatalf("%s decoded as %#v", name, retained)
		}
	}

	// Exact requirements keep the public spec encoding byte-for-byte so record
	// comparisons and retained digests are unchanged by the wrapper.
	spec := fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64", Cores: 24}, Memory: fleetv1.MemoryRequirement{Capacity: "128Gi"}}
	wrapped, _ := json.Marshal(ExactRequirements(spec))
	plain, _ := json.Marshal(spec)
	var decoded RetainedRequirements
	if !bytes.Equal(wrapped, plain) || json.Unmarshal(plain, &decoded) != nil || decoded.Legacy() {
		t.Fatalf("exact encoding changed: %s vs %s", wrapped, plain)
	}
	if got, err := decoded.Exact(); err != nil || got.CPU.Cores != 24 || got.Memory.Capacity != "128Gi" {
		t.Fatalf("exact decode = %#v, %v", got, err)
	}
}

// legacyizeClaimRecords rewrites the retained custody proposal and asset record
// of claimUID into the pre-exact requirements shape, and points them at
// profile lifetimes that no longer exist, as an upgraded deployment would see.
func legacyizeClaimRecords(t *testing.T, ctx context.Context, kube client.Client, namespace string, claimUID types.UID) {
	t.Helper()
	var list corev1.SecretList
	if err := kube.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{claimUIDLabel: string(claimUID)}); err != nil {
		t.Fatal(err)
	}
	rewritten := 0
	for i := range list.Items {
		secret := &list.Items[i]
		var record map[string]any
		if err := json.Unmarshal(secret.Data[recordDataKey], &record); err != nil {
			t.Fatal(err)
		}
		switch secret.Labels[recordTypeLabel] {
		case "asset":
			legacyizeAssetJSON(t, record)
		case "custody":
			proposal, ok := record["proposedAsset"].(map[string]any)
			if !ok {
				continue
			}
			legacyizeAssetJSON(t, proposal)
		default:
			continue
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		secret.Data[recordDataKey] = data
		if err := kube.Update(ctx, secret); err != nil {
			t.Fatal(err)
		}
		rewritten++
	}
	if rewritten == 0 {
		t.Fatal("no retained records were rewritten")
	}
}

func legacyizeAssetJSON(t *testing.T, record map[string]any) {
	t.Helper()
	requirements := record["acceptedRequirements"].(map[string]any)
	cpu := requirements["cpu"].(map[string]any)
	memory := requirements["memory"].(map[string]any)
	cpu["cores"] = map[string]any{"min": cpu["cores"]}
	memory["capacity"] = map[string]any{"min": memory["capacity"]}
	record["profileUID"], record["localProfileUID"] = "deleted-profile-uid", "deleted-profile-uid"
}

func TestLegacyRecordBlocksAllocationRetryInstallAndNetwork(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("legacy")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Allocate(ctx, request); err != nil {
		t.Fatal(err)
	}
	legacyizeClaimRecords(t, ctx, f.coordinator.client, "private", request.Claim.UID)
	before := snapshotSecrets(t, ctx, f.coordinator.client)

	// The retry uses a profile whose UID matches the legacy record, so only
	// the legacy shape can be the reason the retained allocation is refused.
	retry := f.request("legacy")
	retry.Profile.UID = "deleted-profile-uid"
	if record, err := f.coordinator.Allocate(ctx, retry); !errors.Is(err, ErrLegacyRecord) || !errors.Is(err, ErrRecordInvalid) || record != nil {
		t.Fatalf("Allocate with legacy custody = %#v, %v", record, err)
	}
	records, err := f.coordinator.recordsForClaim(ctx, request.Claim.UID)
	if err != nil || len(records) != 1 || !records[0].AcceptedRequirements.Legacy() {
		t.Fatalf("legacy asset record = %#v, %v", records, err)
	}
	if err := validateAssetForRequest(records[0], retry); !errors.Is(err, ErrLegacyRecord) {
		t.Fatalf("validateAssetForRequest = %v", err)
	}
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "install-uid"}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	if _, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}); !errors.Is(err, ErrLegacyRecord) {
		t.Fatalf("Install on legacy asset = %v", err)
	}
	f.coordinator.networks = &fakeNetworkLifecycle{}
	if _, err := f.coordinator.ReconcileNetwork(ctx, request.Claim.UID); !errors.Is(err, ErrLegacyRecord) {
		t.Fatalf("ReconcileNetwork on legacy asset = %v", err)
	}
	if len(f.executor.requests) != 0 {
		t.Fatalf("legacy record admitted provider effects: %#v", f.executor.requests)
	}
	if after := snapshotSecrets(t, ctx, f.coordinator.client); !equalSnapshots(before, after) {
		t.Fatal("refusing a legacy record changed retained private records")
	}
}

func TestLegacySelectionIsNotClearedOrReselected(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("selecting")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	// Retain a selection whose asset record was never created, then rewrite
	// it to the legacy shape. Allocation would otherwise clear and reselect it.
	machine := &request.MachineCapacities[0]
	proposal, reusable, err := f.coordinator.proposeAsset(ctx, request, machine)
	if err != nil || !reusable {
		t.Fatalf("proposeAsset = %v, %v", reusable, err)
	}
	if err := f.coordinator.selectCustody(ctx, request, proposal); err != nil {
		t.Fatal(err)
	}
	legacyizeClaimRecords(t, ctx, f.coordinator.client, "private", request.Claim.UID)
	if _, err := f.coordinator.Allocate(ctx, request); !errors.Is(err, ErrLegacyRecord) {
		t.Fatalf("Allocate with legacy selection = %v", err)
	}
	custody, _, err := f.coordinator.getCustody(ctx, request.Claim.UID)
	if err != nil || custody.State != CustodySelecting || custody.ProposedAsset == nil || !custody.ProposedAsset.AcceptedRequirements.Legacy() {
		t.Fatalf("legacy selection was altered: %#v, %v", custody, err)
	}
	if records, err := f.coordinator.recordsForClaim(ctx, request.Claim.UID); err != nil || len(records) != 0 {
		t.Fatalf("legacy selection materialized an asset record: %#v, %v", records, err)
	}

	// Release materializes the exact retained proposal solely to release it.
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || result.State != AssetReleased {
		t.Fatalf("Release of legacy selection = %#v, %v", result, err)
	}
	if len(f.executor.requests) != 0 {
		t.Fatalf("Preparing release admitted provider effects: %#v", f.executor.requests)
	}
}

func TestLegacyRecordWithReplacedProfileReleasesThroughVerifiedCleanup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	legacy, next := f.request("legacy"), f.request("next")
	for _, request := range []AllocationRequest{legacy, next} {
		if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.coordinator.Allocate(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	legacyizeClaimRecords(t, ctx, f.coordinator.client, "private", legacy.Claim.UID)
	// The recreated profile has a new UID; the legacy record still names the
	// deleted one. Capacity stays occupied until verified release.
	next.Profile.UID = "recreated-profile-uid"
	if _, err := f.coordinator.Allocate(ctx, next); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("capacity held by legacy record was reallocated: %v", err)
	}

	// Uncertain cleanup stays unknown and never authorizes finalizer removal.
	f.executor.malform = true
	result, err := f.coordinator.Release(ctx, legacy.Claim.UID)
	if err == nil || result.SafeToRemoveFinalizer || result.State != AssetReleasing {
		t.Fatalf("Release with unverified cleanup = %#v, %v", result, err)
	}
	f.executor.malform = false

	result, err = f.coordinator.Release(ctx, legacy.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || result.State != AssetReleased {
		t.Fatalf("Release of legacy record = %#v, %v", result, err)
	}
	cleanups := 0
	for _, request := range f.executor.requests {
		if request.Kind == "cleanup" {
			cleanups++
		}
	}
	if cleanups != 2 {
		t.Fatalf("cleanup requests = %#v", f.executor.requests)
	}
	custody, _, err := f.coordinator.getCustody(ctx, legacy.Claim.UID)
	if err != nil || custody.State != CustodyReleased {
		t.Fatalf("legacy custody after release = %#v, %v", custody, err)
	}
	if again, err := f.coordinator.Release(ctx, legacy.Claim.UID); err != nil || !again.SafeToRemoveFinalizer {
		t.Fatalf("idempotent release = %#v, %v", again, err)
	}

	// The released legacy record is retained but never blocks reuse: the next
	// exact claim advances the executor epoch over it.
	record, err := f.coordinator.Allocate(ctx, next)
	if err != nil || record.Epoch != 2 || record.ClaimUID != next.Claim.UID || record.AcceptedRequirements.Legacy() {
		t.Fatalf("reallocation after legacy release = %#v, %v", record, err)
	}
}

func TestExactRecordWithReplacedProfileStillReleases(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("exact")
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Allocate(ctx, request); err != nil {
		t.Fatal(err)
	}
	recreated := f.request("exact")
	recreated.Profile.UID = "recreated-profile-uid"
	if _, err := f.coordinator.Allocate(ctx, recreated); !errors.Is(err, ErrRecordInvalid) || errors.Is(err, ErrLegacyRecord) {
		t.Fatalf("Allocate after profile recreation = %v", err)
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.SafeToRemoveFinalizer || len(f.executor.requests) != 1 || f.executor.requests[0].Kind != "cleanup" {
		t.Fatalf("Release after profile recreation = %#v, %v, %#v", result, err, f.executor.requests)
	}
}

func snapshotSecrets(t *testing.T, ctx context.Context, kube client.Client) map[string]string {
	t.Helper()
	var list corev1.SecretList
	if err := kube.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, secret := range list.Items {
		out[secret.Name] = secret.ResourceVersion + "/" + string(secret.Data[recordDataKey])
	}
	return out
}

func equalSnapshots(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
