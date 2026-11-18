package manager

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/requestedhardware"
)

// RequestedHardwareProjector is the fleet-manager's in-process writer of
// Machine status.requestedHardware and the RequestResolved condition for its
// one configured project. It needs only the routed project API: it reads the
// Machine and its MachineProfile and performs no allocation, provider, quota,
// or private-record effect, so it runs in the default inert manager mode as
// well as synthetic staging.
//
// The projection itself is requestedhardware.Project, shared with the
// multi-project fleet-request-resolver. Exactly one of them may serve a
// project: a manager that shares its project with the resolver must run with
// the projector disabled (see NewMachineProjector).
type RequestedHardwareProjector struct {
	Client  client.Client
	Project controller.ProjectIdentity
}

func (p RequestedHardwareProjector) ReconcileEvent(ctx context.Context, request ProjectRequest) Outcome {
	if p.Client == nil || !p.Project.Valid() || !request.Valid() || !request.SameProject(p.Project.AuthorityID, p.Project.UID) {
		return Outcome{RetryError: ErrEventWiringUnavailable}
	}
	if err := requestedhardware.Project(ctx, p.Client, client.ObjectKey{Namespace: request.Namespace, Name: request.Name}, request.UID); err != nil {
		return Outcome{RetryError: err}
	}
	return Outcome{}
}

// DisabledMachineProjector is the Machine projector used when another
// component owns requested-hardware projection for the project. It never reads
// or writes the Machine, so requestedHardware and RequestResolved keep exactly
// one writer.
type DisabledMachineProjector struct{}

func (DisabledMachineProjector) ReconcileEvent(context.Context, ProjectRequest) Outcome {
	return Outcome{}
}

// NewMachineProjector returns the manager's Machine projector. enabled keeps
// today's behaviour: the manager pins requested hardware for its project.
// Disabling it hands that projection to the fleet-request-resolver; the
// resolver and an enabled projector must never serve the same project.
func NewMachineProjector(enabled bool, c client.Client, project controller.ProjectIdentity) Handler {
	if !enabled {
		return DisabledMachineProjector{}
	}
	return RequestedHardwareProjector{Client: c, Project: project}
}
