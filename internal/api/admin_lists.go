package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// handleListUsers is the administrator's list of accounts.
//
// The people on an instance are one of the few things only an administrator may
// see: a list of accounts is a list of who to ask about what, and a member of one
// project has no business enumerating the people on the whole installation.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	users, total, err := s.store.Users().List(r.Context(), store.ListUsersFilter{
		Query:  query,
		Limit:  queryInt(r, "limit", 50, 200),
		Offset: queryInt(r, "page", 1, 10_000) - 1,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	views := make([]map[string]any, 0, len(users))
	for _, user := range users {
		views = append(views, s.adminUserView(r, user))
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"users": views,
		"total": total,
	})
}

// adminUserView is one account as an administrator sees it.
//
// The password hash is not here, and neither is anything derived from it: an
// account list is a page people paste from, and it should carry nothing that a
// reader could use against anybody.
func (s *Server) adminUserView(r *http.Request, user *models.User) map[string]any {
	view := map[string]any{
		"id":         user.ID,
		"username":   user.Username,
		"name":       user.Name,
		"email":      user.Email,
		"is_admin":   user.IsAdmin,
		"created_at": user.CreatedAt,
	}
	if user.LastSignIn != nil {
		view["last_sign_in"] = user.LastSignIn
	}

	// What this account can reach, because "who is an administrator" is only half
	// the question an administrator is asking. Counted rather than listed: the
	// projects themselves are their own pages, and this is a summary meant to be
	// read at a glance.
	projects, levels, err := s.store.Projects().ListVisible(r.Context(), user.ID, "")
	if err == nil {
		view["projects"] = len(levels)
		_ = projects
	}

	keys, err := s.store.SSHKeys().ListByUser(r.Context(), user.ID)
	if err == nil {
		view["ssh_keys"] = len(keys)
	}
	return view
}

// handleListRunners is the machines that run builds.
//
// A runner is a module, so it is listed as one — it registers, it reports what it
// can see, and it is forbidden with the same button as anything else. What this
// page adds is the work: which runner is holding which jobs right now, because a
// runner that is online and busy is a different fact from a runner that is online
// and idle, and only the first one tells anybody anything about why a pipeline is
// taking as long as it is.
func (s *Server) handleListRunners(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integrations, err := s.store.Integrations().List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	runners := make([]map[string]any, 0)
	for _, integration := range integrations {
		if !strings.HasPrefix(integration.Kind, "runner:") {
			continue
		}
		view := s.integrationView(r, integration, nil, nil)

		// The work, counted rather than listed. A runner running four jobs out of a
		// hundred queued is the answer to "is this machine busy", and the hundred is
		// not: the pipeline list shows those.
		running, waiting, err := s.runnerWorkload(r.Context(), integration.ID)
		if err != nil {
			s.log.Debug("read a runner's workload", "runner", integration.Name, "error", err)
		}
		view["running_jobs"] = running
		view["concurrent_limit"] = integration.Capabilities.Capacity
		view["queue_depth"] = waiting

		// How stale the answer is. A page that says "online" without saying when it
		// last heard from the machine is a page that can be confidently wrong.
		if integration.LastSeenAt != nil {
			view["last_seen_ago"] = time.Since(*integration.LastSeenAt).Round(time.Second).String()
		}

		runners = append(runners, view)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"runners": runners})
}

// runnerWorkload is what one runner is doing and what is waiting behind it.
func (s *Server) runnerWorkload(ctx context.Context, runnerID uuid.UUID) (running, waiting int, err error) {
	row := s.store.Pool().QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'pending')
		FROM jobs WHERE runner_id = $1`, runnerID)

	if err := row.Scan(&running, &waiting); err != nil {
		return 0, 0, err
	}
	return running, waiting, nil
}
