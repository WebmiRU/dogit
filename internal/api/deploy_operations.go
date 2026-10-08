package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/store"
)

// handleProjectDeployOperations lists a project's deployments, running ones first.
//
// An ordinary request rather than something assembled out of events, and that is the whole point
// of it. A page built from the live feed knows only about what happened while somebody was
// watching: open it an hour into a rollout and the rollout is not there, and the page says so by
// drawing nothing. The feed then only has to add to this, never to provide it.
//
// Two lists in one answer because the page draws two: everything still under way, without a limit,
// and then the most recent finished things. A running deployment is not paged out behind a hundred
// older rows — that is a deployment somebody is waiting for.
func (s *Server) handleProjectDeployOperations(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Ten finished by default: enough to fill a page of cards and few enough that somebody
	// scrolls rather than gives up. Zero is a way of saying "none of the old ones", which is
	// not the same as leaving it out and getting ten.
	finished := 10
	if raw := r.URL.Query().Get("finished"); raw != "" {
		if asked, err := strconv.Atoi(raw); err == nil {
			finished = asked
		}
	}

	// One parameter, named for what it is. It was two, `cluster` and `namespace`, and both were
	// guesses about how a place is written down: the step names a place, and the two fields were
	// the shape the configuration used to have. A request that filters on a field the step never
	// sets does not return the wrong rows — it returns none, and a page with no rows on it is
	// indistinguishable from a page where nothing has ever been deployed.
	place := strings.TrimSpace(r.URL.Query().Get("place"))

	operations, err := s.store.Pipelines().DeployOperations(r.Context(), project.ID, place, finished)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	active := []map[string]any{}
	complete := []map[string]any{}
	for _, one := range operations {
		// Three states and not two: a deploy step nobody has claimed has neither begun nor
		// ended, and it belongs on neither list. The core knows it — the row exists — and a
		// card for it would draw an empty step list and no end time.
		switch {
		case one.Running():
			active = append(active, deployOperationView(one))
		case one.Finished():
			complete = append(complete, deployOperationView(one))
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"active":   active,
		"finished": complete,
	})
}

// deployOperationView is one operation as the page is told about it.
//
// The identity is the job's id, and it is first because everything else on the card hangs off it:
// every event about this deployment carries it, and it is the only thing that tells two
// deployments apart. Milliseconds are kept because two deployments in the same second are ordinary
// and a page whose cards swap places between two loads cannot be read.
func deployOperationView(one store.DeployOperation) map[string]any {
	return map[string]any{
		"job_id": one.JobID,
		// The place by name, which is how a deploy step says it and how the page knows its
		// rows. Cluster and Namespace come along for records written the older way, and are
		// empty for the current ones — a page that drew "cluster/namespace" on every card
		// would print a slash and two blanks on every deployment made since the change.
		"place":       one.Place,
		"cluster":     one.Cluster,
		"namespace":   one.Namespace,
		"status":      one.Status,
		"name":        one.Name,
		"error":       one.Error,
		"started_at":  millisOf(one.StartedAt),
		"finished_at": millisOf(one.FinishedAt),
		"running":     one.StartedAt != nil && one.FinishedAt == nil,
	}
}

// millisOf is a moment as a number, or nil when there was no such moment.
//
// Unix milliseconds rather than a formatted string: the page sorts and compares these, and a page
// that has to parse a date to decide which card is on top is a page whose order depends on a date
// format. Nil for the absent case because zero is a moment — 1970 — and a card dated 1970 is a
// card claiming something happened when nothing did.
func millisOf(at *time.Time) any {
	if at == nil {
		return nil
	}
	return at.UnixMilli()
}
