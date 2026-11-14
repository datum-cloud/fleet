package manager

import (
	"context"
	"errors"
	"sync"
)

var ErrDeadlineAuthority = errors.New("projector cannot own evidence deadlines")

// LifecycleOwner serializes coordinator and projector work for one exact
// ProjectRequest across direct, dependency-route, and deadline controllers.
// The coordinator always runs first and is the only deadline authority.
type LifecycleOwner struct {
	coordinator Handler
	projector   Handler
	deadlines   *DeadlineScheduler

	mu    sync.Mutex
	locks map[ProjectRequest]*requestLock
}

type requestLock struct {
	gate chan struct{}
	refs int
}

func NewLifecycleOwner(coordinator, projector Handler, deadlines *DeadlineScheduler) *LifecycleOwner {
	return &LifecycleOwner{coordinator: coordinator, projector: projector, deadlines: deadlines, locks: map[ProjectRequest]*requestLock{}}
}

func (o *LifecycleOwner) ReconcileEvent(ctx context.Context, request ProjectRequest) Outcome {
	if o == nil || o.coordinator == nil || o.projector == nil || !request.Valid() {
		return Outcome{RetryError: ErrEventWiringUnavailable}
	}
	lock, acquired := o.acquire(ctx, request)
	if !acquired {
		return Outcome{RetryError: ctx.Err()}
	}
	defer o.release(request, lock)

	coordinator := o.coordinator.ReconcileEvent(ctx, request)
	if coordinator.RetryError != nil {
		return coordinator
	}
	projector := o.projector.ReconcileEvent(ctx, request)
	if projector.RetryError != nil {
		return projector
	}
	if projector.Deadline.Mode != DeadlineUnchanged {
		return Outcome{RetryError: ErrDeadlineAuthority}
	}
	if err := o.applyDeadline(request, coordinator.Deadline); err != nil {
		return Outcome{RetryError: err}
	}
	return Outcome{}
}

func (o *LifecycleOwner) acquire(ctx context.Context, request ProjectRequest) (*requestLock, bool) {
	o.mu.Lock()
	lock := o.locks[request]
	if lock == nil {
		lock = &requestLock{gate: make(chan struct{}, 1)}
		lock.gate <- struct{}{}
		o.locks[request] = lock
	}
	lock.refs++
	o.mu.Unlock()
	select {
	case <-lock.gate:
		return lock, true
	case <-ctx.Done():
		o.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(o.locks, request)
		}
		o.mu.Unlock()
		return nil, false
	}
}

func (o *LifecycleOwner) release(request ProjectRequest, lock *requestLock) {
	lock.gate <- struct{}{}
	o.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(o.locks, request)
	}
	o.mu.Unlock()
}

func (o *LifecycleOwner) applyDeadline(request ProjectRequest, directive DeadlineDirective) error {
	switch directive.Mode {
	case DeadlineUnchanged:
		return nil
	case DeadlineSchedule:
		if directive.At.IsZero() || o.deadlines == nil {
			return ErrEventWiringUnavailable
		}
		return o.deadlines.Schedule(request, directive.At)
	case DeadlineCancel:
		if o.deadlines == nil {
			return ErrEventWiringUnavailable
		}
		return o.deadlines.Cancel(request)
	default:
		return ErrEventWiringUnavailable
	}
}
