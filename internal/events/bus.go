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
	subs   map[int]*subscriber
	nextID int
}

func New(st *store.Store, log *slog.Logger) *Bus {
	return &Bus{store: st, log: log, subs: map[int]*subscriber{}}
}

// subscriber is one consumer's delivery: a channel to read, and a queue behind it that
// grows instead of overflowing.
//
// The queue is the whole point. A fixed channel drops what does not fit, and the claim
// that made that acceptable — "it will catch up via the durable log" — is false for the
// reader of the log itself: RunTail's cursor has already moved past those rows, and
// nothing ever asks for them again. So a replay that outran its subscriber dropped the
// pushes it had not reached yet, silently, and the first pushes after a restart were
// the ones that went missing.
//
// A queue that grows costs nothing when nobody is behind — which is the ordinary case —
// and the alternative is a lost push.
type subscriber struct {
	ch chan models.Event

	mu     sync.Mutex
	queue  []models.Event
	closed bool
	// wake says there is something to move; quiet says stop.
	wake  chan struct{}
	quiet chan struct{}
	done  chan struct{}
}

// offer hands an event over, blocking only for as long as the queue is locked.
func (s *subscriber) offer(ev models.Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
}

// pump moves the queue into the channel, one event at a time, until told to stop.
func (s *subscriber) pump() {
	defer close(s.done)
	for {
		s.mu.Lock()
		if s.closed && len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			// Idle, rather than spinning: an empty queue is the ordinary state.
			select {
			case <-s.wake:
			case <-s.quiet:
				return
			}
			continue
		}
		ev := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()

		select {
		case s.ch <- ev:
		case <-s.quiet:
			return
		}
	}
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
	if buffer <= 0 {
		buffer = 1
	}
	sub := &subscriber{
		ch:    make(chan models.Event, buffer),
		wake:  make(chan struct{}, 1),
		quiet: make(chan struct{}),
		done:  make(chan struct{}),
	}
	go sub.pump()

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = sub
	b.mu.Unlock()

	return sub.ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()

		sub.mu.Lock()
		if sub.closed {
			sub.mu.Unlock()
			return
		}
		sub.closed = true
		sub.mu.Unlock()
		close(sub.quiet)
		<-sub.done
	}
}

func (b *Bus) broadcast(ev models.Event) {
	b.mu.RLock()
	subs := make([]*subscriber, 0, len(b.subs))
	for _, sub := range b.subs {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()

	for _, sub := range subs {
		sub.offer(ev)
		select {
		case sub.wake <- struct{}{}:
		default:
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
