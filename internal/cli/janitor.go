package cli

import (
	"context"
	"time"

	"github.com/ewolf/dogit/internal/app"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// janitorInterval is how often background housekeeping runs. It is deliberately
// slow: nothing here is urgent, and a busy instance should not pay for it.
const janitorInterval = time.Minute

// runJanitor performs the housekeeping that would otherwise leak.
//
// Three things accumulate in the database over time:
//
//   - modules that stopped sending heartbeats, which would otherwise stay
//     "online" forever and be handed out tokens;
//   - short-lived module tokens, which are worthless once expired;
//   - expired sessions, which would otherwise accumulate indefinitely;
//   - jobs whose runner stopped answering, which would otherwise leave their
//     pipelines running for ever.
//
// Running this in the web tier means any instance can do it, and several
// instances doing it at once is harmless because every statement is idempotent.
func runJanitor(ctx context.Context, a *app.App) {
	expiredSessions, err := a.Store.Sessions().DeleteExpired(ctx)
	if err != nil {
		a.Log.Warn("remove expired sessions", "error", err)
	} else if expiredSessions > 0 {
		a.Log.Debug("removed expired sessions", "count", expiredSessions)
	}

	expiredTokens, err := a.Store.IntegrationTokens().DeleteExpired(ctx)
	if err != nil {
		a.Log.Warn("remove expired module tokens", "error", err)
	} else if expiredTokens > 0 {
		a.Log.Debug("removed expired module tokens", "count", expiredTokens)
	}

	stale, err := a.Store.Integrations().MarkStaleOffline(ctx, 90*time.Second)
	if err != nil {
		a.Log.Warn("mark modules offline", "error", err)
	} else if len(stale) > 0 {
		a.Log.Info("modules marked offline", "count", len(stale))
		// Said aloud as well as written: an administrator's page follows the event
		// feed, and without this the page would show a module as online long after
		// its machine stopped answering.
		for _, one := range stale {
			a.PublishInstanceEvent(ctx, models.EventModuleUpdated,
				map[string]any{"kind": one.Kind, "name": one.Name, "status": "offline"})
		}
	}

	// A removal whose module has gone quiet is marked stalled rather than left
	// running forever. It is not failed: the log keeps whatever the module managed
	// to write, which is usually the line explaining where it stopped, and the
	// administrator decides what to do about it.
	// A job marked running is a claim that some machine is working on it. When the
	// machine is gone the claim is still there, and the pipeline waits on it for
	// ever — the one state a pipeline must never be in, because an operator who
	// restarts a runner should not have to go and repair the queue by hand.
	staleJobs, err := a.Store.Pipelines().ReapStaleJobs(ctx, store.JobStaleAfter)
	if err != nil {
		a.Log.Warn("reclaim jobs whose runner went quiet", "error", err)
	} else if len(staleJobs) > 0 {
		a.Log.Warn("jobs interrupted: their runners stopped reporting",
			"jobs", len(staleJobs), "after", store.JobStaleAfter.String())
	}

	stalled, err := a.Store.ModuleUninstall().Stale(ctx, store.StalledAfter)
	if err != nil {
		a.Log.Warn("stall module removals", "error", err)
	} else if stalled > 0 {
		a.Log.Warn("module removals stalled", "count", stalled,
			"silence", store.StalledAfter.String())
	}
}

// interruptOpenRemovals runs once at start-up.
//
// The only removals running when the core was stopped are the ones the core
// stopped running, and their outcome is unknown rather than bad: the module may
// well have finished deleting, and nobody heard. Saying so is what lets an
// administrator decide whether to run it again, which the module must therefore
// be able to survive.
func interruptOpenRemovals(ctx context.Context, a *app.App) {
	interrupted, err := a.Store.ModuleUninstall().InterruptOpen(ctx)
	if err != nil {
		a.Log.Warn("record interrupted module removals", "error", err)
		return
	}
	if interrupted > 0 {
		a.Log.Warn("module removals were interrupted by a restart", "count", interrupted)
	}
}

// startJanitor runs runJanitor on a ticker until the context is cancelled.
func startJanitor(ctx context.Context, a *app.App) {
	go func() {
		ticker := time.NewTicker(janitorInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runJanitor(ctx, a)
			}
		}
	}()
}
