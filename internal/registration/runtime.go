package registration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/manager"
	"go.datum.net/fleet/internal/projectdiscovery"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewHandler(store Store, root client.Client, template *rest.Config, scheme *runtime.Scheme) *Handler {
	store.RequireActive = true
	return &Handler{Store: store, VerifyFence: func(ctx context.Context) error { return VerifyFence(ctx, root, store.Projects) }, Resolve: func(ctx context.Context, name string) (Project, error) {
		identity, err := manager.ResolveProjectIdentity(ctx, root, store.Authority, name)
		if err != nil {
			return Project{}, err
		}
		config, err := projectdiscovery.ProjectRESTConfig(template, name)
		if err != nil {
			return Project{}, err
		}
		api, err := client.New(config, client.Options{Scheme: scheme})
		return Project{Identity: identity, Client: api}, err
	}, Authorize: Authorize, FenceDigest: func(ctx context.Context) (string, error) {
		return VerifiedFenceDigest(ctx, root, store.Projects)
	}}
}

func Authorize(ctx context.Context, project Project, actor authenticationv1.UserInfo, group, resource, name string) error {
	verb := "get"
	if resource == "machinecapacities" {
		verb = "create"
	}
	extra := map[string]authorizationv1.ExtraValue{}
	for key, values := range actor.Extra {
		extra[key] = authorizationv1.ExtraValue(append([]string(nil), values...))
	}
	review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{User: actor.Username, UID: actor.UID, Groups: append([]string(nil), actor.Groups...), Extra: extra, ResourceAttributes: &authorizationv1.ResourceAttributes{Group: group, Resource: resource, Version: "v1alpha1", Namespace: "default", Verb: verb, Name: name}}}
	if err := project.Client.Create(ctx, review); err != nil {
		return errors.New("current registration authorization is unavailable")
	}
	if !review.Status.Allowed || review.Status.Denied || review.Status.EvaluationError != "" {
		return fmt.Errorf("current %s permission on %s is required", verb, resource)
	}
	return nil
}

func (handler *Handler) Recover(ctx context.Context) error {
	secret, ledger, err := handler.Store.Read(ctx)
	if err != nil {
		return err
	}
	changed := false
	var failures error
	for index := range ledger.Entries {
		entry := &ledger.Entries[index]
		outcome, err := handler.observe(ctx, *entry)
		failures = errors.Join(failures, err)
		if entry.Outcome != outcome {
			entry.Outcome = outcome
			changed = true
		}
	}
	if changed {
		failures = errors.Join(failures, handler.Store.Write(ctx, secret, ledger))
	}
	return failures
}

func (handler *Handler) observe(ctx context.Context, entry Entry) (string, error) {
	project, err := handler.Resolve(ctx, entry.Project)
	if err != nil {
		return "Unknown", err
	}
	if project.Identity.UID != entry.ProjectUID {
		return "LifetimeLost", nil
	}
	var namespace corev1.Namespace
	if err := project.Client.Get(ctx, client.ObjectKey{Name: "default"}, &namespace); err != nil && !apierrors.IsNotFound(err) {
		return "Unknown", err
	}
	var device inventory.Device
	if err := project.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: entry.DeviceName}, &device); err != nil && !apierrors.IsNotFound(err) {
		return "Unknown", err
	}
	if namespace.UID != entry.NamespaceUID || !namespace.DeletionTimestamp.IsZero() || device.UID != entry.DeviceUID || !device.DeletionTimestamp.IsZero() || !reflect.DeepEqual(aliases(&device), entry.Aliases) {
		return "LifetimeLost", nil
	}
	var current metal.MachineCapacity
	err = project.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: entry.Name}, &current)
	if apierrors.IsNotFound(err) {
		return "Unknown", nil
	}
	if err != nil {
		return "Unknown", err
	}
	var input metal.MachineCapacity
	if err := json.Unmarshal(entry.Input, &input); err != nil {
		return "Unknown", err
	}
	if current.UID != entry.CapacityUID || !reflect.DeepEqual(current.Spec, input.Spec) || !reflect.DeepEqual(current.Annotations, input.Annotations) || !reflect.DeepEqual(current.Labels, input.Labels) || !current.DeletionTimestamp.IsZero() {
		return "IncarnationConflict", nil
	}
	return "Recorded", nil
}
