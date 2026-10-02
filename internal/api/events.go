package api

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/store"
)

// eventPollIntervalMS is the cadence the client falls back to while no WebSocket
// transport is available. It lives in the response so the number is decided in one
// place rather than in every client.
const eventPollIntervalMS = 4000

// handleEventStream serves the event feed the frontend follows.
//
// Today it is a plain cursor-based read; the WebSocket server will speak the same
// shape. The payload is deliberately a signal and not the record itself: a page
// showing the commits of a branch does not need them pushed to it, it refetches
// through the REST endpoint it already has, which keeps its caching and its
// filtering. Pushing data would only ever be correct for one page.
//
// ?since=<id> resumes from a cursor, so a client that reloads or reconnects neither
// misses an event nor replays what it has already seen.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	afterID := int64(queryInt(r, "since", 0, 1<<62))
	limit := queryInt(r, "limit", 50, 200)

	// A project filter lets a page subscribe narrowly. It is checked here rather
	// than left to the query, because refusing to answer at all is clearer than an
	// empty feed that looks like "nothing is happening".
	var projectID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("project")); raw != "" {
		project, err := s.resolveProjectRef(r.Context(), raw)
		if err != nil {
			s.writeError(w, r, errNotFoundf("project %q does not exist", raw))
			return
		}

		full, err := s.store.Projects().ByID(r.Context(), *project)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		allowed, err := s.store.Permissions().Can(r.Context(), user, full, store.ActionReadProject)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !allowed {
			// Reported as missing rather than forbidden: a caller who cannot see the
			// project should not learn that it exists.
			s.writeError(w, r, errNotFound("project does not exist"))
			return
		}
		projectID = project
	}

	entries, err := s.store.Events().VisibleSince(r.Context(), user.ID, projectID, afterID, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The cursor is the highest id seen, so a client filtering on one project still
	// advances past events meant for others instead of re-reading them forever.
	cursor := afterID
	for _, entry := range entries {
		if entry.ID > cursor {
			cursor = entry.ID
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"events":   entries,
		"cursor":   cursor,
		"retry_ms": eventPollIntervalMS,
	})
}
