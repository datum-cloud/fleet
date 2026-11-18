package milo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.datum.net/fleet/internal/controller"
	fleetnetwork "go.datum.net/fleet/internal/network"
)

// TestLegacyAuthorityRequirementsDecodeForReleaseOnly proves an authority
// record retained before CPU and memory became exact still decodes with its
// original requirements digest, survives the record rewrites release performs,
// and is refused for recovery and allocation.
func TestLegacyAuthorityRequirementsDecodeForReleaseOnly(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, _ := NewRecordStore(fake.NewClientBuilder().WithScheme(scheme).Build(), "private")
	legacyJSON := []byte(`{"cpu":{"architecture":"amd64","cores":{"min":24}},"memory":{"capacity":{"min":"128Gi"}}}`)
	var accepted controller.RetainedRequirements
	if err := json.Unmarshal(legacyJSON, &accepted); err != nil || !accepted.Legacy() {
		t.Fatalf("legacy requirements = %v, %v", accepted.Legacy(), err)
	}
	// The digest a pre-exact manager retained was computed over this JSON.
	sum := sha256.Sum256(legacyJSON)
	request := SourceUseRequest{ConsumerProject: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "consumer-uid"}, ProviderProject: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "provider", UID: "provider-uid"}, NamespaceUID: "namespace-uid", ClaimUID: "claim-uid", LocalClassUID: "local-class", SourceClassUID: "source-class", SourceClassRevision: "r1", SourceClassDigest: "class-digest", LocalProfileUID: "deleted-profile-uid", ProfileKind: ProfileCustom, RequirementsDigest: hex.EncodeToString(sum[:]), PublicationRevision: "publication", Location: fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}}
	receipt, _ := (&testSource{}).AuthorizeSource(ctx, request)
	contract := struct {
		Request SourceUseRequest `json:"request"`
		Receipt SourceUseReceipt `json:"receipt"`
	}{request, receipt}
	contractDigest, _ := digest(contract)
	record := AuthorityRecord{SchemaVersion: authoritySchemaVersion, RecordID: "record-id", State: StatePrepared, ClaimNamespace: "default", ClaimName: "allocation", ClaimUID: request.ClaimUID, SourceRequest: request, SourceReceipt: receipt, ContractDigest: contractDigest, AcceptedRequirements: accepted}
	ref, err := store.Create(ctx, record)
	if err != nil {
		t.Fatalf("legacy authority record rejected: %v", err)
	}
	stored, err := store.Get(ctx, ref)
	if err != nil || !stored.AcceptedRequirements.Legacy() {
		t.Fatalf("legacy authority decode = %#v, %v", stored, err)
	}
	if _, err := store.Update(ctx, ref, func(*AuthorityRecord) error { return nil }); err != nil {
		t.Fatalf("legacy authority rewrite: %v", err)
	}
	if stored, err = store.Get(ctx, ref); err != nil || !stored.AcceptedRequirements.Legacy() {
		t.Fatalf("legacy authority after rewrite = %#v, %v", stored, err)
	}

	f := newAdapterFixture(t)
	if err := validateRecoveredPrepare(stored, f.prepare, request.ConsumerProject, request.ProviderProject); !errors.Is(err, controller.ErrLegacyRecord) {
		t.Fatalf("recovery of legacy authority = %v", err)
	}
	if err := validatePreparedAllocation(stored, PreparedClaim{}, f.allocation); !errors.Is(err, controller.ErrLegacyRecord) {
		t.Fatalf("allocation with legacy authority = %v", err)
	}
}
