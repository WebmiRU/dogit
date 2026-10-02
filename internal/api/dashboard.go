package api

import (
	"net/http"

	"github.com/ewolf/dogit/internal/store"
)

// dashboardResponse is the payload of the home page.
type dashboardResponse struct {
	Stats    store.DashboardStats         `json:"stats"`
	Activity []store.ActivityEntry        `json:"activity"`
	Commits  []store.ProjectActivityEntry `json:"commits"`
	Projects []projectView                `json:"projects"`
}

// handleDashboard aggregates everything the home page shows.
//
// The counts, the activity feed and the commit feed are gathered server-side in a
// handful of queries, so the browser issues one request instead of five and the
// page has no loading waterfall.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	stats, err := s.store.Permissions().DashboardStatsFor(r.Context(), user)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	activity, err := s.store.Events().RecentActivity(r.Context(), user.ID, queryInt(r, "activity", 12, 100))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	commits, err := s.store.Commits().RecentCommitsForUser(r.Context(), user.ID, queryInt(r, "commits", 8, 100))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	projects, levels, err := s.store.Projects().ListVisible(r.Context(), user.ID, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Projects the user can push to come first: they are the ones they are
	// actually working on.
	views := make([]projectView, 0, len(projects))
	for _, p := range projects {
		views = append(views, s.projectToView(p, levels[p.ID]))
	}

	s.writeJSON(w, r, http.StatusOK, dashboardResponse{
		Stats:    *stats,
		Activity: activity,
		Commits:  commits,
		Projects: views,
	})
}

// handleAdminOverview backs the admin area. Only administrators reach it, and it
// deliberately reports counts rather than exposing anything to modify yet.
func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if !user.IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	var users, projects, groups, pipelines, runners int64
	queries := map[string]*int64{
		"users":     &users,
		"projects":  &projects,
		"groups":    &groups,
		"pipelines": &pipelines,
		"runners":   &runners,
	}
	for name, target := range queries {
		if err := s.store.Pool().QueryRow(r.Context(), "SELECT count(*) FROM "+name).Scan(target); err != nil {
			s.writeError(w, r, err)
			return
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"counts": map[string]int64{
			"users": users, "projects": projects, "groups": groups,
			"pipelines": pipelines, "runners": runners,
		},
		// Advertised so the UI can hide sections that have no backend yet
		// instead of offering buttons that would fail.
		"features": map[string]bool{
			"merge_requests": false,
			"ci_cd":          false,
			"issues":         false,
			"runner_jobs":    false,
		},
	})
}
