package registration

import (
	"context"
	"errors"
	"fmt"

	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/projectdiscovery"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewHandler wires the handler to Milo. The root client resolves Project
// lifetimes; the template is re-addressed to each provider project's control
// plane for uncached Device and capacity reads and SubjectAccessReviews.
func NewHandler(authority string, projects []string, root client.Client, template *rest.Config, scheme *runtime.Scheme) *Handler {
	return &Handler{Projects: projects, Resolve: func(ctx context.Context, name string) (Project, error) {
		identity, err := manager.ResolveProjectIdentity(ctx, root, authority, name)
		if err != nil {
			return Project{}, err
		}
		config, err := projectdiscovery.ProjectRESTConfig(template, name)
		if err != nil {
			return Project{}, err
		}
		api, err := client.New(config, client.Options{Scheme: scheme})
		return Project{Identity: identity, Client: api}, err
	}, Authorize: Authorize}
}

// Authorize evaluates the caller's own permission with a SubjectAccessReview
// carrying the caller's username, UID, groups and extras. The service's
// credentials evaluate the review; they never stand in for the caller.
func Authorize(ctx context.Context, project Project, actor authenticationv1.UserInfo, verb, group, resource, name string) error {
	extra := map[string]authorizationv1.ExtraValue{}
	for key, values := range actor.Extra {
		extra[key] = authorizationv1.ExtraValue(append([]string(nil), values...))
	}
	review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{User: actor.Username, UID: actor.UID, Groups: append([]string(nil), actor.Groups...), Extra: extra, ResourceAttributes: &authorizationv1.ResourceAttributes{Group: group, Resource: resource, Version: "v1alpha1", Namespace: InventoryNamespace, Verb: verb, Name: name}}}
	if err := project.Client.Create(ctx, review); err != nil {
		return errors.New("current registration authorization is unavailable")
	}
	if !review.Status.Allowed || review.Status.Denied || review.Status.EvaluationError != "" {
		return fmt.Errorf("current %s permission on %s is required", verb, resource)
	}
	return nil
}
