// This module's settings, as the core holds them and as this build acts on them.
//
// The point of this file is that a setting is not decoration. A runner that declares a number
// in its manifest and never reads it has built a panel that says something false, and the
// person reading it has no way to tell. So every setting declared in the manifest is read
// here, and a setting the core holds that this build does not recognise is reported rather
// than ignored — an unknown setting is somebody's work that stopped taking effect.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/ewolf/runner/internal/core"
)

// Settings is one read, and what changed.
type Settings struct {
	// Concurrency is how many jobs at once. Atomic because it changes while jobs are running
	// and is read by the loop that decides whether to ask for more work.
	Concurrency atomic.Int64
	// KeepCheckout is whether a finished job's working copy is left behind.
	KeepCheckout atomic.Bool
	// JobTimeout ends a job that will not end, so that it stops holding a slot.
	JobTimeout atomic.Int64
	// Unknown lists the keys the core holds that this build does not act on. Reported in the
	// heartbeat, because a setting nobody applies is a setting somebody believes they set.
	Unknown atomic.Value // []string
}

// Read fetches the settings and applies them, saying which ones changed.
//
// Best effort. A runner whose core is unreachable keeps the values it has — a number that
// resets to its default because one request failed would make an administrator's change
// disappear and reappear at random, which is a worse experience than a setting that takes
// effect thirty seconds late.
func (s *Settings) Read(ctx context.Context, client *core.Client) error {
	effective, schema, err := client.Settings(ctx)
	if err != nil {
		return err
	}

	known := map[string]bool{}
	declare := func(key string) { known[key] = true }

	if value, ok := numberOf(effective["concurrency"]); ok && value >= 1 {
		before := s.Concurrency.Swap(value)
		declare("concurrency")
		if before != 0 && before != value {
			log.Printf("runner: concurrency is now %d, was %d", value, before)
		}
	}
	if value, ok := effective["keep_checkout"].(bool); ok {
		s.KeepCheckout.Store(value)
		declare("keep_checkout")
	}
	if value, ok := numberOf(effective["job_timeout"]); ok && value >= 1 {
		s.JobTimeout.Store(int64(time.Duration(value) * time.Second))
		declare("job_timeout")
	}

	var unknown []string
	for key := range effective {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(schema) > 0 {
		s.Unknown.Store(unknown)
	}
	return nil
}

// Seed puts the built-in defaults in place, so that the first heartbeat has something true to
// report before the first read has happened.
func (s *Settings) Seed(concurrency int, jobTimeout time.Duration) {
	s.Concurrency.Store(int64(concurrency))
	s.KeepCheckout.Store(true)
	s.JobTimeout.Store(int64(jobTimeout))
	s.Unknown.Store([]string(nil))
}

// UnknownKeys is what the core holds that this build does not act on.
func (s *Settings) UnknownKeys() []string {
	held, _ := s.Unknown.Load().([]string)
	return held
}

// numberOf reads a number the core may have sent as any of the JSON shapes a number can take.
//
// The core unmarshals settings into `any`, so a value declared as an int can arrive as a
// float64, an int, a json.Number or a string depending on how it was stored and whether it
// came from a default or from somebody typing it into a panel. Reading only one of those is a
// setting that works until somebody edits it by hand.
func numberOf(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		// The core stores a setting as raw JSON, so a number can arrive still wrapped in the
		// type that says it has not been decoded. Reading only float64 and missing this is a
		// setting that works until somebody saves it from a different place.
		if number, err := typed.Int64(); err == nil {
			return number, true
		}
	case string:
		// A number somebody typed into a text box is still a number somebody meant.
		var number int64
		if _, err := fmt.Sscanf(typed, "%d", &number); err == nil {
			return number, true
		}
	}
	return 0, false
}
