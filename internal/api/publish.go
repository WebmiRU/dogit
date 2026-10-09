package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
)

// publishCommitEvent records that something changed in a repository.
//
// Every write path that other clients may care about goes through here: the git
// hook after a push, and the web editor after a save. That is what lets a page
// learn about a change without polling the repository, and it is the only thing a
// future WebSocket has to forward.
//
// The payload is a hint, not a record: it says what happened and where, and the
// client refetches through the REST endpoints it already has. Anything that grows
// into a list — commits, files, statuses — would be wrong for every page but one.
func (s *Server) publishCommitEvent(r *http.Request, rc *repoContext, actor *models.User, branch, sha, message string) {
	s.publish(r.Context(), rc, actor, models.EventPush, map[string]any{
		"source":  "web",
		"ref":     "refs/heads/" + branch,
		"branch":  branch,
		"sha":     sha,
		"message": firstLine(message),
	})
}

// publish appends an event to the durable log and fans it out to live subscribers.
func (s *Server) publish(ctx context.Context, rc *repoContext, actor *models.User, kind models.EventKind, payload map[string]any) {
	if s.events == nil {
		return
	}

	var actorID *uuid.UUID
	if actor != nil {
		id := actor.ID
		actorID = &id
	}
	projectID := rc.Project.ID

	if err := s.events.Publish(ctx, kind, &projectID, actorID, payload); err != nil {
		// An event that cannot be recorded must not fail the operation that caused
		// it: the commit is already in the repository, and the next push re-announces
		// the same state.
		s.log.Warn("publish event", "kind", kind, "project", rc.Project.Path, "error", err)
	}
}

// firstLine keeps a commit subject to one line; the full message is in git.
func firstLine(message string) string {
	for i := 0; i < len(message); i++ {
		if message[i] == '\n' {
			return message[:i]
		}
	}
	return message
}
