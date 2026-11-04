package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestLifecycleOwnerSerializesCoordinatorAndProjectorByExactRequest(t *testing.T) {
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "claim-uid"}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	active, maximum := 0, 0
	var order []string
	coordinator := HandlerFunc(func(context.Context, ProjectRequest) Outcome {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		order = append(order, "coordinator")
		mu.Unlock()
		once.Do(func() { close(firstStarted); <-releaseFirst })
		return Outcome{}
	})
	projector := HandlerFunc(func(context.Context, ProjectRequest) Outcome {
		mu.Lock()
		order = append(order, "projector")
		active--
		mu.Unlock()
		return Outcome{}
	})
	owner := NewLifecycleOwner(coordinator, projector, nil)
	done := make(chan struct{}, 2)
	go func() { owner.ReconcileEvent(context.Background(), request); done <- struct{}{} }()
	<-firstStarted
	go func() { owner.ReconcileEvent(context.Background(), request); done <- struct{}{} }()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if len(order) != 1 || maximum != 1 {
		t.Fatalf("concurrent owner order=%v maximum=%d", order, maximum)
	}
	mu.Unlock()
	close(releaseFirst)
	<-done
	<-done
	mu.Lock()
	defer mu.Unlock()
	if maximum != 1 || len(order) != 4 || order[0] != "coordinator" || order[1] != "projector" || order[2] != "coordinator" || order[3] != "projector" {
		t.Fatalf("serialized order=%v maximum=%d", order, maximum)
	}
}

func TestLifecycleOwnerWaitHonorsContextCancellation(t *testing.T) {
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "claim-uid"}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	owner := NewLifecycleOwner(
		HandlerFunc(func(context.Context, ProjectRequest) Outcome {
			select {
			case <-firstStarted:
			default:
				close(firstStarted)
				<-releaseFirst
			}
			return Outcome{}
		}),
		HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} }),
		nil,
	)
	firstDone := make(chan Outcome, 1)
	go func() { firstDone <- owner.ReconcileEvent(context.Background(), request) }()
	<-firstStarted

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second := owner.ReconcileEvent(ctx, request)
	if !errors.Is(second.RetryError, context.Canceled) {
		t.Fatalf("cancelled waiter error=%v", second.RetryError)
	}

	close(releaseFirst)
	if outcome := <-firstDone; outcome.RetryError != nil {
		t.Fatal(outcome.RetryError)
	}
	if len(owner.locks) != 0 {
		t.Fatalf("request lock leaked after cancellation: %#v", owner.locks)
	}
}

func TestLifecycleOwnerOwnsOneShotCoordinatorDeadline(t *testing.T) {
	start := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	clock := clocktesting.NewFakeClock(start)
	request := ProjectRequest{AuthorityID: "milo", ProjectUID: "project", Namespace: "default", Name: "claim", UID: "claim-uid"}
	emitted := make(chan ProjectRequest, 1)
	scheduler := NewDeadlineScheduler(clock, func(request ProjectRequest) { emitted <- request })
	coordinator := HandlerFunc(func(context.Context, ProjectRequest) Outcome {
		return Outcome{Deadline: ScheduleDeadline(start.Add(time.Minute))}
	})
	projector := HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} })
	owner := NewLifecycleOwner(coordinator, projector, scheduler)
	if outcome := owner.ReconcileEvent(context.Background(), request); outcome.RetryError != nil {
		t.Fatal(outcome.RetryError)
	}
	clock.Step(time.Minute)
	select {
	case got := <-emitted:
		if got != request {
			t.Fatalf("deadline request=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not emit")
	}
	unauthorized := NewLifecycleOwner(HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{} }), HandlerFunc(func(context.Context, ProjectRequest) Outcome { return Outcome{Deadline: CancelDeadline()} }), scheduler)
	if outcome := unauthorized.ReconcileEvent(context.Background(), request); !errors.Is(outcome.RetryError, ErrDeadlineAuthority) {
		t.Fatalf("projector deadline error=%v", outcome.RetryError)
	}
}
