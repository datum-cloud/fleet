package manager

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/tenancy"
)

var _ tenancy.Resolver = (*Projects)(nil)

// ProjectConsumer is an engaged project as a consumer.
func ProjectConsumer(project controller.ProjectIdentity, api client.Client) tenancy.Consumer {
	return tenancy.Consumer{Ref: tenancy.Project(project.RouteName), UID: project.UID, Client: api}
}

// Resolve resolves a Milo Project consumer to the project engaged under its
// name. Project discovery is the only source of a project's identity, so a
// project that is not engaged is unavailable, not gone.
func (p *Projects) Resolve(_ context.Context, ref fleetv1.TypedReference) (tenancy.Consumer, error) {
	if err := tenancy.Supported(ref); err != nil {
		return tenancy.Consumer{}, err
	}
	if ref.Kind != tenancy.ProjectKind {
		return tenancy.Consumer{}, fmt.Errorf("%w: the managed mode serves Projects only", tenancy.ErrUnsupportedConsumer)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	var found *ProjectRuntime
	for _, runtime := range p.byCluster {
		if runtime.Binding.Project.RouteName != ref.Name {
			continue
		}
		if found != nil {
			return tenancy.Consumer{}, fmt.Errorf("%w: more than one engaged project is named %s", tenancy.ErrConsumerUnavailable, ref.Name)
		}
		found = runtime
	}
	if found == nil {
		return tenancy.Consumer{}, fmt.Errorf("%w: project %s is not engaged", tenancy.ErrConsumerUnavailable, ref.Name)
	}
	return ProjectConsumer(found.Binding.Project, found.Binding.Client), nil
}
