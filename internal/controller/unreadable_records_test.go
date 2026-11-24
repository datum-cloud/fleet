package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// withSecondMachine adds machine-2 after the fixture's machine-1, so an
// allocation that cannot use machine-1 has somewhere else to go.
func (f *fixture) withSecondMachine(request AllocationRequest) AllocationRequest {
	second := request.MachineCapacities[0]
	second.Name, second.UID, second.Spec.DeviceRef.Name = "machine-2", "machine-2-uid", "host-2"
	request.MachineCapacities = append(request.MachineCapacities, second)
	request.EvidenceObservedAt[second.UID] = f.now
	return request
}

func (f *fixture) allocate(t *testing.T, request AllocationRequest) *AssetRecord {
	t.Helper()
	ctx := context.Background()
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	record, err := f.coordinator.Allocate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// writeOlderAssetShape rewrites an asset record's accepted requirements into
// the pre-exact minimum and maximum CPU and memory shape, which no longer
// decodes. It returns the stored bytes so callers can prove nothing rewrote
// them.
func (f *fixture) writeOlderAssetShape(t *testing.T, assetID string, mutateLabels func(map[string]string)) []byte {
	t.Helper()
	ctx := context.Background()
	var secret corev1.Secret
	if err := f.coordinator.client.Get(ctx, client.ObjectKey{Namespace: "private", Name: assetRecordName(assetID)}, &secret); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(secret.Data[recordDataKey], &fields); err != nil {
		t.Fatal(err)
	}
	fields["acceptedRequirements"] = json.RawMessage(`{"cpu":{"architecture":"amd64","cores":{"min":4}},"memory":{"capacity":{"min":"16Gi"}}}`)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	secret.Data[recordDataKey] = data
	if mutateLabels != nil {
		mutateLabels(secret.Labels)
	}
	if err := f.coordinator.client.Update(ctx, &secret); err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *fixture) storedAsset(t *testing.T, assetID string) []byte {
	t.Helper()
	var secret corev1.Secret
	if err := f.coordinator.client.Get(context.Background(), client.ObjectKey{Namespace: "private", Name: assetRecordName(assetID)}, &secret); err != nil {
		t.Fatal(err)
	}
	return secret.Data[recordDataKey]
}

func installRequestFor(request AllocationRequest, name string) InstallationRequest {
	installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: name, UID: types.UID(name + "-uid")}, Spec: fleetv1.InstallationSpec{MachineRef: fleetv1.MachineReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	return InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, Admission: installAdmission()}
}

func undecodableCount(t *testing.T, recordType string) float64 {
	t.Helper()
	var metric dto.Metric
	if err := undecodablePrivateRecords.WithLabelValues(recordType).Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

// An older-shape asset record must not stop the coordinator from serving
// other claims, and must keep its own claim blocked without being rewritten.
func TestUndecodableAssetRecordBlocksOnlyItsOwnClaim(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, f *fixture, other AllocationRequest)
	}{
		{name: "allocate", run: func(t *testing.T, f *fixture, other AllocationRequest) {
			record := f.allocate(t, other)
			if record.MachineName != "machine-2" || record.State != AssetCommitted || record.Epoch != 1 {
				t.Fatalf("other claim allocation = %#v", record)
			}
		}},
		{name: "install", run: func(t *testing.T, f *fixture, other AllocationRequest) {
			f.allocate(t, other)
			result, err := f.coordinator.Install(context.Background(), installRequestFor(other, "other-install"))
			if err != nil || result.Revision == "" {
				t.Fatalf("other claim install = %#v, %v", result, err)
			}
		}},
		{name: "release", run: func(t *testing.T, f *fixture, other AllocationRequest) {
			f.allocate(t, other)
			released, err := f.coordinator.Release(context.Background(), other.Claim.UID)
			if err != nil || !released.SafeToRemoveFinalizer || released.State != AssetReleased {
				t.Fatalf("other claim release = %#v, %v", released, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			stale, other := f.withSecondMachine(f.request("stale")), f.withSecondMachine(f.request("other"))
			asset := f.allocate(t, stale)
			if asset.MachineName != "machine-1" {
				t.Fatalf("stale claim allocated %s, want machine-1", asset.MachineName)
			}
			stored := f.writeOlderAssetShape(t, asset.AssetID, nil)

			tc.run(t, f, other)

			if _, err := f.coordinator.Allocate(ctx, stale); !errors.Is(err, ErrRecordUnreadable) {
				t.Fatalf("stale allocate error = %v, want ErrRecordUnreadable", err)
			}
			if _, err := f.coordinator.Install(ctx, installRequestFor(stale, "stale-install")); !errors.Is(err, ErrRecordUnreadable) {
				t.Fatalf("stale install error = %v, want ErrRecordUnreadable", err)
			}
			if released, err := f.coordinator.Release(ctx, stale.Claim.UID); !errors.Is(err, ErrRecordUnreadable) || released.SafeToRemoveFinalizer {
				t.Fatalf("stale release = %#v, %v, want ErrRecordUnreadable", released, err)
			}
			if got := f.storedAsset(t, asset.AssetID); !bytes.Equal(got, stored) {
				t.Fatalf("undecodable asset record was rewritten: %s", got)
			}
			for _, request := range f.executor.requests {
				if request.AssetID == asset.AssetID {
					t.Fatalf("executor received work for the undecodable asset: %#v", request)
				}
			}
		})
	}
}

// The claim-uid label attributes an undecodable record. Without it the record
// may belong to any claim, so every claim-scoped scan fails closed.
func TestUndecodableRecordAttribution(t *testing.T) {
	cases := []struct {
		name        string
		labels      func(map[string]string)
		otherBlocks bool
	}{
		{name: "labeled for its claim", otherBlocks: false},
		{name: "label names a different claim", labels: func(l map[string]string) { l[claimUIDLabel] = "someone-else" }, otherBlocks: false},
		{name: "label missing", labels: func(l map[string]string) { delete(l, claimUIDLabel) }, otherBlocks: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			stale := f.request("stale")
			asset := f.allocate(t, stale)
			f.writeOlderAssetShape(t, asset.AssetID, tc.labels)
			_, err := f.coordinator.recordsForClaimScoped(ctx, "other", ProjectIdentity{})
			if blocked := errors.Is(err, ErrRecordUnreadable); blocked != tc.otherBlocks {
				t.Fatalf("asset scan for another claim error = %v, want blocked=%v", err, tc.otherBlocks)
			}
			if tc.labels == nil {
				if _, err := f.coordinator.recordsForClaimScoped(ctx, stale.Claim.UID, ProjectIdentity{}); !errors.Is(err, ErrRecordUnreadable) {
					t.Fatalf("asset scan for its own claim error = %v, want ErrRecordUnreadable", err)
				}
			}

			// The same rule applies to installation records scanned by binding.
			operation := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "private", Name: operationPrefix + "older", Labels: map[string]string{recordTypeLabel: "installation", claimUIDLabel: string(stale.Claim.UID)}}, Type: corev1.SecretType("fleet.datumapis.com/private-lifecycle"), Data: map[string][]byte{recordDataKey: []byte(`{"schemaVersion":1}`)}}
			if tc.labels != nil {
				tc.labels(operation.Labels)
			}
			if err := f.coordinator.client.Create(ctx, operation); err != nil {
				t.Fatal(err)
			}
			_, err = f.coordinator.operationsForBinding(ctx, "other", "other-binding")
			if blocked := errors.Is(err, ErrRecordUnreadable); blocked != tc.otherBlocks {
				t.Fatalf("operation scan for another claim error = %v, want blocked=%v", err, tc.otherBlocks)
			}
			if tc.labels == nil {
				if _, err := f.coordinator.operationsForBinding(ctx, stale.Claim.UID, asset.BindingID); !errors.Is(err, ErrRecordUnreadable) {
					t.Fatalf("operation scan for its own claim error = %v, want ErrRecordUnreadable", err)
				}
			}
		})
	}
}

// A Released asset record is retained so the next binding advances the
// executor epoch. If it cannot be decoded, its epoch is unknown, so the asset
// is never proposed again: a new binding at an earlier epoch could otherwise
// sit below a stale operation's fence.
func TestUndecodableReleasedRecordKeepsTheEpochFence(t *testing.T) {
	cases := []struct {
		name          string
		secondMachine bool
		wantErr       error
	}{
		{name: "only the fenced asset", wantErr: ErrInsufficientCapacity},
		{name: "another asset available", secondMachine: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			first, next := f.request("first"), f.request("next")
			if tc.secondMachine {
				first, next = f.withSecondMachine(first), f.withSecondMachine(next)
			}
			asset := f.allocate(t, first)
			if released, err := f.coordinator.Release(ctx, first.Claim.UID); err != nil || !released.SafeToRemoveFinalizer {
				t.Fatalf("release = %#v, %v", released, err)
			}
			stored := f.writeOlderAssetShape(t, asset.AssetID, nil)
			executed := len(f.executor.requests)

			if err := f.coordinator.EnsureClaimCustody(ctx, next.Claim, next.NamespaceUID, next.Admission); err != nil {
				t.Fatal(err)
			}
			record, err := f.coordinator.Allocate(ctx, next)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("allocate error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil || record.AssetID == asset.AssetID || record.Epoch != 1 {
				t.Fatalf("allocate = %#v, %v, want the other asset at epoch 1", record, err)
			}
			custody, _, err := f.coordinator.getCustody(ctx, next.Claim.UID)
			if err != nil || custody.AssetID == asset.AssetID || custody.ProposedAsset != nil && custody.ProposedAsset.AssetID == asset.AssetID {
				t.Fatalf("custody selected the fenced asset: %#v, %v", custody, err)
			}
			// The released claim's retained operations cannot run again.
			if _, err := f.coordinator.Install(ctx, installRequestFor(first, "stale-install")); !errors.Is(err, ErrRecordInvalid) {
				t.Fatalf("stale install error = %v, want ErrRecordInvalid", err)
			}
			if got := f.storedAsset(t, asset.AssetID); !bytes.Equal(got, stored) {
				t.Fatalf("undecodable Released record was rewritten: %s", got)
			}
			for _, request := range f.executor.requests[executed:] {
				if request.AssetID == asset.AssetID {
					t.Fatalf("executor received work for the fenced asset: %#v", request)
				}
			}
		})
	}
}

func TestUndecodableRecordsAreCountedOncePerCall(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	stale, other := f.withSecondMachine(f.request("stale")), f.withSecondMachine(f.request("other"))
	asset := f.allocate(t, stale)
	f.writeOlderAssetShape(t, asset.AssetID, nil)
	if err := f.coordinator.EnsureClaimCustody(ctx, other.Claim, other.NamespaceUID, other.Admission); err != nil {
		t.Fatal(err)
	}

	before := undecodableCount(t, "asset")
	// The record is seen by the claim scan and again as candidate machine-1.
	if _, err := f.coordinator.Allocate(ctx, other); err != nil {
		t.Fatal(err)
	}
	if got := undecodableCount(t, "asset") - before; got != 1 {
		t.Fatalf("asset counter advanced by %v in one call, want 1", got)
	}
	if _, err := f.coordinator.Allocate(ctx, stale); !errors.Is(err, ErrRecordUnreadable) {
		t.Fatalf("stale allocate error = %v", err)
	}
	if got := undecodableCount(t, "asset") - before; got != 2 {
		t.Fatalf("asset counter advanced by %v after two calls, want 2", got)
	}
}

// selectingOn leaves request's claim in Selecting custody with a proposal for
// the asset another claim already holds, as when two claims race for the
// same free asset and the other claim's exclusive create wins.
func (f *fixture) selectingOn(t *testing.T, request AllocationRequest, held *AssetRecord) {
	t.Helper()
	ctx := context.Background()
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission); err != nil {
		t.Fatal(err)
	}
	binding, fence, err := f.coordinator.authorityIDs()
	if err != nil {
		t.Fatal(err)
	}
	proposal := newAssetRecord(request, &request.MachineCapacities[0], held.AssetID, held.Epoch, binding, fence)
	if err := f.coordinator.selectCustody(ctx, request, proposal); err != nil {
		t.Fatal(err)
	}
}

// A claim releasing from Selecting reads its proposed asset's record by name.
// An undecodable record labeled for another claim is that claim's binding, so
// the releasing claim never held the asset: it releases without touching the
// record. A record labeled for the releasing claim, or with no label, might be
// its binding, so release fails closed and keeps the finalizer.
func TestReleaseFromSelectionAttributesAnUndecodableProposedAsset(t *testing.T) {
	cases := []struct {
		name     string
		labels   func(releasing types.UID) func(map[string]string)
		released bool
	}{
		{name: "labeled for another claim", released: true},
		{name: "labeled for the releasing claim", labels: func(releasing types.UID) func(map[string]string) {
			return func(l map[string]string) { l[claimUIDLabel] = string(releasing) }
		}},
		{name: "label missing", labels: func(types.UID) func(map[string]string) {
			return func(l map[string]string) { delete(l, claimUIDLabel) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			holder, releasing := f.request("holder"), f.request("releasing")
			held := f.allocate(t, holder)
			f.selectingOn(t, releasing, held)
			var labels func(map[string]string)
			if tc.labels != nil {
				labels = tc.labels(releasing.Claim.UID)
			}
			stored := f.writeOlderAssetShape(t, held.AssetID, labels)
			executed := len(f.executor.requests)

			result, err := f.coordinator.Release(ctx, releasing.Claim.UID)
			custody, _, custodyErr := f.coordinator.getCustody(ctx, releasing.Claim.UID)
			if custodyErr != nil {
				t.Fatal(custodyErr)
			}
			if tc.released {
				if err != nil || !result.SafeToRemoveFinalizer || custody.State != CustodyReleased {
					t.Fatalf("release = %#v, %v, custody %s; want released", result, err, custody.State)
				}
			} else if !errors.Is(err, ErrRecordUnreadable) || result.SafeToRemoveFinalizer || custody.State != CustodySelecting {
				t.Fatalf("release = %#v, %v, custody %s; want ErrRecordUnreadable with custody kept", result, err, custody.State)
			}
			if !tc.released {
				// The claim's own asset scan stops Release first, so check the
				// selection path directly as well.
				if err := f.coordinator.materializeSelectionForRelease(ctx, custody); !errors.Is(err, ErrRecordUnreadable) {
					t.Fatalf("materialize selection error = %v, want ErrRecordUnreadable", err)
				}
			}
			if got := f.storedAsset(t, held.AssetID); !bytes.Equal(got, stored) {
				t.Fatalf("undecodable proposed asset record was rewritten: %s", got)
			}
			if len(f.executor.requests) != executed {
				t.Fatalf("release sent executor work: %#v", f.executor.requests[executed:])
			}
			// The holder's binding is still fenced by its undecodable record.
			if tc.released {
				if released, err := f.coordinator.Release(ctx, holder.Claim.UID); !errors.Is(err, ErrRecordUnreadable) || released.SafeToRemoveFinalizer {
					t.Fatalf("holder release = %#v, %v, want ErrRecordUnreadable", released, err)
				}
			}
		})
	}
}

// Every lifecycle read that decodes a private record by name reports a record
// that cannot be decoded, not only the scans across claims.
func TestUndecodableRecordsAreReportedOnEveryDecodePath(t *testing.T) {
	undecodable := func(name, recordType string, claimUID types.UID) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "private", Name: name, Labels: map[string]string{recordTypeLabel: recordType, claimUIDLabel: string(claimUID)}}, Type: corev1.SecretType("fleet.datumapis.com/private-lifecycle"), Data: map[string][]byte{recordDataKey: []byte(`{"schemaVersion":1}`)}}
	}
	cases := []struct {
		name       string
		recordType string
		run        func(t *testing.T, f *fixture) error
		// released is set where the record belongs to another claim, so the
		// call succeeds after reporting it.
		released bool
	}{
		{name: "EnsureClaimCustody", recordType: "custody", run: func(t *testing.T, f *fixture) error {
			request := f.request("claim")
			name := custodyRecordName(LocalClaimScope(request.NamespaceUID), request.Claim.UID)
			if err := f.coordinator.client.Create(context.Background(), undecodable(name, "custody", request.Claim.UID)); err != nil {
				t.Fatal(err)
			}
			return f.coordinator.EnsureClaimCustody(context.Background(), request.Claim, request.NamespaceUID, request.Admission)
		}},
		{name: "getOrCreateOperation", recordType: "installation", run: func(t *testing.T, f *fixture) error {
			if err := f.coordinator.client.Create(context.Background(), undecodable(operationPrefix+"existing", "installation", "claim")); err != nil {
				t.Fatal(err)
			}
			_, _, err := f.coordinator.getOrCreateOperation(context.Background(), operationPrefix+"existing", InstallationRequest{}, &AssetRecord{}, "None", ResolvedImage{})
			return err
		}},
		{name: "markOperationSucceeded", recordType: "installation", run: func(t *testing.T, f *fixture) error {
			if err := f.coordinator.client.Create(context.Background(), undecodable(operationPrefix+"running", "installation", "claim")); err != nil {
				t.Fatal(err)
			}
			_, err := f.coordinator.markOperationSucceeded(context.Background(), operationPrefix+"running", &InstallationRecord{})
			return err
		}},
		{name: "materializeSelectionForRelease", recordType: "asset", released: true, run: func(t *testing.T, f *fixture) error {
			holder, releasing := f.request("holder"), f.request("releasing")
			held := f.allocate(t, holder)
			f.selectingOn(t, releasing, held)
			f.writeOlderAssetShape(t, held.AssetID, nil)
			custody, _, err := f.coordinator.getCustody(context.Background(), releasing.Claim.UID)
			if err != nil {
				t.Fatal(err)
			}
			return f.coordinator.materializeSelectionForRelease(context.Background(), custody)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			before := undecodableCount(t, tc.recordType)
			err := tc.run(t, f)
			if tc.released && err != nil || !tc.released && !errors.Is(err, ErrRecordUnreadable) {
				t.Fatalf("error = %v, want released=%v", err, tc.released)
			}
			if got := undecodableCount(t, tc.recordType) - before; got != 1 {
				t.Fatalf("%s counter advanced by %v, want 1", tc.recordType, got)
			}
		})
	}
}

// A claim whose own custody record cannot be decoded is not allocated or
// released through it, and both calls report the record as unreadable so
// callers stop retrying.
func TestUndecodableOwnCustodyBlocksAllocateAndRelease(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("a")
	f.allocate(t, request)
	_, secret, err := f.coordinator.getCustody(ctx, request.Claim.UID)
	if err != nil {
		t.Fatal(err)
	}
	secret.Data[recordDataKey] = []byte(`{"schemaVersion":99}`)
	if err := f.coordinator.client.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.Release(ctx, request.Claim.UID); !errors.Is(err, ErrRecordUnreadable) {
		t.Fatalf("release error = %v, want ErrRecordUnreadable", err)
	}
	if _, err := f.coordinator.Allocate(ctx, request); !errors.Is(err, ErrRecordUnreadable) {
		t.Fatalf("allocate error = %v, want ErrRecordUnreadable", err)
	}
}
