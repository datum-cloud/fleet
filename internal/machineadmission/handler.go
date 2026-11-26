// Package machineadmission records who created a Machine.
//
// Milo's root API server calls both webhooks, because Milo does not evaluate
// admission policies stored in a project. The request's UserInfo is the
// original caller, as Milo's routing establishes it, with the project in its
// extras. The mutating webhook writes that caller into spec.creator and adds
// the Machine protection finalizer in the create itself, replacing any
// creator the client sent. The validating webhook refuses a create whose
// creator is not the caller or that lacks the finalizer, and any change to
// the creator. A controller can therefore trust spec.creator: only these
// webhooks could have set it. Neither webhook reads anything, keeps state or
// holds credentials.
package machineadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.datum.net/fleet/internal/allocation"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/tenancy"
)

const (
	// MutatePath and ValidatePath are where the handlers are served.
	MutatePath   = "/mutate-machine-creator"
	ValidatePath = "/validate-machine-creator"
)

// Mutator sets spec.creator to the caller and adds the protection
// finalizer on create.
type Mutator struct{}

// Validator admits creates whose creator is the caller and that carry the
// protection finalizer, and updates that leave the creator unchanged.
type Validator struct{}

// consumer returns the typed reference to the caller's Milo project. Milo
// sets the parent extras when it routes a request through a project control
// plane, and overwrites any the client supplied. A Machine created through
// Milo has a Project consumer; tenancy refuses any other kind.
func consumer(actor authenticationv1.UserInfo) (metal.TypedReference, error) {
	group, kind, name := actor.Extra[iam.ParentAPIGroupExtraKey], actor.Extra[iam.ParentKindExtraKey], actor.Extra[iam.ParentNameExtraKey]
	if len(group) != 1 || len(kind) != 1 || len(name) != 1 {
		return metal.TypedReference{}, errors.New("an authenticated Milo project context is required")
	}
	ref := metal.TypedReference{APIGroup: group[0], Kind: kind[0], Name: name[0]}
	if err := tenancy.Supported(ref); err != nil || ref != tenancy.Project(ref.Name) {
		return metal.TypedReference{}, errors.New("an authenticated Milo project context is required")
	}
	return ref, nil
}

// caller returns the creator for an authenticated caller in a Milo project.
func caller(actor authenticationv1.UserInfo) (metal.MachineCreator, error) {
	if _, err := consumer(actor); err != nil {
		return metal.MachineCreator{}, err
	}
	if actor.Username == "" || actor.UID == "" {
		return metal.MachineCreator{}, errors.New("an authenticated caller and project are required")
	}
	return metal.MachineCreator{Username: actor.Username, UID: actor.UID}, nil
}

func supported(request admission.Request) bool {
	return request.Resource.Group == metal.GroupVersion.Group && request.Resource.Resource == "machines" && request.SubResource == ""
}

// decode reads the object with exact, case-sensitive keys, as the API
// server does. encoding/json into a struct would match keys without regard
// to case. Numbers stay exact, although nothing is re-encoded from them.
func decode(raw []byte) (map[string]any, map[string]any, error) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return nil, nil, err
	}
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("spec is not an object")
	}
	return object, spec, nil
}

// creatorOf returns spec.creator, or nil when it is absent.
func creatorOf(spec map[string]any) (*metal.MachineCreator, error) {
	value, ok := spec["creator"]
	if !ok {
		return nil, nil
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("spec.creator is not an object")
	}
	username, _ := fields["username"].(string)
	uid, _ := fields["uid"].(string)
	return &metal.MachineCreator{Username: username, UID: uid}, nil
}

func finalizers(object map[string]any) []string {
	metadata, _ := object["metadata"].(map[string]any)
	values, _ := metadata["finalizers"].([]any)
	var result []string
	for _, value := range values {
		if name, ok := value.(string); ok {
			result = append(result, name)
		}
	}
	return result
}

func (Mutator) Handle(_ context.Context, request admission.Request) admission.Response {
	if !supported(request) || request.Operation != admissionv1.Create {
		return admission.Denied("unsupported Machine creator admission request")
	}
	creator, err := caller(request.UserInfo)
	if err != nil {
		return admission.Denied(err.Error())
	}
	object, spec, err := decode(request.Object.Raw)
	if err != nil {
		return admission.Denied("the Machine could not be decoded")
	}
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		return admission.Denied("the Machine could not be decoded")
	}
	// Patch only the two fields, so nothing else in the request is
	// re-encoded.
	value := map[string]any{"username": creator.Username, "uid": creator.UID}
	patches := []jsonpatch.JsonPatchOperation{jsonpatch.NewOperation("add", "/spec/creator", value)}
	if _, ok := spec["creator"]; ok {
		patches[0] = jsonpatch.NewOperation("replace", "/spec/creator", value)
	}
	if !slices.Contains(finalizers(object), allocation.MachineFinalizer) {
		if existing, ok := metadata["finalizers"].([]any); ok && existing != nil {
			patches = append(patches, jsonpatch.NewOperation("add", "/metadata/finalizers/-", allocation.MachineFinalizer))
		} else {
			patches = append(patches, jsonpatch.NewOperation("add", "/metadata/finalizers", []string{allocation.MachineFinalizer}))
		}
	}
	return admission.Patched("the creator is the caller", patches...)
}

func (Validator) Handle(_ context.Context, request admission.Request) admission.Response {
	if !supported(request) {
		return admission.Denied("unsupported Machine creator admission request")
	}
	switch request.Operation {
	case admissionv1.Create:
		expected, err := caller(request.UserInfo)
		if err != nil {
			return admission.Denied(err.Error())
		}
		object, spec, err := decode(request.Object.Raw)
		if err != nil {
			return admission.Denied("the Machine could not be decoded")
		}
		creator, err := creatorOf(spec)
		if err != nil || creator == nil || *creator != expected {
			return admission.Denied("spec.creator must be the authenticated caller's username and UID")
		}
		if !slices.Contains(finalizers(object), allocation.MachineFinalizer) {
			return admission.Denied("a Machine is created with the protection finalizer")
		}
		return admission.Allowed("the creator is the caller")
	case admissionv1.Update:
		_, spec, err := decode(request.Object.Raw)
		if err != nil {
			return admission.Denied("the Machine could not be decoded")
		}
		_, previousSpec, err := decode(request.OldObject.Raw)
		if err != nil {
			return admission.Denied("the Machine could not be decoded")
		}
		creator, err := creatorOf(spec)
		if err != nil {
			return admission.Denied("spec.creator cannot change")
		}
		previous, err := creatorOf(previousSpec)
		if err != nil || (creator == nil) != (previous == nil) || (creator != nil && *creator != *previous) {
			return admission.Denied("spec.creator cannot change")
		}
		return admission.Allowed("the creator is unchanged")
	}
	return admission.Denied("unsupported Machine creator admission request")
}
