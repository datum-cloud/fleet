// Package creatorwebhook is the validating webhook that keeps a Device
// operation's spec.creator equal to the authenticated caller who created it.
//
// Milo's root API server calls it, because Milo does not evaluate admission
// policies stored in a project. The request's UserInfo is the original
// caller, as Milo's routing establishes it, with the project in its extras;
// the webhook compares spec.creator with that identity and never substitutes
// its own. It reads nothing, keeps no state and holds no credentials: the API
// server has already authorized the caller to create the kind, and IAM
// decides which caller may create which kind.
package creatorwebhook

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	iam "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Path is where the handler is served.
const Path = "/validate-operation-creator"

// Resources are the operation plurals whose creator the webhook governs.
var Resources = []string{"deviceinstallations", "devicewipes", "devicerecoveries", "devicecommissionings", "devicecommissioningplans", "devicecommissioningplanapprovals"}

// Handler admits creates whose creator is the caller and updates that leave
// the creator unchanged.
type Handler struct {
	// Projects is the provider scope. Device operations exist only there.
	Projects []string
	// Resources are the operation plurals the handler governs.
	Resources []string
}

// creatorOf reads spec.creator with exact, case-sensitive keys, as the API
// server does. encoding/json into a struct would match keys without regard
// to case and let a later duplicate win.
func creatorOf(raw []byte) (*ops.Creator, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	spec, _ := object["spec"].(map[string]any)
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
	return &ops.Creator{Username: username, UID: uid}, nil
}

// project returns the caller's Milo project. Milo sets these extras when it
// routes a request through a project control plane.
func project(actor authenticationv1.UserInfo) (string, error) {
	for key, expected := range map[string]string{iam.ParentKindExtraKey: "Project", iam.ParentAPIGroupExtraKey: "resourcemanager.miloapis.com"} {
		if values := actor.Extra[key]; len(values) != 1 || values[0] != expected {
			return "", errors.New("an authenticated Milo project context is required")
		}
	}
	values := actor.Extra[iam.ParentNameExtraKey]
	if len(values) != 1 || values[0] == "" || actor.Username == "" || actor.UID == "" {
		return "", errors.New("an authenticated caller and project are required")
	}
	return values[0], nil
}

func (h *Handler) Handle(_ context.Context, request admission.Request) admission.Response {
	if request.Resource.Group != ops.GroupVersion.Group || !slices.Contains(h.Resources, request.Resource.Resource) || request.SubResource != "" {
		return admission.Denied("unsupported operation creator admission request")
	}
	name, err := project(request.UserInfo)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if !slices.Contains(h.Projects, name) {
		return admission.Denied("Device operations exist only in the provider projects")
	}
	switch request.Operation {
	case admissionv1.Create:
		creator, err := creatorOf(request.Object.Raw)
		if err != nil {
			return admission.Denied("the operation could not be decoded")
		}
		if creator == nil || creator.Username != request.UserInfo.Username || creator.UID != request.UserInfo.UID {
			return admission.Denied("spec.creator must be the authenticated caller's username and UID")
		}
		return admission.Allowed("the creator is the caller")
	case admissionv1.Update:
		creator, err := creatorOf(request.Object.Raw)
		if err != nil {
			return admission.Denied("the operation could not be decoded")
		}
		previous, err := creatorOf(request.OldObject.Raw)
		if err != nil || previous == nil || creator == nil || *creator != *previous {
			return admission.Denied("spec.creator cannot change")
		}
		return admission.Allowed("the creator is unchanged")
	}
	return admission.Denied("unsupported operation creator admission request")
}
