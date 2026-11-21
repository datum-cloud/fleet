package v1alpha1

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func syntheticSite() *Site {
	return &Site{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Site"},
		ObjectMeta: metav1.ObjectMeta{Name: "site-stable", Namespace: "inventory", UID: "site-uid"},
		Spec:       SiteSpec{PhysicalMetadata: PhysicalMetadata{DisplayName: "Example operational site", Provenance: Provenance{Method: "Manual", RecordedBy: "synthetic", RecordedAt: metav1.NewTime(recordedAt)}}, Code: "EX3", FacilityRefs: []LocalReference{{Name: "facility-stable"}}},
	}
}

func syntheticFacility() *Facility {
	return &Facility{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Facility"},
		ObjectMeta: metav1.ObjectMeta{Name: "facility-stable", Namespace: "inventory", UID: "facility-uid"},
		Spec:       FacilitySpec{PhysicalMetadata: syntheticSite().Spec.PhysicalMetadata, Identifiers: []FacilityIdentifier{{Provider: "example-a", Type: "PremisesCode", Value: "EX1"}, {Provider: "example-b", Type: "PremisesCode", Value: "EX1"}}},
	}
}

func TestSiteAndFacilityAdmission(t *testing.T) {
	site, facility := syntheticSite(), syntheticFacility()
	for _, record := range []any{site, facility} {
		kind := object(t, record)["kind"].(string)
		if errs := admit(t, kind, object(t, record), nil); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	legacy := syntheticFacility()
	legacy.Spec.Identifiers = nil
	if errs := admit(t, "Facility", object(t, legacy), object(t, legacy)); len(errs) != 0 {
		t.Fatal(errs)
	}
	renamed := site.DeepCopyObject().(*Site)
	renamed.Spec.Code = "NEW-CODE"
	renamed.Spec.DisplayName = "Renamed operational label"
	if errs := admit(t, "Site", object(t, renamed), object(t, site)); len(errs) == 0 {
		t.Fatal("a rename must advance provenance")
	}
	renamed.Spec.Provenance.RecordedAt = metav1.NewTime(recordedAt.Add(time.Second))
	if errs := admit(t, "Site", object(t, renamed), object(t, site)); len(errs) != 0 {
		t.Fatal(errs)
	}
	for name, mutate := range map[string]func(*Site){
		"missing name": func(candidate *Site) { candidate.Spec.FacilityRefs[0].Name = "" },
		"missing code": func(candidate *Site) { candidate.Spec.Code = "" },
		"duplicate name": func(candidate *Site) {
			candidate.Spec.FacilityRefs = append(candidate.Spec.FacilityRefs, LocalReference{Name: "facility-stable"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := syntheticSite()
			mutate(candidate)
			if errs := admit(t, "Site", object(t, candidate), nil); len(errs) == 0 {
				t.Fatal("accepted invalid Site relationship")
			}
		})
	}
	for _, identifier := range []FacilityIdentifier{{Type: "PremisesCode", Value: "EX1"}, {Provider: "example", Type: "Metro", Value: "EX"}, {Provider: "example", Type: "Account", Value: "account"}, {Provider: "example", Type: "PremisesCode"}} {
		candidate := syntheticFacility()
		candidate.Spec.Identifiers = []FacilityIdentifier{identifier}
		if errs := admit(t, "Facility", object(t, candidate), nil); len(errs) == 0 {
			t.Fatal("accepted unqualified or non-premises identifier")
		}
	}
	facility.Spec.Identifiers = append(facility.Spec.Identifiers, facility.Spec.Identifiers[0])
	if errs := admit(t, "Facility", object(t, facility), nil); len(errs) == 0 {
		t.Fatal("accepted duplicate identifier")
	}
}

type deniedFacilityReader struct{ client.Reader }

func (deniedFacilityReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return fmt.Errorf("forbidden secret-provider-code")
}

func TestSiteReaderIsObservationalAndNamespaceScoped(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	facility, site := syntheticFacility(), syntheticSite()
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(facility, site).Build()
	before := facility.DeepCopyObject()
	resolved, err := ReadSiteFacilities(context.Background(), store, site)
	if err != nil || len(resolved) != 1 {
		t.Fatalf("membership: %v, %v", resolved, err)
	}
	site.Spec.Code = "RENAMED"
	facility.Spec.Identifiers[0].Value = "RENAMED-PROVIDER-CODE"
	if err := store.Update(context.Background(), facility); err != nil {
		t.Fatal(err)
	}
	resolved, err = ReadSiteFacilities(context.Background(), store, site)
	if err != nil || resolved[0].UID != before.(*Facility).UID {
		t.Fatal("labels changed stable membership")
	}
	for _, candidate := range []*Site{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "other", UID: site.UID}, Spec: site.Spec},
		{ObjectMeta: site.ObjectMeta, Spec: SiteSpec{FacilityRefs: []LocalReference{{Name: "missing"}}}},
		{ObjectMeta: site.ObjectMeta, Spec: SiteSpec{FacilityRefs: []LocalReference{{Name: facility.Name}, {Name: facility.Name}}}},
	} {
		if records, err := ReadSiteFacilities(context.Background(), store, candidate); err == nil || records != nil {
			t.Fatal("unresolved membership was treated as resolved")
		}
	}
	if records, err := ReadSiteFacilities(context.Background(), deniedFacilityReader{}, site); err == nil || records != nil || strings.Contains(err.Error(), "secret-provider-code") {
		t.Fatal("denied read leaked data or returned membership")
	}
	empty := syntheticSite()
	empty.Spec.FacilityRefs = nil
	if records, err := ReadSiteFacilities(context.Background(), store, empty); err != nil || len(records) != 0 {
		t.Fatal("unmapped Site should resolve to no Facilities")
	}
	var storedSite Site
	if err := store.Get(context.Background(), client.ObjectKeyFromObject(site), &storedSite); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(object(t, storedSite)["spec"], object(t, syntheticSite())["spec"]) {
		t.Fatal("reader changed stored Site")
	}
}

func TestSiteNameReuseResolvesReplacementWithoutChangingDesiredState(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	site, facility := syntheticSite(), syntheticFacility()
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(site, facility).Build()
	before := site.DeepCopyObject().(*Site)
	if err := store.Delete(context.Background(), facility); err != nil {
		t.Fatal(err)
	}
	if records, err := ReadSiteFacilities(context.Background(), store, site); err == nil || records != nil {
		t.Fatal("missing name should remain unresolved")
	}
	replacement := syntheticFacility()
	replacement.UID = "replacement-server-uid"
	if err := store.Create(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	records, err := ReadSiteFacilities(context.Background(), store, site)
	if err != nil || len(records) != 1 || records[0].UID != replacement.UID {
		t.Fatalf("name reference must resolve the replacement: %v, %v", records, err)
	}
	if !reflect.DeepEqual(object(t, site)["spec"], object(t, before)["spec"]) {
		t.Fatal("reader changed desired state")
	}
	replacement.DeletionTimestamp = &metav1.Time{Time: recordedAt}
	replacement.Finalizers = []string{"test.example/retain"}
	deleting := fake.NewClientBuilder().WithScheme(scheme).WithObjects(replacement).Build()
	if records, err := ReadSiteFacilities(context.Background(), deleting, site); err == nil || records != nil {
		t.Fatal("deleting Facility should remain unavailable")
	}
}

func TestSiteAndFacilitySpecsContainNoUIDFields(t *testing.T) {
	var inspect func(extensionsv1.JSONSchemaProps, string)
	inspect = func(schema extensionsv1.JSONSchemaProps, path string) {
		for name, property := range schema.Properties {
			if strings.Contains(strings.ToLower(name), "uid") {
				t.Fatalf("UID field in desired-state schema: %s.%s", path, name)
			}
			inspect(property, path+"."+name)
		}
		if schema.Items != nil && schema.Items.Schema != nil {
			inspect(*schema.Items.Schema, path+"[]")
		}
	}
	crds := loadCRDs(t)
	for _, kind := range []string{"Site", "Facility"} {
		inspect(crds[kind].Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"], kind+".spec")
	}
	site := syntheticSite()
	encoded := object(t, site)
	refs := encoded["spec"].(map[string]any)["facilityRefs"].([]any)
	if !reflect.DeepEqual(refs[0], map[string]any{"name": "facility-stable"}) {
		t.Fatal("serialized spec reference must contain only a name")
	}
	if encoded["metadata"].(map[string]any)["uid"] != "site-uid" {
		t.Fatal("server metadata UID was lost")
	}
	copy := site.DeepCopyObject().(*Site)
	copy.Spec.FacilityRefs[0].Name = "other"
	if site.Spec.FacilityRefs[0].Name != "facility-stable" || copy.UID != site.UID {
		t.Fatal("deep copy must isolate references and retain server identity")
	}
}
