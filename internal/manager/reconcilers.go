package manager

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"go.datum.net/fleet/internal/controller"
)

var ErrEventWiringUnavailable = errors.New("required event-driven integration is unavailable")

// Outcome makes retry authority explicit. Waiting for a dependency event is not
// an error and never schedules a time-based reconcile.
type Outcome struct {
	RetryError error
}

type Handler interface {
	ReconcileEvent(context.Context, ProjectRequest) Outcome
}

type HandlerFunc func(context.Context, ProjectRequest) Outcome

func (f HandlerFunc) ReconcileEvent(ctx context.Context, request ProjectRequest) Outcome {
	return f(ctx, request)
}

type ProjectVerifier interface {
	VerifyProject(context.Context, controller.ProjectIdentity) error
}

type ProjectVerifierFunc func(context.Context, controller.ProjectIdentity) error

func (f ProjectVerifierFunc) VerifyProject(ctx context.Context, project controller.ProjectIdentity) error {
	return f(ctx, project)
}

type ProjectReconciler struct {
	Project  controller.ProjectIdentity
	Handler  Handler
	Verifier ProjectVerifier
}

func (r ProjectReconciler) Reconcile(ctx context.Context, request ProjectRequest) (reconcile.Result, error) {
	if !r.Project.Valid() || !request.Valid() || !request.SameProject(r.Project.AuthorityID, r.Project.UID) || r.Handler == nil || r.Verifier == nil {
		return reconcile.Result{}, nil
	}
	if err := r.Verifier.VerifyProject(ctx, r.Project); err != nil {
		if errors.Is(err, ErrEventWiringUnavailable) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	outcome := r.Handler.ReconcileEvent(ctx, request)
	return reconcile.Result{}, outcome.RetryError
}
