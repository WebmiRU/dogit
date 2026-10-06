package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// The flag that says a place deploys by itself, and the row field it lives in.
//
// The same word the core gives a settings row: this is one of the two things a scope
// below the one that wrote a cluster is allowed to say about it without rewriting it.
// The other is whether it is in use at all.
// autodeployAllowed says whether a push may deploy to this place by itself.
//
// It is a question about one place, asked of the module that will do the deploying. It
// cannot be a question about the project, because a project with two clusters may have
// one of them automatic and the other only by hand, and that is the question actually
// being asked.
//
// Asking *the* module rather than any module with a cluster of that name matters: two
// deployment modules can both have a cluster called prod, with different kubeconfigs
// behind them, and a switch on one of them says nothing about the other.
//
// A place that is not written down at all, a place nobody has said anything about, and
// a module that has no such row all mean yes: nothing is stopped unless somebody
// stopped it.
func (s *Server) autodeployAllowed(ctx context.Context, project *models.Project,
	target, place string) bool {

	place = strings.TrimSpace(place)
	if place == "" {
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

	var groupID, projectID *uuid.UUID
	if project != nil {
		id := project.ID
		projectID = &id
		groupID = project.GroupID
	}

	settings, err := s.store.Integrations().SettingsFor(ctx, integration.ID,
		groupID, projectID, integration.Capabilities.Settings)
	if err != nil {
		s.log.Warn("could not read the places for the autodeploy switches",
			"project", projectPath(project), "error", err)
		return true
	}

	// Every place with this name, not the first one: a deployment that says
	// `place: staging` goes to all of them, so one place's switch is about all of
	// them. A place that is switched off for itself is not deployed to at all, and has
	// nothing to say about the others.
	for _, row := range placesOf(settings) {
		name, _ := stringValue(row["name"])
		if strings.TrimSpace(name) != place {
			continue
		}
		if !flagOf(row, switchField, true) {
			continue
		}
		if !flagOf(row, autoDeployField, true) {
			namespace, _ := stringValue(row["default_namespace"])
			s.log.Info("this place does not deploy by itself, so the deployment is not started",
				"project", projectPath(project), "place", place, "namespace", namespace)
			return false
		}
	}
	return true
}

// placesOf is the clusters setting as a list of rows.
//
// The core does not know what a place is: it knows that this setting is where the places
// are kept, that a row's "name" is what a deployment calls it, and that "enabled" and
// "auto_deploy" are the two words it keeps for itself in such a list.
func placesOf(settings map[string]json.RawMessage) []map[string]json.RawMessage {
	var rows []map[string]json.RawMessage
	if raw, ok := settings[placesKey]; ok {
		_ = json.Unmarshal(raw, &rows)
	}
	return rows
}

// placesKey is where a deployment module keeps its places.
//
// A name rather than a walk of the manifest: the settings a deployment module inherits
// entry by entry are the clusters list, and every deployment module in this codebase
// calls it that. Anything else in the settings is configuration, not a place, and
// mistaking one for the other would put a switch on a timeout.
const placesKey = "clusters"

// flagOf reads one of the core's own row fields as a switch: what it says, or what it
// says when it says nothing.
//
// A field that is not a boolean is ignored rather than guessed at, and it stands at the
// default: a value nobody can read is not somebody saying no.
func flagOf(row map[string]json.RawMessage, key string, fallback bool) bool {
	raw, said := row[key]
	if !said {
		return fallback
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	return value
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
// wherever its configuration says, because the switch exists for the moment somebody
// needs this and still has to be able to deploy something on purpose.
//
// The build and the push are kept either way: they are the repository's own, and what
// this answers is where the result may be put, not whether it may be made.
func (s *Server) withoutAutodeployJobs(ctx context.Context, project *models.Project,
	jobs []store.Job) []store.Job {

	kept := make([]store.Job, 0, len(jobs))
	for _, job := range jobs {
		if job.Deploy == nil {
			kept = append(kept, job)
			continue
		}
		target, place := deployTargetOf(job)
		if s.autodeployAllowed(ctx, project, target, place) {
			kept = append(kept, job)
			continue
		}
		s.log.Info("a deployment was not started by a push: this place does not deploy by itself",
			"project", projectPath(project), "place", place, "job", job.Name)
	}
	return kept
}

// deployTargetOf is where a deployment job goes, out of the job's own copy of what the
// repository said: which module and which place in it.
func deployTargetOf(job store.Job) (target, place string) {
	encoded, err := json.Marshal(job.Deploy)
	if err != nil {
		return "", ""
	}
	var spec struct {
		Module string `json:"module"`
		Target string `json:"target"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return "", ""
	}
	// The place a deployment goes to is the one it names; a configuration that names
	// none is taken to mean the deploy itself, which is what a single-place
	// configuration used to be.
	if place := strings.TrimSpace(spec.Target); place != "" {
		return spec.Module, place
	}
	return spec.Module, spec.Name
}
