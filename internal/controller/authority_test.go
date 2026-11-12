package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

func TestExternalAuthorityScopesAssetAndGatesRelease(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("milo")
	consumer := ProjectIdentity{AuthorityID: "milo-a", RouteName: "consumer", UID: "consumer-uid"}
	provider := ProjectIdentity{AuthorityID: "milo-a", RouteName: "provider", UID: "provider-uid"}
	authority := AuthorityRef{Provider: "milo", RecordName: "record", RecordID: "record-id", ContractDigest: "digest"}
	hold := ExternalHoldRef{Provider: "milo", Namespace: request.Claim.Namespace, Name: "quota", UID: "quota-uid"}
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission, consumer); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.RetainClaimAuthority(ctx, request.Claim.UID, consumer, provider, authority); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.RetainAllocationAuthority(ctx, request.Claim.UID, consumer, provider, authority, hold); err != nil {
		t.Fatal(err)
	}
	request.ConsumerProject, request.ProviderProject = consumer, provider
	request.Authority, request.ExternalHold = authority, hold
	asset, err := f.coordinator.Allocate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(asset.AssetID, "milo-a/provider-uid/") {
		t.Fatalf("asset ID %q is not provider lifetime scoped", asset.AssetID)
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || result.State != AssetCleanupVerified || result.SafeToRemoveFinalizer || !result.ExternalSettlementRequired {
		t.Fatalf("release = %#v, %v", result, err)
	}
	wrong := hold
	wrong.UID = "replacement"
	if _, err := f.coordinator.CompleteExternalRelease(ctx, request.Claim.UID, authority, wrong); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("wrong hold error = %v", err)
	}
	result, err = f.coordinator.CompleteExternalRelease(ctx, request.Claim.UID, authority, hold)
	if err != nil || !result.SafeToRemoveFinalizer || result.State != AssetReleased {
		t.Fatalf("completion = %#v, %v", result, err)
	}
}

func TestInstallRejectsCustodyAssetCorruptionBeforeEffects(t *testing.T) {
	mutations := map[string]func(*ClaimCustodyRecord){
		"released": func(record *ClaimCustodyRecord) { record.State = CustodyReleased },
		"unbound": func(record *ClaimCustodyRecord) {
			record.State, record.AssetID, record.ProposedAsset = CustodyUnbound, "", nil
		},
		"wrong asset": func(record *ClaimCustodyRecord) { record.AssetID = "replacement-asset" },
		"authority":   func(record *ClaimCustodyRecord) { record.Authority.RecordID = "replacement-record" },
		"hold":        func(record *ClaimCustodyRecord) { record.ExternalHold.UID = "replacement-hold" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			resolver := &countingResolver{}
			f.coordinator.images = resolver
			request := f.request("claim-uid")
			consumer := ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "consumer-uid"}
			provider := ProjectIdentity{AuthorityID: "milo", RouteName: "provider", UID: "provider-uid"}
			authority := AuthorityRef{Provider: "milo", RecordName: "record", RecordID: "record-id", ContractDigest: "digest"}
			hold := ExternalHoldRef{Provider: "milo", Namespace: request.Claim.Namespace, Name: "quota", UID: "hold-uid"}
			if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission, consumer); err != nil {
				t.Fatal(err)
			}
			if err := f.coordinator.RetainClaimAuthority(ctx, request.Claim.UID, consumer, provider, authority); err != nil {
				t.Fatal(err)
			}
			if err := f.coordinator.RetainAllocationAuthority(ctx, request.Claim.UID, consumer, provider, authority, hold); err != nil {
				t.Fatal(err)
			}
			request.ConsumerProject, request.ProviderProject, request.Authority, request.ExternalHold = consumer, provider, authority, hold
			if _, err := f.coordinator.Allocate(ctx, request); err != nil {
				t.Fatal(err)
			}
			custody, secret, err := f.coordinator.getCustodyScoped(ctx, ProjectClaimScope(consumer), request.Claim.UID)
			if err != nil {
				t.Fatal(err)
			}
			mutate(custody)
			if err := encodeRecord(secret, custody, "custody"); err != nil {
				t.Fatal(err)
			}
			if err := f.coordinator.client.Update(ctx, secret); err != nil {
				t.Fatal(err)
			}
			installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: "installation-uid"}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
			_, err = f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, ClaimScope: ProjectClaimScope(consumer), ConsumerProject: consumer, Admission: installAdmission()})
			if !errors.Is(err, ErrRecordInvalid) || resolver.calls != 0 || len(f.executor.requests) != 0 {
				t.Fatalf("Install error=%v image=%d executor=%d", err, resolver.calls, len(f.executor.requests))
			}
			var secrets corev1.SecretList
			if err := f.coordinator.client.List(ctx, &secrets); err != nil {
				t.Fatal(err)
			}
			for i := range secrets.Items {
				if secrets.Items[i].Labels[recordTypeLabel] == "installation" {
					t.Fatalf("installation operation created: %s", secrets.Items[i].Name)
				}
			}
		})
	}
}

func TestSettledUnboundHoldCanBeClearedButNotSubstituted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	request := f.request("unbound")
	consumer := ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "consumer"}
	provider := ProjectIdentity{AuthorityID: "milo", RouteName: "provider", UID: "provider"}
	authority := AuthorityRef{Provider: "milo", RecordName: "record", RecordID: "id", ContractDigest: "digest"}
	hold := ExternalHoldRef{Provider: "milo", Namespace: "default", Name: "hold", UID: "hold-uid"}
	if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission, consumer); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.RetainClaimAuthority(ctx, request.Claim.UID, consumer, provider, authority); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.RetainAllocationAuthority(ctx, request.Claim.UID, consumer, provider, authority, hold); err != nil {
		t.Fatal(err)
	}
	result, err := f.coordinator.Release(ctx, request.Claim.UID)
	if err != nil || !result.ExternalSettlementRequired || result.SafeToRemoveFinalizer {
		t.Fatalf("release = %#v, %v", result, err)
	}
	wrong := authority
	wrong.RecordID = "other"
	if err := f.coordinator.ClearSettledExternalHold(ctx, request.Claim.UID, wrong, hold); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("wrong authority error = %v", err)
	}
	if err := f.coordinator.ClearSettledExternalHold(ctx, request.Claim.UID, authority, hold); err != nil {
		t.Fatal(err)
	}
}

func TestClaimCustodyIsScopedAcrossProjectAPIsWithEqualUIDs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	left := f.request("left")
	right := f.request("right")
	right.Claim.UID = left.Claim.UID
	leftProject := ProjectIdentity{AuthorityID: "milo", RouteName: "left", UID: "left-project"}
	rightProject := ProjectIdentity{AuthorityID: "milo", RouteName: "right", UID: "right-project"}
	if err := f.coordinator.EnsureClaimCustody(ctx, left.Claim, left.NamespaceUID, left.Admission, leftProject); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.EnsureClaimCustody(ctx, right.Claim, right.NamespaceUID, right.Admission, rightProject); err != nil {
		t.Fatal(err)
	}
	leftRecord, leftSecret, err := f.coordinator.getCustodyScoped(ctx, ProjectClaimScope(leftProject), left.Claim.UID)
	if err != nil {
		t.Fatal(err)
	}
	rightRecord, rightSecret, err := f.coordinator.getCustodyScoped(ctx, ProjectClaimScope(rightProject), right.Claim.UID)
	if err != nil {
		t.Fatal(err)
	}
	if leftSecret.Name == rightSecret.Name || leftRecord.Scope == rightRecord.Scope {
		t.Fatalf("colliding custody: %q %#v / %q %#v", leftSecret.Name, leftRecord.Scope, rightSecret.Name, rightRecord.Scope)
	}
	if _, _, err := f.coordinator.getCustody(ctx, left.Claim.UID); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("unscoped collision was not rejected: %v", err)
	}
}

func TestInstallationsAreScopedAcrossProjectAPIsWithEqualClaimUIDs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	type projectCase struct {
		name     string
		consumer ProjectIdentity
		provider ProjectIdentity
	}
	cases := []projectCase{
		{name: "left", consumer: ProjectIdentity{AuthorityID: "milo", RouteName: "left-consumer", UID: "left-consumer"}, provider: ProjectIdentity{AuthorityID: "milo", RouteName: "left-provider", UID: "left-provider"}},
		{name: "right", consumer: ProjectIdentity{AuthorityID: "milo", RouteName: "right-consumer", UID: "right-consumer"}, provider: ProjectIdentity{AuthorityID: "milo", RouteName: "right-provider", UID: "right-provider"}},
	}
	var operationNames []string
	for _, tc := range cases {
		request := f.request("shared-claim-uid")
		request.NamespaceUID = types.UID(tc.name + "-namespace-uid")
		authority := AuthorityRef{Provider: "milo", RecordName: tc.name + "-record", RecordID: tc.name + "-record-id", ContractDigest: tc.name + "-digest"}
		hold := ExternalHoldRef{Provider: "milo", Namespace: request.Claim.Namespace, Name: tc.name + "-quota", UID: types.UID(tc.name + "-hold-uid")}
		if err := f.coordinator.EnsureClaimCustody(ctx, request.Claim, request.NamespaceUID, request.Admission, tc.consumer); err != nil {
			t.Fatal(err)
		}
		if err := f.coordinator.RetainClaimAuthority(ctx, request.Claim.UID, tc.consumer, tc.provider, authority); err != nil {
			t.Fatal(err)
		}
		if err := f.coordinator.RetainAllocationAuthority(ctx, request.Claim.UID, tc.consumer, tc.provider, authority, hold); err != nil {
			t.Fatal(err)
		}
		request.ConsumerProject, request.ProviderProject, request.Authority, request.ExternalHold = tc.consumer, tc.provider, authority, hold
		if _, err := f.coordinator.Allocate(ctx, request); err != nil {
			t.Fatal(err)
		}
		installation := &fleetv1.Installation{ObjectMeta: metav1.ObjectMeta{Namespace: request.Claim.Namespace, Name: "install", UID: types.UID(tc.name + "-installation")}, Spec: fleetv1.InstallationSpec{ClaimRef: fleetv1.ClaimReference{Name: request.Claim.Name}, ExpectedPredecessor: fleetv1.InstallationPredecessor{Type: "None"}, ImageRef: fleetv1.LocalReference{Name: "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
		result, err := f.coordinator.Install(ctx, InstallationRequest{Claim: request.Claim, Installation: installation, NamespaceUID: request.NamespaceUID, ClaimScope: ProjectClaimScope(tc.consumer), ConsumerProject: tc.consumer, Admission: installAdmission()})
		if err != nil || result.Record.ClaimScope != ProjectClaimScope(tc.consumer) || result.Record.ConsumerProject != tc.consumer {
			t.Fatalf("%s installation = %#v, %v", tc.name, result, err)
		}
		operationNames = append(operationNames, operationRecordName(result.Record.ClaimScope, result.Record.ClaimUID, result.Record.Predecessor))
	}
	if operationNames[0] == operationNames[1] || len(f.executor.requests) != 2 {
		t.Fatalf("operation collision: names=%v requests=%d", operationNames, len(f.executor.requests))
	}
}
