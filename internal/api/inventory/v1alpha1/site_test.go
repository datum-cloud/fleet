package v1alpha1

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func syntheticSite() *Site {
	return &Site{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Site"},
		ObjectMeta: metav1.ObjectMeta{Name: "site-stable", Namespace: "inventory", UID: "site-uid"},
		Spec:       SiteSpec{PhysicalMetadata: PhysicalMetadata{DisplayName: "Example operational site", Provenance: Provenance{Method: "Manual", RecordedBy: "synthetic", RecordedAt: metav1.NewTime(recordedAt)}}, Code: "EX3", FacilityRefs: []FacilityMembershipReference{{Name: "facility-stable", UID: "facility-uid"}}},
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
		"missing UID":  func(candidate *Site) { candidate.Spec.FacilityRefs[0].UID = "" },
		"missing name": func(candidate *Site) { candidate.Spec.FacilityRefs[0].Name = "" },
		"missing code": func(candidate *Site) { candidate.Spec.Code = "" },
		"duplicate UID": func(candidate *Site) {
			candidate.Spec.FacilityRefs = append(candidate.Spec.FacilityRefs, FacilityMembershipReference{Name: "other", UID: "facility-uid"})
		},
		"duplicate name": func(candidate *Site) {
			candidate.Spec.FacilityRefs = append(candidate.Spec.FacilityRefs, FacilityMembershipReference{Name: "facility-stable", UID: "other"})
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

func TestSiteReaderIsObservationalAndIncarnationBound(t *testing.T) {
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
		{ObjectMeta: site.ObjectMeta, Spec: SiteSpec{FacilityRefs: []FacilityMembershipReference{{Name: facility.Name, UID: "replaced"}}}},
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
