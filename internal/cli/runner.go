package cli

import (
	"context"
	"github.com/ewolf/dogit/internal/app"
)

// Runner runs CI jobs.
//
// Jobs are claimed from the database, executed in an isolated container and
// streamed to disk. The runner is a separate process from the web tier so that a
// busy pipeline cannot exhaust the web server's database pool.
func Runner(ctx context.Context, args []string) error {
	fs := newFlagSet("runner")
	concurrency := fs.Int("concurrency", 0, "jobs to run in parallel (default from DOGIT_RUNNER_CONCURRENCY)")
	if err := parse(fs, args); err != nil {
		return err
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	if *concurrency > 0 {
		a.Cfg.RunnerConcurrency = *concurrency
	}

	a.Log.Info("runner starting",
		"version", Version,
		"concurrency", a.Cfg.RunnerConcurrency,
		"docker", a.Cfg.DockerBinary)

	// Job execution is not implemented yet. Idle instead of exiting so the
	// container stays healthy and the service is in place when it lands.
	a.Log.Warn("CI job execution is not implemented yet; runner idling",
		"planned", "claim pending jobs, build a container, stream logs")

	<-ctx.Done()
	a.Log.Info("runner stopped")
	return nil
}
