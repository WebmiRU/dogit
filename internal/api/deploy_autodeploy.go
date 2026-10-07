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

// placeInUse is whether this project has switched a place on, and whether there was a row
// to ask about at all.
//
// The same question `autodeployAllowed` asks and the opposite answer, kept apart because
// they are asked at different moments and mean different things: that one is "may a push
// deploy here by itself", this one is "may this project deploy here at all".
//
// A place nobody has written down, and a project with no row for it, are both in use —
// nothing is stopped unless somebody stopped it. So this answers "yes" whenever it cannot
// read an answer, and the only "no" it ever gives is a row that says `enabled: false`.
func (s *Server) placeInUse(ctx context.Context, project *models.Project,
	target, place string) bool {

	place = strings.TrimSpace(place)
	if place == "" || project == nil {
		return true
	}

	integration, err := s.deployModule(ctx, strings.TrimSpace(target))
	if err != nil || integration == nil {
		return true
	}
	id := project.ID
	settings, err := s.store.Integrations().SettingsFor(ctx, integration.ID,
		project.GroupID, &id, integration.Capabilities.Settings)
	if err != nil {
		s.log.Warn("could not read the places for the in-use switch",
			"project", projectPath(project), "place", place, "error", err)
		return true
	}

	return placeSwitchedOn(placesOf(settings), place)
}

// placeSwitchedOn is whether a project's own row for a place says it is in use, out of rows
// already read.
//
// Split from placeInUse so that the row is read once per question rather than once per
// question per place: naming every place of a run asks about each of them, and each read
// walks the whole places list of a module to answer one line of it.
func placeSwitchedOn(rows []map[string]json.RawMessage, place string) bool {
	for _, row := range rows {
		name, _ := stringValue(row["name"])
		if strings.TrimSpace(name) != place {
			continue
		}
		return flagOf(row, switchField, true)
	}
	return true
}

// aPlaceNamed is one place as an event about it has to be written: the name a deployment
// calls it, and the namespace it deploys into, which is all a card matches on.
type aPlaceNamed struct {
	place     string
	namespace string
}

// placesOfRun is every place this run names a deployment for, ready to be named in an
// event about the run's work.
//
// Out of the run's own jobs and not out of the configuration file, for the reason the
// deployment reads it from the job: the rules were applied when the run was created, and a
// file edited since then describes a different set of places than the one this run was
// asked to do.
//
// A place this project has switched off is left out, and a place named by two jobs is named
// once. The first because the work in hand is not for a place this project may not deploy
// to, and a card opened to watch it would never see a rollout — it would have to be read a
// second time to understand. The second because one line arriving twice reads as a log that
// says everything twice.
func (s *Server) placesOfRun(ctx context.Context, project *models.Project,
	pipelineID int64) []aPlaceNamed {

	if project == nil || pipelineID == 0 {
		return nil
	}
	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, pipelineID)
	if err != nil {
		return nil
	}

	named := []aPlaceNamed{}
	seen := map[string]bool{}
	for _, job := range jobs {
		if job.Deploy == nil {
			continue
		}
		// The keys as the job carries them, which is how the page reads them too: the
		// column is a Go struct written out as it stands, so "Target" here and "target"
		// there are the same field to Go and two different fields to a map.
		place := strings.TrimSpace(asString(job.Deploy["Target"]))
		if place == "" || seen[place] {
			continue
		}

		module, err := s.deployModule(ctx, strings.TrimSpace(asString(job.Deploy["Module"])))
		if err != nil || module == nil {
			continue
		}
		// Written down before the row is read, so that a place two jobs name is read once
		// and not once per job — including a place that turns out to be switched off.
		seen[place] = true

		rows := s.placeRows(ctx, project, module)
		if !placeSwitchedOn(rows, place) {
			continue
		}
		named = append(named, aPlaceNamed{
			place:     place,
			namespace: placeNamespaceIn(rows, place),
		})
	}
	return named
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
