package registration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func controlFor(t *testing.T, handler *Handler, key ed25519.PrivateKey, action string) SignedControl {
	t.Helper()
	secret, route, err := handler.Store.root(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return SignControl(ControlRequest{Action: action, ID: "operator-change-" + secret.ResourceVersion, Reason: "synthetic test approval", Authority: handler.Store.Authority, Projects: handler.Store.Projects, RootUID: secret.UID, ResourceVersion: secret.ResourceVersion, Epoch: route.Epoch, FenceDigest: "approved-fence"}, key)
}

func controlFixture(t *testing.T, handler *Handler) ed25519.PrivateKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	handler.Store.ControlKey = public
	handler.Store.AllowInactiveForTests = false
	handler.VerifyFence = func(context.Context) error { return nil }
	handler.FenceDigest = func(context.Context) (string, error) { return "approved-fence", nil }
	return private
}

func TestBootstrapDoesNotAdmitOrInterpretCallerConfiguration(t *testing.T) {
	ctx := context.Background()
	handler, device := fixture(t)
	private := controlFixture(t, handler)
	server := httptest.NewServer(handler.Transport())
	defer server.Close()
	for path, expected := range map[string]int{"/transportz": http.StatusNoContent, "/activationz": http.StatusServiceUnavailable} {
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("%s: %d", path, response.StatusCode)
		}
	}
	request := registrationRequest(t, device, "bootstrap")
	for _, resource := range []metav1.GroupVersionResource{{Group: "metal.datumapis.com", Resource: "machinecapacities"}, {Group: "fleet.datumapis.com", Resource: "devices"}, {Resource: "namespaces"}, {Group: "resourcemanager.miloapis.com", Resource: "projects"}} {
		request.Resource, request.Operation, request.Name = resource, admissionv1.Delete, "unrelated"
		if handler.Handle(ctx, request).Allowed {
			t.Fatal("bootstrap admitted a request")
		}
	}
	control := controlFor(t, handler, private, "activate")
	for name, mutate := range map[string]func(*SignedControl){
		"signature": func(control *SignedControl) { control.Signature[0] ^= 1 },
		"authority": func(control *SignedControl) { control.Request.Authority = "other" },
		"scope":     func(control *SignedControl) { control.Request.Projects = []string{"other"} },
		"epoch":     func(control *SignedControl) { control.Request.Epoch++ },
		"root":      func(control *SignedControl) { control.Request.RootUID = "replacement" },
		"fence":     func(control *SignedControl) { control.Request.FenceDigest = "relaxed" },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(control)
			var changed SignedControl
			_ = json.Unmarshal(encoded, &changed)
			mutate(&changed)
			if handler.ApplyControl(ctx, changed) == nil {
				t.Fatal("tampered configuration accepted")
			}
			if name != "signature" {
				changed = SignControl(changed.Request, private)
				if handler.ApplyControl(ctx, changed) == nil {
					t.Fatal("wrong signed target accepted")
				}
			}
		})
	}
	handler.VerifyFence = func(context.Context) error { return errors.New("missing Fail webhook") }
	if handler.ApplyControl(ctx, control) == nil {
		t.Fatal("missing fence activated")
	}
	handler.VerifyFence = func(context.Context) error { return nil }
	if err := handler.ApplyControl(ctx, control); err != nil {
		t.Fatal(err)
	}
	if err := handler.ApplyControl(ctx, control); err != nil {
		t.Fatal("activation retry", err)
	}
	if err := handler.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	secret, before, err := handler.Store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var legacy Ledger
	if err := json.Unmarshal(secret.Data["ledger.json"], &legacy); err != nil || legacy.Authority == handler.Store.Authority {
		t.Fatal("old binary not fenced")
	}
	handler.FenceDigest = func(context.Context) (string, error) { return "changed-fence", nil }
	if handler.Handle(ctx, registrationRequest(t, device, "changed-trust")).Allowed {
		t.Fatal("changed fence bypassed activation")
	}
	_, after, _ := handler.Store.Read(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("denial changed reservations")
	}
	handler.FenceDigest = func(context.Context) (string, error) { return "approved-fence", nil }
	handler.VerifyFence = func(context.Context) error { return errors.New("unavailable") }
	if handler.Ready(ctx) == nil {
		t.Fatal("lost fence reported active")
	}
	handler.VerifyFence = func(context.Context) error { return nil }
	for range 7 {
		if err := handler.ApplyControl(ctx, controlFor(t, handler, private, "activate")); err != nil {
			t.Fatal(err)
		}
	}
	if err := handler.ApplyControl(ctx, controlFor(t, handler, private, "activate")); err == nil {
		t.Fatal("activation audit history exceeded its bound")
	}
	current, _, err := handler.Store.root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Store.Client.Delete(ctx, current); err != nil {
		t.Fatal(err)
	}
	if handler.Handle(ctx, registrationRequest(t, device, "lost-ledger")).Allowed {
		t.Fatal("missing ledger admitted")
	}
	response, err := server.Client().Get(server.URL + "/transportz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal("missing ledger prevented transport inspection")
	}
}
