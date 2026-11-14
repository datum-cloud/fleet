package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/controller"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
)

var ErrProjectIdentityUnavailable = errors.New("project identity is unavailable")

// ResolveProjectIdentity turns the operator-facing Milo project name into the
// immutable project lifetime identity used by Fleet's custody records. The UID
// is always read from Milo; accepting it as deployment configuration would let
// stale or mistyped configuration bind work to the wrong project lifetime.
func ResolveProjectIdentity(ctx context.Context, reader client.Reader, authorityID, projectName string) (controller.ProjectIdentity, error) {
	authorityID = strings.TrimSpace(authorityID)
	projectName = strings.TrimSpace(projectName)
	if reader == nil || authorityID == "" || projectName == "" {
		return controller.ProjectIdentity{}, ErrProjectIdentityUnavailable
	}

	project := &resourcemanagerv1alpha1.Project{}
	if err := reader.Get(ctx, client.ObjectKey{Name: projectName}, project); err != nil {
		return controller.ProjectIdentity{}, fmt.Errorf("%w: resolve Milo Project %q: %v", ErrProjectIdentityUnavailable, projectName, err)
	}
	if project.Name != projectName || project.UID == "" || !project.DeletionTimestamp.IsZero() {
		return controller.ProjectIdentity{}, fmt.Errorf("%w: Milo Project %q has no active immutable identity", ErrProjectIdentityUnavailable, projectName)
	}

	identity := controller.ProjectIdentity{AuthorityID: authorityID, RouteName: projectName, UID: project.UID}
	if !identity.Valid() {
		return controller.ProjectIdentity{}, ErrProjectIdentityUnavailable
	}
	return identity, nil
}
