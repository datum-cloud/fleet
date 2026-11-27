package creatorwebhook

import (
	"context"
	"encoding/json"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var resources = []string{"deviceinstallations", "devicewipes", "devicerecoveries"}

func caller(name, project string) authenticationv1.UserInfo {
	return authenticationv1.UserInfo{Username: name, UID: name + "-uid", Extra: map[string]authenticationv1.ExtraValue{
		iam.ParentKindExtraKey: {"Project"}, iam.ParentAPIGroupExtraKey: {"resourcemanager.miloapis.com"}, iam.ParentNameExtraKey: {project},
	}}
}

func object(t *testing.T, creator *ops.Creator) runtime.RawExtension {
	t.Helper()
	recovery := ops.DeviceRecovery{Spec: ops.DeviceRecoverySpec{Decision: ops.DecisionSettle}}
	if creator != nil {
		recovery.Spec.Creator = *creator
	}
	data, err := json.Marshal(recovery)
	if err != nil {
		t.Fatal(err)
	}
	if creator == nil {
		var raw map[string]any
		_ = json.Unmarshal(data, &raw)
		delete(raw["spec"].(map[string]any), "creator")
		data, _ = json.Marshal(raw)
	}
	return runtime.RawExtension{Raw: data}
}

func request(operation admissionv1.Operation, user authenticationv1.UserInfo, newObject, oldObject runtime.RawExtension) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: operation, UserInfo: user, Object: newObject, OldObject: oldObject,
		Resource: metav1.GroupVersionResource{Group: ops.GroupVersion.Group, Version: "v1alpha1", Resource: "devicerecoveries"},
	}}
}

func TestHandler(t *testing.T) {
	handler := &Handler{Projects: []string{"provider-example"}, Resources: resources}
	staff := caller("staff-example", "provider-example")
	self := &ops.Creator{Username: "staff-example", UID: "staff-example-uid"}
	// Some credentials, such as client certificates, carry no UID.
	service := caller("service-example", "provider-example")
	service.UID = ""
	cases := map[string]struct {
		request admission.Request
		allowed bool
	}{
		"a create naming the caller":            {request(admissionv1.Create, staff, object(t, self), runtime.RawExtension{}), true},
		"a create naming someone else":          {request(admissionv1.Create, staff, object(t, &ops.Creator{Username: "other", UID: "other-uid"}), runtime.RawExtension{}), false},
		"a create with the caller's name only":  {request(admissionv1.Create, staff, object(t, &ops.Creator{Username: "staff-example", UID: "forged"}), runtime.RawExtension{}), false},
		"a create without a creator":            {request(admissionv1.Create, staff, object(t, nil), runtime.RawExtension{}), false},
		"a caller outside a project":            {request(admissionv1.Create, authenticationv1.UserInfo{Username: "staff-example", UID: "staff-example-uid"}, object(t, self), runtime.RawExtension{}), false},
		"a caller in another project":           {request(admissionv1.Create, caller("staff-example", "consumer-example"), object(t, self), runtime.RawExtension{}), false},
		"an update keeping the creator":         {request(admissionv1.Update, caller("coordinator", "provider-example"), object(t, self), object(t, self)), true},
		"an update changing the creator":        {request(admissionv1.Update, staff, object(t, &ops.Creator{Username: "x", UID: "y"}), object(t, self)), false},
		"a delete":                              {request(admissionv1.Delete, staff, runtime.RawExtension{}, object(t, self)), false},
		"a caller without a UID naming itself":  {request(admissionv1.Create, service, object(t, &ops.Creator{Username: "service-example"}), runtime.RawExtension{}), true},
		"a caller without a UID adding one":     {request(admissionv1.Create, service, object(t, &ops.Creator{Username: "service-example", UID: "forged"}), runtime.RawExtension{}), false},
		"a caller without a UID naming another": {request(admissionv1.Create, service, object(t, &ops.Creator{Username: "staff-example"}), runtime.RawExtension{}), false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if response := handler.Handle(context.Background(), test.request); response.Allowed != test.allowed {
				t.Fatalf("allowed = %v (%v)", response.Allowed, response.Result)
			}
		})
	}
	t.Run("creator keys are read case-sensitively", func(t *testing.T) {
		// encoding/json into a struct would let the later "Creator" win.
		raw := runtime.RawExtension{Raw: []byte(`{"spec":{"creator":{"username":"other","uid":"other-uid"},"Creator":{"username":"staff-example","uid":"staff-example-uid"}}}`)}
		if handler.Handle(context.Background(), request(admissionv1.Create, staff, raw, runtime.RawExtension{})).Allowed {
			t.Fatal("a differently cased duplicate key decided the creator")
		}
	})
	t.Run("ungoverned resources and subresources are denied", func(t *testing.T) {
		other := request(admissionv1.Create, staff, object(t, self), runtime.RawExtension{})
		other.Resource.Resource = "devicecustodies"
		status := request(admissionv1.Update, staff, object(t, self), object(t, self))
		status.SubResource = "status"
		for _, request := range []admission.Request{other, status} {
			if handler.Handle(context.Background(), request).Allowed {
				t.Fatal("allowed")
			}
		}
	})
}
