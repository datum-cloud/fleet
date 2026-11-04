package manager

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/types"
)

// ExternalEvent is a durable invalidation or observation notification. Sequence
// is opaque but stable within Source. Events are only routing hints; reconcilers
// must re-read and validate the retained authority identified by RecordID.
type ExternalEvent struct {
	Source      string
	Sequence    string
	RecordID    string
	AuthorityID string
	ProjectUID  types.UID
	ObjectUID   types.UID
}

func (e ExternalEvent) Valid() bool {
	return e.Source != "" && e.Sequence != "" && e.RecordID != "" && e.AuthorityID != "" && e.ProjectUID != ""
}

// ReplayableSource must resume strictly after cursor and replay every durable
// event. A lossy process-local channel does not implement this contract.
type ReplayableSource interface {
	Replay(context.Context, string, func(ExternalEvent) error) (nextCursor string, err error)
	Watch(context.Context, string, func(WatchedEvent) error) error
}

// WatchedEvent carries the source checkpoint strictly after Event. Sequence is
// an event identity and must never be interpreted as a replay cursor.
type WatchedEvent struct {
	Event      ExternalEvent
	NextCursor string
}

// CursorStore commits a replay checkpoint only after every event preceding it
// is durably persisted in the inbox. Event sequence identities are not cursors.
type CursorStore interface {
	Load(context.Context, string) (string, error)
	Commit(context.Context, string, string) error
}

type InboxReceipt struct {
	Source   string
	Sequence string
	RecordID string
	WorkID   string
}

func (r InboxReceipt) ValidFor(event ExternalEvent) bool {
	return r.Source == event.Source && r.Sequence == event.Sequence && r.RecordID == event.RecordID && r.WorkID != ""
}

// DurableInbox persists an event before its source cursor may advance. Persist
// must be idempotent for the exact Source/Sequence/RecordID tuple and conflict
// if the same durable key is reused with different content.
type DurableInbox interface {
	Persist(context.Context, ExternalEvent) (InboxReceipt, error)
	Pending(context.Context, string) ([]InboxWork, error)
	Ack(context.Context, InboxReceipt) error
}

// InboxWork remains Pending until a consumer has durably processed Event and
// acknowledges Receipt. Implementations must replay unacknowledged work after
// process restart.
type InboxWork struct {
	Receipt InboxReceipt
	Event   ExternalEvent
}

type ExternalRunner struct {
	Source  ReplayableSource
	Cursors CursorStore
	Inbox   DurableInbox
}

func (r ExternalRunner) ReplayOnce(ctx context.Context, sourceName string) error {
	if r.Source == nil || r.Cursors == nil || r.Inbox == nil || sourceName == "" {
		return ErrEventWiringUnavailable
	}
	cursor, err := r.Cursors.Load(ctx, sourceName)
	if err != nil {
		return err
	}
	nextCursor, err := r.Source.Replay(ctx, cursor, func(event ExternalEvent) error {
		return r.persist(ctx, sourceName, event)
	})
	if err != nil {
		return err
	}
	return r.Cursors.Commit(ctx, sourceName, nextCursor)
}

func (r ExternalRunner) Watch(ctx context.Context, sourceName string) error {
	if r.Source == nil || r.Cursors == nil || r.Inbox == nil || sourceName == "" {
		return ErrEventWiringUnavailable
	}
	cursor, err := r.Cursors.Load(ctx, sourceName)
	if err != nil {
		return err
	}
	return r.Source.Watch(ctx, cursor, func(delivery WatchedEvent) error {
		if delivery.NextCursor == "" {
			return errors.New("watched event has no post-event cursor")
		}
		if err := r.persist(ctx, sourceName, delivery.Event); err != nil {
			return err
		}
		return r.Cursors.Commit(ctx, sourceName, delivery.NextCursor)
	})
}

func (r ExternalRunner) persist(ctx context.Context, sourceName string, event ExternalEvent) error {
	if !event.Valid() || event.Source != sourceName {
		return ErrEventWiringUnavailable
	}
	receipt, err := r.Inbox.Persist(ctx, event)
	if err != nil {
		return err
	}
	if !receipt.ValidFor(event) {
		return errors.New("durable inbox returned a mismatched receipt")
	}
	return nil
}

// Drain delivers durable pending work. consume must return nil only after its
// effect is durably recorded; Ack is therefore safe to replay after crashes.
func (r ExternalRunner) Drain(ctx context.Context, sourceName string, consume func(context.Context, ExternalEvent) error) error {
	if r.Inbox == nil || sourceName == "" || consume == nil {
		return ErrEventWiringUnavailable
	}
	work, err := r.Inbox.Pending(ctx, sourceName)
	if err != nil {
		return err
	}
	for _, item := range work {
		if !item.Event.Valid() || item.Event.Source != sourceName || !item.Receipt.ValidFor(item.Event) {
			return errors.New("durable inbox returned invalid pending work")
		}
		if err := consume(ctx, item.Event); err != nil {
			return err
		}
		if err := r.Inbox.Ack(ctx, item.Receipt); err != nil {
			return err
		}
	}
	return nil
}
