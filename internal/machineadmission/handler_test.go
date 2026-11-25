package machineadmission

import (
	"context"
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.datum.net/fleet/internal/allocation"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
)

func actor(name, project string) authenticationv1.UserInfo {
	return authenticationv1.UserInfo{Username: name, UID: name + "-uid", Extra: map[string]authenticationv1.ExtraValue{
		iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {project},
	}}
}

func machine(t *testing.T, creator *metal.MachineCreator, finalizers ...string) runtime.RawExtension {
	t.Helper()
	data, err := json.Marshal(metal.Machine{
		TypeMeta:   metav1.TypeMeta{APIVersion: metal.GroupVersion.String(), Kind: "Machine"},
		ObjectMeta: metav1.ObjectMeta{Name: "machine-example", Namespace: "default", Finalizers: finalizers},
		Spec:       metal.MachineSpec{MachineClassRef: metal.LocalReference{Name: "class"}, MachineProfileRef: metal.LocalReference{Name: "profile"}, LocationRef: metal.LocalReference{Name: "location"}, Creator: creator},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime.RawExtension{Raw: data}
}

func request(operation admissionv1.Operation, user authenticationv1.UserInfo, newObject, oldObject runtime.RawExtension) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: operation, UserInfo: user, Object: newObject, OldObject: oldObject,
		Resource: metav1.GroupVersionResource{Group: metal.GroupVersion.Group, Version: "v1alpha1", Resource: "machines"},
	}}
}

// apply returns the Machine the API server would store after the mutation.
func apply(t *testing.T, raw []byte, response admission.Response) metal.Machine {
	t.Helper()
	patches, err := json.Marshal(response.Patches)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := jsonpatch.DecodePatch(patches)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patch.Apply(raw)
	if err != nil {
		t.Fatal(err)
	}
	var result metal.Machine
	if err := json.Unmarshal(patched, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMutatorRecordsTheCaller(t *testing.T) {
	user := actor("customer-example", "consumer-example")
	self := metal.MachineCreator{Username: "customer-example", UID: "customer-example-uid"}
	for name, sent := range map[string]*metal.MachineCreator{
		"no creator":         nil,
		"someone else":       {Username: "other", UID: "other-uid"},
		"the caller's name":  {Username: "customer-example", UID: "forged"},
		"the caller already": &self,
	} {
		t.Run(name, func(t *testing.T) {
			raw := machine(t, sent, "example.com/other")
			response := Mutator{}.Handle(context.Background(), request(admissionv1.Create, user, raw, runtime.RawExtension{}))
			if !response.Allowed {
				t.Fatalf("refused: %s", response.Result.Message)
			}
			stored := apply(t, raw.Raw, response)
			if stored.Spec.Creator == nil || *stored.Spec.Creator != self {
				t.Fatalf("creator = %+v", stored.Spec.Creator)
			}
			if len(stored.Finalizers) != 2 || stored.Finalizers[0] != "example.com/other" || stored.Finalizers[1] != allocation.MachineFinalizer {
				t.Fatalf("finalizers = %v", stored.Finalizers)
			}
			// The mutated Machine passes validation.
			validated := Validator{}.Handle(context.Background(), request(admissionv1.Create, user, machine(t, stored.Spec.Creator, stored.Finalizers...), runtime.RawExtension{}))
			if !validated.Allowed {
				t.Fatalf("mutated Machine refused: %s", validated.Result.Message)
			}
		})
	}
	t.Run("the finalizer is not added twice", func(t *testing.T) {
		raw := machine(t, nil, allocation.MachineFinalizer)
		stored := apply(t, raw.Raw, Mutator{}.Handle(context.Background(), request(admissionv1.Create, user, raw, runtime.RawExtension{})))
		if len(stored.Finalizers) != 1 {
			t.Fatalf("finalizers = %v", stored.Finalizers)
		}
	})
}

func TestMutatorRefuses(t *testing.T) {
	noProject := actor("customer-example", "consumer-example")
	delete(noProject.Extra, iam.ParentNameExtraKey)
	organization := actor("customer-example", "consumer-example")
	organization.Extra[iam.ParentKindExtraKey] = authenticationv1.ExtraValue{"Organization"}
	noUID := actor("customer-example", "consumer-example")
	noUID.UID = ""
	other := request(admissionv1.Create, actor("customer-example", "consumer-example"), machine(t, nil), runtime.RawExtension{})
	other.Resource.Resource = "installations"
	status := request(admissionv1.Create, actor("customer-example", "consumer-example"), machine(t, nil), runtime.RawExtension{})
	status.SubResource = "status"
	for name, request := range map[string]admission.Request{
		"no project":             request(admissionv1.Create, noProject, machine(t, nil), runtime.RawExtension{}),
		"an organization":        request(admissionv1.Create, organization, machine(t, nil), runtime.RawExtension{}),
		"no caller UID":          request(admissionv1.Create, noUID, machine(t, nil), runtime.RawExtension{}),
		"an update":              request(admissionv1.Update, actor("customer-example", "consumer-example"), machine(t, nil), machine(t, nil)),
		"another resource":       other,
		"a subresource":          status,
		"an undecodable Machine": request(admissionv1.Create, actor("customer-example", "consumer-example"), runtime.RawExtension{Raw: []byte(`{"spec":"x"}`)}, runtime.RawExtension{}),
	} {
		t.Run(name, func(t *testing.T) {
			if response := (Mutator{}).Handle(context.Background(), request); response.Allowed {
				t.Fatal("allowed")
			}
		})
	}
}

func TestValidator(t *testing.T) {
	user := actor("customer-example", "consumer-example")
	self := &metal.MachineCreator{Username: "customer-example", UID: "customer-example-uid"}
	other := &metal.MachineCreator{Username: "other", UID: "other-uid"}
	none := runtime.RawExtension{}
	cases := map[string]struct {
		request admission.Request
		allowed bool
	}{
		"a create naming the caller with the finalizer": {request(admissionv1.Create, user, machine(t, self, allocation.MachineFinalizer), none), true},
		"a create without the finalizer":                {request(admissionv1.Create, user, machine(t, self), none), false},
		"a create without a creator":                    {request(admissionv1.Create, user, machine(t, nil, allocation.MachineFinalizer), none), false},
		"a create naming someone else":                  {request(admissionv1.Create, user, machine(t, other, allocation.MachineFinalizer), none), false},
		"another caller claiming this caller":           {request(admissionv1.Create, actor("other", "consumer-example"), machine(t, self, allocation.MachineFinalizer), none), false},
		"an update keeping the creator":                 {request(admissionv1.Update, user, machine(t, self), machine(t, self, allocation.MachineFinalizer)), true},
		"another caller's update keeping the creator":   {request(admissionv1.Update, actor("other", "consumer-example"), machine(t, self), machine(t, self)), true},
		"an update changing the creator":                {request(admissionv1.Update, user, machine(t, other), machine(t, self)), false},
		"an update removing the creator":                {request(admissionv1.Update, user, machine(t, nil), machine(t, self)), false},
		"an update adding a creator":                    {request(admissionv1.Update, user, machine(t, self), machine(t, nil)), false},
		"an update of a Machine that never had one":     {request(admissionv1.Update, user, machine(t, nil), machine(t, nil)), true},
		"a delete": {request(admissionv1.Delete, user, none, machine(t, self)), false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if response := (Validator{}).Handle(context.Background(), test.request); response.Allowed != test.allowed {
				t.Fatalf("allowed = %v: %s", response.Allowed, response.Result.Message)
			}
		})
	}
}
