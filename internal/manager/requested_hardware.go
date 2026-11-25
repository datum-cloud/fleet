package manager

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/requestedhardware"
)

// RequestedHardwareProjector is the only writer of Machine
// status.requestedHardware and the RequestResolved condition. The manager
// builds one per engaged project, bound to that project's identity and
// client. It reads the Machine and its MachineProfile and performs no
// allocation, provider, quota, or custody effect, so it runs in every
// configuration, including the default one whose lifecycle handlers are
// blocked.
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
