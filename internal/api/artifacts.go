package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// handleListBuildArtifacts returns tracked artifact producers, including work still waiting
// in either queue, and the image reference/digest once the runner has confirmed the push.
func (s *Server) handleListBuildArtifacts(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	page := atoiOr(r.URL.Query().Get("page"), 1)
	perPage := atoiOr(r.URL.Query().Get("per_page"), store.PipelinePageSizeDefault)
	artifacts, total, err := s.store.Pipelines().ListBuildArtifacts(r.Context(), project.ID, page, perPage)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if perPage < 1 {
		perPage = store.PipelinePageSizeDefault
	}
	if perPage > store.PipelinePageSizeMax {
		perPage = store.PipelinePageSizeMax
	}
	if page < 1 {
		page = 1
	}
	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"artifacts": artifacts,
		"total": total,
		"page": page,
		"pages": pages,
		"per_page": perPage,
	})
}

// handleReportBuildArtifact accepts the exact image reference and digest after BuildKit's
// push-enabled export completes. Only the runner assigned to a running build job may report it.
func (s *Server) handleReportBuildArtifact(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())
	if !strings.HasPrefix(integration.Kind, "runner:") {
		s.writeError(w, r, errForbidden("only a runner may report a built artifact"))
		return
	}
	jobID, err := strconv.ParseInt(pathParam(r, "jobID"), 10, 64)
	if err != nil {
		s.writeError(w, r, errBadRequest("a job id is required"))
		return
	}
	job, err := s.store.Pipelines().JobByID(r.Context(), jobID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such job"))
		return
	}
	if job.RunnerID == nil || *job.RunnerID != integration.ID {
		s.writeError(w, r, errForbidden("this job belongs to another runner"))
		return
	}
	if job.Status != store.JobRunning || len(job.Build) == 0 {
		s.writeError(w, r, errBadRequest("this is not a running artifact-producing job"))
		return
	}

	var req struct {
		Image  string `json:"image"`
		Digest string `json:"digest"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	req.Image = strings.TrimSpace(req.Image)
	req.Digest = strings.TrimSpace(req.Digest)
	if req.Image == "" || req.Digest == "" || !strings.HasPrefix(req.Digest, "sha256:") {
		s.writeError(w, r, errBadRequest("a pushed image reference and sha256 digest are required"))
		return
	}

	build := make(map[string]any, len(job.Build)+3)
	for key, value := range job.Build {
		build[key] = value
	}
	build["reference"] = req.Image
	build["digest"] = req.Digest
	build["pushed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.Pipelines().SetJobBuild(r.Context(), job.ID, build); err != nil {
		s.writeError(w, r, err)
		return
	}

	// This is a normal, durable state change, unlike individual log chunks. The catalog
	// reloads it to show the exact reference and digest recorded for the successful push.
	s.publishPipeline(r.Context(), job.ProjectID, nil, models.EventJobUpdated, map[string]any{
		"job_id": job.ID,
		"status": job.Status,
		"artifact_updated": true,
	})
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"job_id": job.ID,
		"image": req.Image,
		"digest": req.Digest,
	})
}
