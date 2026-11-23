package milo

import (
	"context"
	"errors"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	fleetnetwork "go.datum.net/fleet/internal/network"
)

func TestSourceReceiptRejectsEverySubstitution(t *testing.T) {
	request := SourceUseRequest{ConsumerProject: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "consumer-uid"}, ProviderProject: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "provider", UID: "provider-uid"}, NamespaceUID: "namespace-uid", ClaimUID: "claim-uid", LocalClassUID: "local-class", SourceClassUID: "source-class", SourceClassRevision: "r1", SourceClassDigest: "class-digest", LocalProfileUID: "local-profile", SourceProfileUID: "source-profile", ProfileKind: ProfilePublished, RequirementsDigest: "requirements", PublicationRevision: "publication", Location: fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}}
	receipt, err := (&testSource{}).AuthorizeSource(context.Background(), request)
	if err != nil || validateReceipt(request, receipt) != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*SourceUseRequest){"consumer lifetime": func(r *SourceUseRequest) { r.ConsumerProject.UID = "replacement" }, "namespace": func(r *SourceUseRequest) { r.NamespaceUID = "replacement" }, "class": func(r *SourceUseRequest) { r.SourceClassUID = "replacement" }, "profile": func(r *SourceUseRequest) { r.SourceProfileUID = "replacement" }, "requirements": func(r *SourceUseRequest) { r.RequirementsDigest = "replacement" }, "publication": func(r *SourceUseRequest) { r.PublicationRevision = "replacement" }, "location": func(r *SourceUseRequest) { r.Location.UID = "replacement" }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if validateReceipt(changed, receipt) == nil {
				t.Fatal("substituted request accepted")
			}
		})
	}
	bad := receipt
	bad.PrincipalUID = ""
	if validateReceipt(request, bad) == nil {
		t.Fatal("receipt without retained principal accepted")
	}
}

func TestAuthorityRecordConcurrentCreateIsExactAndPrivate(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	store, _ := NewRecordStore(kube, "private")
	accepted := fleetv1.MachineProfileSpec{}
	requirements, _ := digest(accepted)
	request := SourceUseRequest{ConsumerProject: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "consumer", UID: "consumer-uid"}, ProviderProject: controller.ProjectIdentity{AuthorityID: "milo", RouteName: "provider", UID: "provider-uid"}, NamespaceUID: "namespace-uid", ClaimUID: "claim-uid", LocalClassUID: "local-class", SourceClassUID: "source-class", SourceClassRevision: "r1", SourceClassDigest: "class-digest", LocalProfileUID: "local-profile", ProfileKind: ProfileCustom, RequirementsDigest: requirements, PublicationRevision: "publication", Location: fleetnetwork.LocationAuthority{Name: "us-central-1", UID: "location-uid", Revision: "location-r1"}}
	receipt, _ := (&testSource{}).AuthorizeSource(context.Background(), request)
	contract := struct {
		Request SourceUseRequest `json:"request"`
		Receipt SourceUseReceipt `json:"receipt"`
	}{request, receipt}
	contractDigest, _ := digest(contract)
	record := AuthorityRecord{SchemaVersion: authoritySchemaVersion, RecordID: "record-id", State: StatePrepared, ClaimNamespace: "default", ClaimName: "allocation", ClaimUID: request.ClaimUID, SourceRequest: request, SourceReceipt: receipt, ContractDigest: contractDigest, AcceptedRequirements: accepted}
	mismatched := record
	mismatched.ClaimUID = "other-claim"
	if !errors.Is(validateAuthorityRecord(&mismatched), ErrAuthorityInvalid) {
		t.Fatal("outer claim lifetime diverged from retained source request")
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.Create(context.Background(), record); errorsCh <- err }()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var secrets corev1.SecretList
	if err := kube.List(context.Background(), &secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 || secrets.Items[0].Type != authorityType {
		t.Fatalf("records = %#v", secrets.Items)
	}
	if string(secrets.Items[0].Data[authorityDataKey]) == "" {
		t.Fatal("private authority record is empty")
	}
	conflict := record
	conflict.SourceRequest.ProviderProject.UID = "replacement"
	if _, err := store.Create(context.Background(), conflict); !errors.Is(err, ErrAuthorityInvalid) {
		t.Fatalf("conflict error = %v", err)
	}
}

func entitlementObject(generation int64, phase string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "services.miloapis.com/v1alpha1", "kind": "ServiceEntitlement", "metadata": map[string]interface{}{"name": EntitlementName, "uid": "entitlement-uid", "generation": generation}, "status": map[string]interface{}{"phase": phase, "serviceName": ServiceName, "conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "observedGeneration": generation}}}}}
	object.SetGroupVersionKind(entitlementGVK)
	return object
}

func TestEntitlementUsesReviewedActiveReadyShape(t *testing.T) {
	active := entitlementObject(2, "Active")
	_ = unstructured.SetNestedSlice(active.Object, []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "observedGeneration": int64(2)}}, "status", "conditions")
	stale := entitlementObject(2, "Active")
	_ = unstructured.SetNestedSlice(stale.Object, []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "observedGeneration": int64(1)}}, "status", "conditions")
	for name, object := range map[string]*unstructured.Unstructured{"active": active, "inactive": entitlementObject(2, "Pending"), "stale": stale} {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			scheme.AddKnownTypeWithName(entitlementGVK, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(entitlementGVK.GroupVersion().WithKind("ServiceEntitlementList"), &unstructured.UnstructuredList{})
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(object).Build()
			evidence, err := (ServiceEntitlementReader{}).Current(context.Background(), kube)
			if name == "active" && (err != nil || evidence.UID == "") {
				t.Fatalf("evidence = %#v, %v", evidence, err)
			}
			if name != "active" && !errors.Is(err, ErrEntitlementUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
