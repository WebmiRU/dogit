package postreceive

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/events"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/hooks"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

const zeroSHA = "0000000000000000000000000000000000000000"

// Options describes the push being processed.
type Options struct {
	// RepoPath is the bare repository on disk.
	RepoPath string
	// ProjectPath is the canonical project path, e.g. "group/project".
	ProjectPath string
	// ProjectID skips the project lookup when already known.
	ProjectID string
	// ActorID is the pushing user, when the transport identified one.
	ActorID string
	Logger  *slog.Logger
}

// Run processes post-receive input read from r.
//
// Ordering matters: git has already written the objects, and only then do we
// touch the database. If the database write fails the commits are still safe on
// disk and the next ref update catches up, whereas the reverse order could
// leave a database row pointing at a commit that does not exist.
func Run(ctx context.Context, st *store.Store, git *gitx.Git, bus *events.Bus, r io.Reader, opts Options) error {
	updates, err := hooks.ParsePostReceive(r)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	projectID, err := resolveProject(ctx, st, opts)
	if err != nil {
		return err
	}

	// Debug, not Info: this hook's stderr is the client's "remote:" stream, so
	// anything logged here is shown to whoever pushed.
	log.Debug("post-receive", "project", opts.ProjectPath, "refs", len(updates))

	ranges := make([]hooks.CommitRange, 0, len(updates))
	commits := []models.Commit{}

	for _, u := range updates {
		if u.IsDelete() {
			log.Debug("ref deleted", "ref", u.Ref, "project", opts.ProjectPath)
			continue
		}

		cr := hooks.CommitRange{
			Ref:    u.Ref,
			Branch: u.Branch(),
			NewSHA: u.NewSHA,
		}
		if u.OldSHA != zeroSHA {
			cr.OldSHA = u.OldSHA
			cr.Shas = newCommits(ctx, git, opts.RepoPath, u.NewSHA, u.OldSHA)
		} else {
			// New ref: everything reachable from the tip that we have not stored.
			cr.Shas = newCommits(ctx, git, opts.RepoPath, u.NewSHA, "")
		}

		if snaps, err := git.SnapshotCommits(ctx, opts.RepoPath, cr.Shas); err != nil {
			log.Warn("post-receive: could not read commit metadata", "ref", u.Ref, "error", err)
		} else {
			for _, s := range snaps {
				commits = append(commits, models.Commit{
					SHA:            s.SHA,
					ProjectID:      projectID,
					Ref:            u.Ref,
					Branch:         cr.Branch,
					AuthorName:     s.AuthorName,
					AuthorEmail:    s.AuthorEmail,
					CommitterName:  s.CommitterName,
					CommitterEmail: s.CommitterEmail,
					Message:        s.Message,
					Timestamp:      s.Timestamp,
				})
			}
		}
		ranges = append(ranges, cr)
	}

	log.Debug("post-receive snapshots", "commits", len(commits), "ranges", len(ranges))

	if len(commits) > 0 {
		if err := st.Commits().Upsert(ctx, commits); err != nil {
			// Log and continue: the events below still fire, and the next push
			// re-snapshots with ON CONFLICT DO NOTHING.
			log.Error("post-receive: could not store commit snapshots",
				"error", err, "count", len(commits))
		}
	}

	if bus == nil || len(ranges) == 0 {
		return nil
	}

	payload := hooks.Payload{
		ProjectID:   projectID.String(),
		ProjectPath: opts.ProjectPath,
		UserID:      opts.ActorID,
		RefUpdates:  ranges,
	}
	if err := bus.Publish(ctx, models.EventPush, &projectID, actorUUID(opts.ActorID), payload); err != nil {
		// Never fail the push because the event bus is unavailable.
		log.Error("post-receive: could not publish the push event", "error", err)
	}
	return nil
}

func actorUUID(raw string) *uuid.UUID {
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	return &id
}

func resolveProject(ctx context.Context, st *store.Store, opts Options) (uuid.UUID, error) {
	if opts.ProjectID != "" {
		return uuid.Parse(opts.ProjectID)
	}
	p, err := st.Projects().ByPath(ctx, strings.TrimSuffix(opts.ProjectPath, ".git"))
	if err != nil {
		return uuid.Nil, err
	}
	return p.ID, nil
}

// maxSnapshotCommits bounds the work for a huge initial push.
const maxSnapshotCommits = 10_000

// newCommits lists commits reachable from newSHA but not from oldSHA.
func newCommits(ctx context.Context, git *gitx.Git, repoPath, newSHA, oldSHA string) []string {
	args := []string{"rev-list", "--max-count=" + strconv.Itoa(maxSnapshotCommits), newSHA}
	if oldSHA != "" {
		args = append(args, "--not", oldSHA)
	}
	out, err := git.Run(ctx, repoPath, nil, args...)
	if err != nil {
		// Returning an empty list means "nothing new recorded", which the caller
		// treats as a no-op; the error itself is logged by the caller.
		slog.Warn("post-receive: rev-list failed",
			"repo", repoPath, "new", newSHA, "old", oldSHA, "error", err)
		return nil
	}

	shas := []string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			shas = append(shas, l)
		}
	}
	return shas
}
