package registration

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIdenticalAdmissionObjects(t *testing.T) {
	for _, test := range []struct {
		name, old, current string
		identical          bool
	}{
		{"key order and whitespace", `{"metadata":{"resourceVersion":"1"},"spec":{}}`, ` {"spec":{},"metadata":{"resourceVersion":"1"}} `, true},
		{"unknown field", `{"future":1}`, `{"future":2}`, false},
		{"large exact integers", `{"future":9007199254740992}`, `{"future":9007199254740993}`, false},
		{"managed fields", `{"metadata":{"managedFields":[]}}`, `{"metadata":{"managedFields":[{"manager":"new"}]}}`, false},
		{"status", `{"status":{}}`, `{"status":{"ready":true}}`, false},
		{"null versus absent", `{}`, `{"future":null}`, false},
		{"array order", `{"future":[1,2]}`, `{"future":[2,1]}`, false},
		{"null objects", `null`, `null`, false},
		{"malformed", `{`, `{`, false},
		{"trailing document", `{} {}`, `{}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if identicalObjects([]byte(test.old), []byte(test.current)) != test.identical {
				t.Fatal("unexpected whole-object comparison")
			}
		})
	}
}

func TestNoopCannotClearPendingRealUpdateFence(t *testing.T) {
	handler, device := fixture(t)
	ctx := context.Background()
	registration := registrationRequest(t, device, "record")
	update := registration
	update.Resource = metav1.GroupVersionResource{Group: "fleet.datumapis.com", Version: "v1alpha1", Resource: "devices"}
	update.Operation = admissionv1.Update
	update.OldObject.Raw, _ = json.Marshal(device)
	changed := device.DeepCopyObject()
	raw, _ := json.Marshal(changed)
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	object["futureField"] = "changed"
	update.Object.Raw, _ = json.Marshal(object)
	if !handler.Handle(ctx, update).Allowed {
		t.Fatal("real update rejected")
	}
	update.Object.Raw = update.OldObject.Raw
	if !handler.Handle(ctx, update).Allowed {
		t.Fatal("no-op rejected")
	}
	if handler.Handle(ctx, registration).Allowed {
		t.Fatal("no-op cleared the pending real-update reservation")
	}
}

func TestLifetimeCollectionDeletionDenied(t *testing.T) {
	for _, resource := range []metav1.GroupVersionResource{{Version: "v1", Resource: "namespaces"}, {Group: "resourcemanager.miloapis.com", Version: "v1alpha1", Resource: "projects"}} {
		t.Run(resource.Resource, func(t *testing.T) {
			handler, device := fixture(t)
			request := registrationRequest(t, device, "unused")
			request.Operation = admissionv1.Delete
			request.Name = ""
			request.Resource = resource
			if handler.Handle(context.Background(), request).Allowed {
				t.Fatal("unnamed deletion cannot exclude a protected lifetime")
			}
		})
	}
}
