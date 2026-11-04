package manager

import (
	"bytes"
	"encoding/json"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// PrimaryPredicate prevents the projector's public status writes from feeding
// the coordinator. Spec generations, labels, annotations, deletion, and
// protected-finalizer changes remain events. Provider observations belong on
// dependency resources (for example Machine status), where DependencyPredicate
// routes their semantic status changes.
func PrimaryPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return true
			}
			return e.ObjectOld.GetUID() != e.ObjectNew.GetUID() || e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() || !reflect.DeepEqual(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) || !reflect.DeepEqual(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations()) || !reflect.DeepEqual(e.ObjectOld.GetFinalizers(), e.ObjectNew.GetFinalizers()) || !reflect.DeepEqual(e.ObjectOld.GetDeletionTimestamp(), e.ObjectNew.GetDeletionTimestamp())
		},
	}
}

func PrivateRecordPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSecret, oldOK := e.ObjectOld.(*corev1.Secret)
			newSecret, newOK := e.ObjectNew.(*corev1.Secret)
			return !oldOK || !newOK || oldSecret.UID != newSecret.UID || oldSecret.Type != newSecret.Type || !reflect.DeepEqual(oldSecret.Labels, newSecret.Labels) || !reflect.DeepEqual(oldSecret.DeletionTimestamp, newSecret.DeletionTimestamp) || !bytes.Equal(oldSecret.Data["record.json"], newSecret.Data["record.json"])
		},
	}
}

// DependencyPredicate includes relevant spec, status, labels, UID, finalizers,
// and deletion changes while ignoring resourceVersion and managed-field noise.
func DependencyPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc:  func(e event.UpdateEvent) bool { return semanticDigest(e.ObjectOld) != semanticDigest(e.ObjectNew) },
	}
}

func semanticDigest(object client.Object) string {
	if object == nil {
		return ""
	}
	data, err := json.Marshal(object)
	if err != nil {
		return "invalid"
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return "invalid"
	}
	if metadata, ok := value["metadata"].(map[string]any); ok {
		delete(metadata, "resourceVersion")
		delete(metadata, "managedFields")
	}
	normalized, _ := json.Marshal(value)
	return string(normalized)
}
