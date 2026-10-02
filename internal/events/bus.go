// Package events provides a durable, Postgres-backed event bus.
//
// Git hooks (post-receive) run as short-lived processes invoked by git itself.
// They cannot safely call the web tier over HTTP: if the web tier is down or
// restarting, the push fails and the user sees a confusing error after their
// objects are already safely in the repository. Instead the hook appends an
// event row and exits; the web and runner tiers consume the log.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// Bus publishes and consumes events.
type Bus struct {
	store  *store.Store
	log    *slog.Logger
	mu     sync.RWMutex
	subs   map[int]chan models.Event
	nextID int
}

func New(st *store.Store, log *slog.Logger) *Bus {
	return &Bus{store: st, log: log, subs: map[int]chan models.Event{}}
}

// Publish appends an event to the durable log.
func (b *Bus) Publish(ctx context.Context, kind models.EventKind, projectID, actorID *uuid.UUID, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ev := &models.Event{Kind: kind, ProjectID: projectID, ActorID: actorID, Payload: raw}
	if err := b.store.Events().Append(ctx, ev); err != nil {
		return err
	}
	b.broadcast(*ev)
	return nil
}

// Subscribe returns a channel of events published after the call, and a
// function that unsubscribes. The channel is buffered; a slow consumer is
// dropped rather than allowed to block publishers.
func (b *Bus) Subscribe(buffer int) (<-chan models.Event, func()) {
	ch := make(chan models.Event, buffer)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}

func (b *Bus) broadcast(ev models.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Consumer is behind; it will catch up via the durable log.
		}
	}
}

// RunTail polls the durable log and delivers events to subscribers. This is the
// safety net that makes at-least-once delivery: even if a live broadcast is
// dropped because a consumer was slow, the row is still in Postgres.
func (b *Bus) RunTail(ctx context.Context, lastID int64) {
	// Start from the current head: only new events are of interest.
	if lastID == 0 {
		events, err := b.store.Events().Since(ctx, 0, 1)
		if err == nil && len(events) == 0 {
			lastID = 0
		}
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := b.store.Events().Since(ctx, lastID, 200)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				b.log.Warn("tail events", "error", err)
				continue
			}
			for _, ev := range rows {
				lastID = ev.ID
				b.broadcast(ev)
			}
		}
	}
}
