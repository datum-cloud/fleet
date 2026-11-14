package milo

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var entitlementGVK = schema.GroupVersionKind{Group: "services.miloapis.com", Version: "v1alpha1", Kind: "ServiceEntitlement"}

type ServiceEntitlementReader struct{}

func (ServiceEntitlementReader) Current(ctx context.Context, kube client.Client) (EntitlementEvidence, error) {
	if kube == nil {
		return EntitlementEvidence{}, ErrEntitlementUnavailable
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(entitlementGVK)
	if err := kube.Get(ctx, client.ObjectKey{Name: EntitlementName}, object); err != nil {
		return EntitlementEvidence{}, ErrEntitlementUnavailable
	}
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	service, _, _ := unstructured.NestedString(object.Object, "status", "serviceName")
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	ready := findCondition(conditions, "Ready")
	if object.GetUID() == "" || phase != "Active" || service != ServiceName || ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != object.GetGeneration() {
		return EntitlementEvidence{}, ErrEntitlementUnavailable
	}
	return EntitlementEvidence{Name: object.GetName(), UID: object.GetUID(), Generation: object.GetGeneration(), ObservedGeneration: ready.ObservedGeneration, ServiceName: service}, nil
}

func findCondition(raw []interface{}, conditionType string) *metav1.Condition {
	for _, item := range raw {
		value, ok := item.(map[string]interface{})
		if !ok || value["type"] != conditionType {
			continue
		}
		condition := &metav1.Condition{Type: conditionType}
		if status, ok := value["status"].(string); ok {
			condition.Status = metav1.ConditionStatus(status)
		}
		switch observed := value["observedGeneration"].(type) {
		case int64:
			condition.ObservedGeneration = observed
		case float64:
			condition.ObservedGeneration = int64(observed)
		case int:
			condition.ObservedGeneration = int64(observed)
		}
		return condition
	}
	return nil
}
