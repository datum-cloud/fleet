package powerwebhook

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const writer = "manager-example"

func raw(status string) runtime.RawExtension {
	return runtime.RawExtension{Raw: []byte(`{"apiVersion":"metal.datumapis.com/v1alpha1","kind":"Machine","status":` + status + `}`)}
}

func request(user string, newStatus, oldStatus string) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Update, UserInfo: authenticationv1.UserInfo{Username: user, UID: user + "-uid"},
		Resource:    metav1.GroupVersionResource{Group: "metal.datumapis.com", Version: "v1alpha1", Resource: "machines"},
		SubResource: "status", Object: raw(newStatus), OldObject: raw(oldStatus),
	}}
}

func TestHandler(t *testing.T) {
	handler := &Handler{Writer: writer}
	on, off := `{"phase":"Ready","power":{"state":"On"}}`, `{"phase":"Ready","power":{"state":"Off"}}`
	none, pending := `{"phase":"Ready"}`, `{"phase":"Pending","power":{"state":"On"}}`
	cases := map[string]struct {
		request admission.Request
		allowed bool
	}{
		"the writer changes power":                 {request(writer, off, on), true},
		"the writer sets power":                    {request(writer, on, none), true},
		"the writer clears power":                  {request(writer, none, on), true},
		"another identity leaves power unchanged":  {request("consumer-example", pending, on), true},
		"another identity with no power":           {request("consumer-example", `{"phase":"Pending"}`, none), true},
		"another identity changes power":           {request("consumer-example", off, on), false},
		"another identity sets power":              {request("provider-example", on, none), false},
		"another identity clears power":            {request("staff-example", none, on), false},
		"another identity adds a power field":      {request("consumer-example", `{"power":{"state":"On","extra":"x"}}`, on), false},
		"a writer named only by prefix":            {request(writer+"-other", off, on), false},
		"a null power equals an absent one":        {request("consumer-example", `{"phase":"Ready","power":null}`, none), true},
		"a key that differs only by case is apart": {request("consumer-example", `{"phase":"Ready","Power":{"state":"Off"},"power":{"state":"On"}}`, on), true},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := handler.Handle(context.Background(), test.request).Allowed; got != test.allowed {
				t.Fatalf("allowed = %v, want %v", got, test.allowed)
			}
		})
	}
	t.Run("no configured writer refuses every power change", func(t *testing.T) {
		empty := &Handler{}
		if empty.Handle(context.Background(), request("", off, on)).Allowed || empty.Handle(context.Background(), request(writer, off, on)).Allowed {
			t.Fatal("a power change was admitted without a writer")
		}
		if !empty.Handle(context.Background(), request(writer, pending, on)).Allowed {
			t.Fatal("an unchanged power was refused")
		}
	})
	t.Run("only Machine status updates are handled", func(t *testing.T) {
		for _, mutate := range []func(*admission.Request){
			func(r *admission.Request) { r.SubResource = "" },
			func(r *admission.Request) { r.Resource.Resource = "installations" },
			func(r *admission.Request) { r.Resource.Group = "fleet.datumapis.com" },
			func(r *admission.Request) { r.Operation = admissionv1.Create },
		} {
			r := request(writer, off, on)
			mutate(&r)
			if handler.Handle(context.Background(), r).Allowed {
				t.Fatalf("admitted %+v", r.AdmissionRequest)
			}
		}
	})
	t.Run("an undecodable object is refused", func(t *testing.T) {
		r := request(writer, off, on)
		r.Object.Raw = []byte("{")
		if handler.Handle(context.Background(), r).Allowed {
			t.Fatal("admitted an undecodable object")
		}
	})
}
