package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/pipeline"
	"github.com/ewolf/dogit/internal/store"
)

type artifactDeployState struct {
	run      *store.Pipeline
	job      *store.Job
	config   *pipeline.Config
	ref      pipeline.Ref
	outputs  []pipeline.BuildOutputSpec
	images   map[string]string
	artifact string
}

// loadArtifactDeployState makes the preview repeatable: it reads the configuration from
// the artifact's commit, rejects incomplete source pipelines, and pins every image before
// a destination is offered to the user.
func (s *Server) loadArtifactDeployState(ctx context.Context, project *models.Project, rawJobID string) (*artifactDeployState, error) {
	jobID, err := strconv.ParseInt(rawJobID, 10, 64)
	if err != nil || jobID < 1 {
		return nil, errBadRequest("a valid artifact job id is required")
	}

	job, err := s.store.Pipelines().JobByID(ctx, jobID)
	if err != nil || job.ProjectID != project.ID {
		return nil, errNotFound("no such artifact in this project")
	}
	if job.Status != store.JobSuccess || len(job.Build) == 0 {
		return nil, errBadRequest("only a successfully pushed image artifact can be deployed")
	}

	run, err := s.store.Pipelines().PipelineByID(ctx, job.PipelineID)
	if err != nil || run.ProjectID != project.ID || run.SHA == "" {
		return nil, errNotFound("the source pipeline for this artifact could not be found")
	}

	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("read the source pipeline jobs: %w", err)
	}
	for _, one := range jobs {
		// A previous deployment failing does not invalidate an already-tested image.
		// Every other required job must be over before the image is offered for rollout.
		if one.Stage == "deploy" {
			continue
		}
		switch one.Status {
		case store.JobSuccess, store.JobSkipped:
			// Done.
		case store.JobFailed:
			if one.AllowFailure {
				continue
			}
			return nil, errBadRequestf("the source pipeline has a failed %q job; fix or retry it before deploying this artifact", one.Name)
		default:
			return nil, errBadRequestf("the source pipeline is not complete: job %q is %s", one.Name, one.Status)
		}
	}

	repoDir := s.repos.PathFor(project)
	config, err := s.pipelineConfig(ctx, repoDir, run.SHA)
	if err != nil {
		return nil, errBadRequestf("the source commit %s has no usable .dogit-ci.yml: %v", shortSHA(run.SHA), err)
	}

	isTag := strings.TrimSpace(run.Variables["CI_COMMIT_TAG"]) != "" ||
		run.Source == string(models.PipelineSourceTag)
	ref := pipeline.RefFor(run.Ref, run.SHA, isTag)
	outputs := config.NamedBuildOutputs()
	images := map[string]string{}

	if len(outputs) > 0 {
		matched := false
		for _, output := range outputs {
			if output.Name == job.Name && job.Stage == "build" {
				matched = true
				break
			}
		}
		if !matched {
			return nil, errBadRequest("this artifact is not one of the named image outputs in its source commit")
		}
		// A named-output release is one set: deploying api while silently leaving worker
		// on its previous commit is not a complete deployment of this pipeline.
		images, err = s.namedBuildImageReferences(ctx, run, outputs)
		if err != nil {
			return nil, errBadRequestf("the named image set is not ready to deploy: %v", err)
		}
	} else {
		image := strings.TrimSpace(asString(job.Build["image"]))
		pinned := pinnedBuildReference(job.Build, image)
		if pinned == "" {
			return nil, errBadRequest("this artifact has no verified digest from a successful push")
		}
		images["image"] = pinned
	}

	selected := images[job.Name]
	if selected == "" {
		selected = images["image"]
	}
	if selected == "" {
		return nil, errBadRequest("the exact image reference for this artifact could not be resolved")
	}
	return &artifactDeployState{
		run: run, job: job, config: config, ref: ref,
		outputs: outputs, images: images, artifact: selected,
	}, nil
}

func (s *Server) artifactDeployTargetAvailable(ctx context.Context, project *models.Project, spec pipeline.DeploySpec) (bool, string) {
	job := deployJob(spec)
	place, placed, err := s.deployPlaceOf(ctx, project, &job)
	if err != nil {
		return false, err.Error()
	}
	if !placed || place.identity == "" || place.namespace == "" {
		return false, "the target has no resolvable cluster and namespace"
	}
	if !s.placeInUse(ctx, project, spec.Module, spec.Target) {
		return false, "this target is switched off for the project"
	}
	return true, ""
}

func (s *Server) artifactDeployChoices(ctx context.Context, state *artifactDeployState, project *models.Project) []map[string]any {
	specs := pipeline.DeploysFor(state.config.Deploys, state.ref)
	choices := make([]map[string]any, 0, len(specs))
	for index, spec := range specs {
		label := strings.TrimSpace(spec.Name)
		if label == "" {
			label = spec.Target
		}
		available, reason := s.artifactDeployTargetAvailable(ctx, project, spec)
		images := make([]map[string]string, 0, len(state.images))
		if len(state.outputs) > 0 {
			for _, output := range state.outputs {
				images = append(images, map[string]string{
					"name": output.Name, "reference": state.images[output.Name],
				})
			}
		} else {
			images = append(images, map[string]string{"name": "image", "reference": state.images["image"]})
		}
		choices = append(choices, map[string]any{
			"index": index, "name": label, "module": spec.Module, "target": spec.Target,
			"manifests": spec.Manifests, "pre_count": len(spec.Pre), "post_count": len(spec.Post),
			"rollout": spec.Rollout, "timeout": spec.Timeout, "images": images,
			"available": available, "reason": reason,
		})
	}
	return choices
}

func (s *Server) handleArtifactDeployOptions(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionTriggerCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !project.AllowPipelineTrigger {
		s.writeError(w, r, errForbidden("pipeline triggering is turned off for this project"))
		return
	}
	state, err := s.loadArtifactDeployState(r.Context(), project, pathParam(r, "jobID"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	artifact := map[string]any{
		"job_id": state.job.ID, "job_name": state.job.Name,
		"pipeline_iid": state.run.IID, "ref": state.run.Ref,
		"sha": state.run.SHA, "reference": state.artifact,
		"digest": asString(state.job.Build["digest"]),
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"artifact": artifact, "deployments": s.artifactDeployChoices(r.Context(), state, project),
	})
}

func (s *Server) handleDeployArtifact(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionTriggerCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !project.AllowPipelineTrigger {
		s.writeError(w, r, errForbidden("pipeline triggering is turned off for this project"))
		return
	}
	state, err := s.loadArtifactDeployState(r.Context(), project, pathParam(r, "jobID"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var request struct {
		DeploymentIndex int `json:"deployment_index"`
	}
	if err := decodeJSON(r, &request); err != nil {
		s.writeError(w, r, err)
		return
	}
	specs := pipeline.DeploysFor(state.config.Deploys, state.ref)
	if request.DeploymentIndex < 0 || request.DeploymentIndex >= len(specs) {
		s.writeError(w, r, errBadRequest("choose one of the deployment targets offered for this artifact"))
		return
	}
	spec := specs[request.DeploymentIndex]
	available, reason := s.artifactDeployTargetAvailable(r.Context(), project, spec)
	if !available {
		s.writeError(w, r, errBadRequest("this target cannot be deployed to: "+reason))
		return
	}

	// References travel on the job as an immutable snapshot. A later push or retry cannot
	// move a tag underneath this manual deployment after the user has confirmed the preview.
	spec.ArtifactPipelineID = state.run.ID
	spec.ArtifactJobID = state.job.ID
	spec.ArtifactImages = make(map[string]string, len(state.images))
	for name, image := range state.images {
		spec.ArtifactImages[name] = image
	}
	job := deployJob(spec)
	job.StageOrder = deployStageOrder(state.config)
	job.BreakOnError = true
	job.BreakOnErrorSet = true

	created, err := s.startRun(r.Context(), project, state.config, state.ref,
		models.PipelineSourceWeb, []store.Job{job}, userFrom(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// startRun records the operation and publishes the plan. Execution then goes through
	// the ordinary candidate-freshness check, namespace lock and latest-candidate queue.
	s.startDeployIfReady(context.WithoutCancel(r.Context()), created.ID)
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"pipeline": s.pipelineView(r, created, nil),
	})
}

func deployStageOrder(config *pipeline.Config) int {
	for index, stage := range config.StageSpecs {
		if stage.Name == "deploy" {
			return index
		}
	}
	for index, stage := range config.Stages {
		if stage == "deploy" {
			return index
		}
	}
	return 0
}
