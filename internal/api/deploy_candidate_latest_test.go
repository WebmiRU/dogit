package api

import (
	"context"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
)

func TestNewestCandidateOvertakesEveryPendingStepBeforeItsBuildFinishes(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "latest-candidate", nil)
	s := deployQueueServer(t, st, t.TempDir())

	physicalKey := "k8s:test-api-server:web"
	logicalKey := "logical:" + project.ID.String() + ":kubernetes:prod"
	keys := []string{physicalKey, logicalKey}
	jobs := []store.Job{
		{Name: "build", Stage: "build"},
		{Name: "deploy-first", Stage: "deploy", Deploy: map[string]any{"Target": "prod", "Module": "kubernetes"}},
		{Name: "deploy-second", Stage: "deploy", Deploy: map[string]any{"Target": "prod", "Module": "kubernetes"}},
	}
	first, firstChanges, err := st.Pipelines().CreatePipelineWithCandidates(ctx,
		project.ID, "v1.1", "aaaaaaa", "tag", nil, nil, store.Commit{}, jobs,
		map[int][]string{1: keys, 2: keys})
	if err != nil {
		t.Fatalf("create first candidate: %v", err)
	}
	if len(firstChanges) != 0 {
		t.Fatalf("first candidate unexpectedly replaced something: %+v", firstChanges)
	}
	firstJobs, err := st.Pipelines().JobsOfPipeline(ctx, first.ID)
	if err != nil {
		t.Fatalf("read first pipeline jobs: %v", err)
	}
	if len(firstJobs) != 3 {
		t.Fatalf("first pipeline has %d jobs, want 3", len(firstJobs))
	}

	// This is the rule's exception: once the rollout is Running, a newer commit must
	// not change it. The other pending deploy step from that old pipeline is still eligible
	// to be superseded immediately.
	claimed, err := st.Pipelines().ClaimDeployJob(ctx, firstJobs[1].ID)
	if err != nil || !claimed {
		t.Fatalf("mark the first rollout active: claimed=%v error=%v", claimed, err)
	}

	newJobs := []store.Job{
		{Name: "build", Stage: "build"},
		{Name: "deploy", Stage: "deploy", Deploy: map[string]any{"Target": "prod", "Module": "kubernetes"}},
	}
	newest, changes, err := st.Pipelines().CreatePipelineWithCandidates(ctx,
		project.ID, "v1.2", "bbbbbbb", "tag", nil, nil, store.Commit{}, newJobs,
		map[int][]string{1: keys})
	if err != nil {
		t.Fatalf("create newer candidate: %v", err)
	}

	var oldStepsRecorded int
	for _, change := range changes {
		if change.PreviousPipelineID != first.ID {
			t.Errorf("replacement points to previous pipeline %d, want %d", change.PreviousPipelineID, first.ID)
		}
		oldStepsRecorded += len(change.PreviousJobIDs)
	}
	if oldStepsRecorded < 2 {
		t.Fatalf("replacement forgot the pending steps of the old pipeline: %+v", changes)
	}

	latest, err := st.Pipelines().IsLatestDeployCandidate(ctx, keys, newest.ID)
	if err != nil || !latest {
		t.Fatalf("new pipeline must be the candidate before its build has finished: latest=%v error=%v", latest, err)
	}
	oldLatest, err := st.Pipelines().IsLatestDeployCandidate(ctx, keys, first.ID)
	if err != nil {
		t.Fatalf("read old candidate state: %v", err)
	}
	if oldLatest {
		t.Fatal("the previous pipeline remained eligible while the new candidate was still building")
	}

	s.supersedeReplacedDeployCandidates(ctx, changes)
	firstJobs, err = st.store.Pipelines().JobsOfPipeline(ctx, first.ID)
	if err != nil {
		t.Fatalf("read previous pipeline after replacement: %v", err)
	}
	if firstJobs[1].Status != store.JobRunning {
		t.Errorf("active rollout was interrupted by a newer candidate: %q", firstJobs[1].Status)
	}
	if firstJobs[2].Status != store.JobSuperseded {
		t.Errorf("pending old deploy step has status %q, want %q", firstJobs[2].Status, store.JobSuperseded)
	}

	// A build failure in the newest candidate does not restore the older one as a fallback.
	newJobs, err = st.Pipelines().JobsOfPipeline(ctx, newest.ID)
	if err != nil {
		t.Fatalf("read newest pipeline jobs: %v", err)
	}
	if err := st.Pipelines().FinishJob(ctx, newJobs[0].ID, store.JobFailed, 0, "build failed"); err != nil {
		t.Fatalf("fail the newest candidate's build: %v", err)
	}
	oldLatest, err = st.Pipelines().IsLatestDeployCandidate(ctx, keys, first.ID)
	if err != nil {
		t.Fatalf("read candidate after build failure: %v", err)
	}
	if oldLatest {
		t.Fatal("the previous pipeline became eligible again after the newest build failed")
	}
}
