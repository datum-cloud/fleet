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

// ClusterProjectReconciler reconciles a ProjectRequest only through the
// runtime engaged under its cluster name, and only when that runtime's
// identity matches the request. A request for a project that is not engaged,
// or whose cluster now holds another project lifetime, is dropped with no
// effect: no handler runs, no private record or status is written.
type ClusterProjectReconciler struct {
	Projects *Projects
	Owner    func(*ProjectRuntime) *LifecycleOwner
}

func (r ClusterProjectReconciler) Reconcile(ctx context.Context, request ProjectRequest) (reconcile.Result, error) {
	if r.Projects == nil || r.Owner == nil {
		return reconcile.Result{}, ErrEventWiringUnavailable
	}
	runtime := r.Projects.For(request)
	if runtime == nil || !runtime.acquire() {
		return reconcile.Result{}, nil
	}
	defer runtime.reconcileMu.RUnlock()
	return ProjectReconciler{Project: runtime.Binding.Project, Handler: r.Owner(runtime), Verifier: runtime.Verifier}.Reconcile(ctx, request)
}

// ClusterRouteReconciler resolves a dependency event through the engaged
// project's own mappers and reader.
type ClusterRouteReconciler struct {
	Projects *Projects
	Owner    func(*ProjectRuntime) *LifecycleOwner
}

func (r ClusterRouteReconciler) Reconcile(ctx context.Context, request RouteRequest) (reconcile.Result, error) {
	if r.Projects == nil || r.Owner == nil {
		return reconcile.Result{}, ErrEventWiringUnavailable
	}
	runtime := r.Projects.Get(request.ClusterName)
	if runtime == nil || !runtime.acquire() {
		return reconcile.Result{}, nil
	}
	defer runtime.reconcileMu.RUnlock()
	return RouteReconciler{Project: runtime.Binding.Project, Verifier: runtime.Verifier, Mappers: runtime.Mappers, Handler: r.Owner(runtime)}.Reconcile(ctx, request)
}
