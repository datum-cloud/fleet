package manager

import (
	"context"
	"sync"
	"time"

	"k8s.io/utils/clock"
)

// DeadlineScheduler emits exactly one event at an evidence expiry deadline.
// It is deliberately not a polling loop. Informer initial Add events rebuild
// deadlines after restart.
type DeadlineScheduler struct {
	clock clock.WithDelayedExecution
	emit  func(ProjectRequest)

	mu      sync.Mutex
	pending map[ProjectRequest]deadlineEntry
	next    uint64
	stopped bool
}

type deadlineEntry struct {
	timer      clock.Timer
	generation uint64
}

func NewDeadlineScheduler(c clock.WithDelayedExecution, emit func(ProjectRequest)) *DeadlineScheduler {
	return &DeadlineScheduler{clock: c, emit: emit, pending: map[ProjectRequest]deadlineEntry{}}
}

func (s *DeadlineScheduler) Schedule(request ProjectRequest, deadline time.Time) error {
	if !request.Valid() || s == nil || s.clock == nil || s.emit == nil || deadline.IsZero() {
		return ErrEventWiringUnavailable
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrEventWiringUnavailable
	}
	if entry, ok := s.pending[request]; ok {
		entry.timer.Stop()
	}
	s.next++
	generation := s.next
	delay := deadline.Sub(s.clock.Now())
	if delay <= 0 {
		delete(s.pending, request)
		// Immediate expiry uses the same serialization boundary as timer expiry.
		// Cancel or a replacement Schedule cannot return while this generation
		// can still emit.
		s.emit(request)
		s.mu.Unlock()
		return nil
	}
	timer := s.clock.AfterFunc(delay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		entry, current := s.pending[request]
		if !current || entry.generation != generation {
			return
		}
		delete(s.pending, request)
		// emit is invoked while serialized with Schedule and Cancel. It must be
		// a bounded queue insertion and must not call back into this scheduler.
		// This ensures a replaced/cancelled generation cannot emit after the
		// mutating call returns.
		s.emit(request)
	})
	s.pending[request] = deadlineEntry{timer: timer, generation: generation}
	s.mu.Unlock()
	return nil
}

func (s *DeadlineScheduler) Cancel(request ProjectRequest) error {
	if s == nil || !request.Valid() {
		return ErrEventWiringUnavailable
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrEventWiringUnavailable
	}
	if entry, ok := s.pending[request]; ok {
		entry.timer.Stop()
		delete(s.pending, request)
	}
	s.mu.Unlock()
	return nil
}

// Start binds the scheduler to the manager lifetime. Stop is serialized with
// Schedule, Cancel, and timer emission, so once Start returns there can be no
// retained deadline or later emission.
func (s *DeadlineScheduler) Start(ctx context.Context) error {
	<-ctx.Done()
	s.Stop()
	return nil
}

// Stop atomically prevents new work, cancels every timer, and drains pending
// state. It waits for any in-flight emission before returning.
func (s *DeadlineScheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	for request, entry := range s.pending {
		entry.timer.Stop()
		delete(s.pending, request)
	}
}
