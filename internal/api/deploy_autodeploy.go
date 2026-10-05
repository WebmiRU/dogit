package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// The flag a deployment module declares for this question, by name.
//
// Written here rather than taken from the manifest's label, because the manifest
// decides what the switch is called and this decides what it means to the core. A
// module is free to call it whatever it likes on the page.
const autoDeployFlag = "auto_deploy"

// autodeployAllowed says whether a push may deploy to this place by itself.
//
// It is a question about one place, asked of the module that will do the deploying, and
// it is a question per place rather than per project: a project with two clusters may
// have one of them automatic and the other only by hand, and the moment to decide is
// per place. By the time a run exists there is a list of places and the question
// "should this have started at all" has already been answered.
//
// Asking *the* module rather than any module that happens to have a row of that name
// matters: two deployment modules can both have a cluster called prod, with different
// kubeconfigs behind them, and a switch on one of them says nothing about the other.
// An answer taken from whichever module came first in a list is not a switch at all.
//
// A place with no row, a row nobody has said anything about, and a module that declares
// no such switch all mean yes: nothing is stopped unless somebody stopped it.
func (s *Server) autodeployAllowed(ctx context.Context, project *models.Project,
	target, cluster string) bool {

	cluster = strings.TrimSpace(cluster)
	if cluster == "" {
		return true
	}

	integration, err := s.deployModule(ctx, target)
	if err != nil || integration == nil {
		// A module that cannot be answered for is not a reason to refuse to deploy, and
		// it is said rather than swallowed: somebody asking why nothing happened wants
		// to know this.
		s.log.Warn("could not read the module for the autodeploy switches",
			"project", projectPath(project), "error", err)
		return true
	}

	asked := map[string]bool{}
	for _, one := range integration.Capabilities.Target.Flags {
		if one.Key == autoDeployFlag {
			asked[one.Key] = one.Default
		}
	}
	if _, has := asked[autoDeployFlag]; !has {
		return true
	}

	var groupID, projectID *uuid.UUID
	if project != nil {
		id := project.ID
		projectID = &id
		groupID = project.GroupID
	}

	resolved, err := s.store.ModuleTargets().Effective(ctx, integration.ID, groupID, projectID)
	if err != nil {
		s.log.Warn("could not read the places for the autodeploy switches",
			"project", projectPath(project), "error", err)
		return true
	}

	for _, row := range resolved.Targets {
		name, _ := stringValue(row.Values["name"])
		if strings.TrimSpace(name) != cluster {
			continue
		}
		// A place switched off entirely is not deployed to at all, so the second switch
		// has nothing to say about it.
		if !row.Enabled {
			continue
		}
		if value, said := row.Flags[autoDeployFlag]; said {
			return value
		}
		return asked[autoDeployFlag]
	}
	return true
}

func projectPath(project *models.Project) string {
	if project == nil {
		return ""
	}
	return project.Path
}

// stringValue reads one of a row's values as text, for a row the core does not know the
// shape of.
func stringValue(raw json.RawMessage) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case nil:
		return "", true
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", false
		}
		return strings.Trim(string(encoded), `"`), true
	}
}

// withoutAutodeployJobs drops the deployments a push is not allowed to start by itself.
//
// Applied only where a run started by itself. A run somebody asked for deploys to
// wherever its configuration says, because the brake exists for the moment somebody
// needs this and still has to be able to deploy something on purpose.
func (s *Server) withoutAutodeployJobs(ctx context.Context, project *models.Project,
	jobs []store.Job) []store.Job {

	kept := make([]store.Job, 0, len(jobs))
	for _, job := range jobs {
		if job.Deploy == nil {
			kept = append(kept, job)
			continue
		}
		target, cluster := deployTargetOf(job)
		if s.autodeployAllowed(ctx, project, target, cluster) {
			kept = append(kept, job)
			continue
		}
		s.log.Info("a deployment was not started by a push: this place does not deploy by itself",
			"project", project.Path, "place", cluster, "job", job.Name)
	}
	return kept
}

// deployTargetOf is where a deployment job goes, out of the job's own copy of what the
// repository said: which module and which place in it.
func deployTargetOf(job store.Job) (target, cluster string) {
	encoded, err := json.Marshal(job.Deploy)
	if err != nil {
		return "", ""
	}
	var spec struct {
		Target  string `json:"target"`
		Cluster string `json:"cluster"`
	}
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return "", ""
	}
	return spec.Target, spec.Cluster
}
