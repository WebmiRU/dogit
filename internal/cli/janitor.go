package cli

import (
	"context"
	"time"

	"github.com/ewolf/dogit/internal/app"
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
//   - expired sessions, which would otherwise accumulate indefinitely.
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
	} else if stale > 0 {
		a.Log.Info("modules marked offline", "count", stale)
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
