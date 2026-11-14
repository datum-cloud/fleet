package manager

import (
	"context"
	"errors"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"go.datum.net/fleet/internal/controller"
)

var ErrEventWiringUnavailable = errors.New("required event-driven integration is unavailable")

// Outcome makes retry authority explicit. Waiting for a dependency event is not
// an error and never schedules a time-based reconcile.
type Outcome struct {
	RetryError error
	Deadline   DeadlineDirective
}

type DeadlineMode string

const (
	DeadlineUnchanged DeadlineMode = ""
	DeadlineSchedule  DeadlineMode = "Schedule"
	DeadlineCancel    DeadlineMode = "Cancel"
)

// DeadlineDirective is owned by the lifecycle coordinator. It requests one
// exact evidence-expiry event; it is not a polling interval.
type DeadlineDirective struct {
	Mode DeadlineMode
	At   time.Time
}

func ScheduleDeadline(at time.Time) DeadlineDirective {
	return DeadlineDirective{Mode: DeadlineSchedule, At: at}
}

func CancelDeadline() DeadlineDirective {
	return DeadlineDirective{Mode: DeadlineCancel}
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
	if !r.Project.Valid() || r.Handler == nil || r.Verifier == nil {
		return reconcile.Result{}, ErrEventWiringUnavailable
	}
	if !request.Valid() || !request.SameProject(r.Project.AuthorityID, r.Project.UID) {
		return reconcile.Result{}, nil
	}
	if err := r.Verifier.VerifyProject(ctx, r.Project); err != nil {
		return reconcile.Result{}, err
	}
	outcome := r.Handler.ReconcileEvent(ctx, request)
	return reconcile.Result{}, outcome.RetryError
}
