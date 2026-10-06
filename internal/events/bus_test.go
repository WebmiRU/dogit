package events

import (
	"testing"

	"github.com/ewolf/dogit/internal/models"
)

// A subscriber that is behind must still be given every event.
//
// This is the shape of a restart: the tail reads the durable log from the beginning and
// delivers faster than anybody reads — a push costs a git read and a few writes, so a
// replay of thousands of events outruns its consumer by thousands. With a fixed channel
// the overflow was dropped, and dropped meant gone: the tail's cursor had already passed
// those rows and nothing reads them again. So the first pushes after a restart were the
// ones that never started a run, and nothing said so.
func TestASubscriberThatIsBehindStillGetsEverything(t *testing.T) {
	b := &Bus{subs: map[int]*subscriber{}}
	events, unsubscribe := b.Subscribe(1)
	defer unsubscribe()

	const total = 500
	for i := 0; i < total; i++ {
		b.broadcast(models.Event{ID: int64(i + 1), Kind: models.EventPush})
	}

	// Read everything, slowly on purpose: the queue behind the channel is what makes
	// this possible at all.
	seen := map[int64]bool{}
	for i := 0; i < total; i++ {
		seen[(<-events).ID] = true
	}
	if len(seen) != total {
		t.Errorf("a slow subscriber received %d of %d events", len(seen), total)
	}
	for i := 1; i <= total; i++ {
		if !seen[int64(i)] {
			t.Errorf("event %d never arrived", i)
			break
		}
	}
}

// And unsubscribing stops the pump rather than leaving it running against a closed
// channel, which is the other way a queue like this goes wrong.
func TestUnsubscribingStopsTheDelivery(t *testing.T) {
	b := &Bus{subs: map[int]*subscriber{}}
	events, unsubscribe := b.Subscribe(1)

	b.broadcast(models.Event{ID: 1, Kind: models.EventPush})
	if got := (<-events).ID; got != 1 {
		t.Fatalf("the first event arrived as %d", got)
	}

	unsubscribe()
	b.broadcast(models.Event{ID: 2, Kind: models.EventPush})

	select {
	case ev := <-events:
		t.Fatalf("an event arrived after unsubscribing: %d", ev.ID)
	default:
	}
}
