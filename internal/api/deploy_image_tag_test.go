package api

import (
	"context"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// A deployment that cannot be pinned to a digest is still deployable, and it is deployed
// by the name the runner pushed it under — which includes the tag.
//
// The name without it is a different name. A manifest reading
// "registry.example.com/team/app" is not that image, it is the same repository under a tag
// that was never pushed: the cluster is asked for :latest, the run pushed v1.2.3, and the
// pod sits in ErrImagePull while the log above it says the image was deployed. That is
// where this was found — on a real deployment whose build had succeeded and whose
// manifests had applied, in a namespace that had never held a pod.
//
// No registry module is installed here, which is the ordinary case for an image at an
// address somebody wrote down rather than one a module of this instance serves: there is
// nothing to ask, so nothing can be pinned, and this is the branch that has to answer.
func TestAnUnpinnableImageIsDeployedUnderTheTagItWasPushedAs(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	project := &models.Project{Path: "test/versions", Name: "Versions", Visibility: "private"}
	if err := st.Projects().Create(ctx, project); err != nil {
		t.Fatalf("create the project: %v", err)
	}
	// Two jobs, because that is what a run that builds and deploys looks like: the image
	// belongs to the job that built it, and the deployment reads it from there rather
	// than from itself.
	run, err := st.Pipelines().CreatePipeline(ctx, project.ID, "v1.2.3", "abc123def456", "tag",
		nil, nil, store.Commit{}, []store.Job{
			{Name: "image", Build: map[string]any{
				"image": "registry.example.com/test/versions",
				"tag":   "v1.2.3",
			}},
			{Name: "deploy"},
		})
	if err != nil {
		t.Fatalf("create the run: %v", err)
	}

	jobs, err := st.Pipelines().JobsOfPipeline(ctx, run.ID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("the run has %d jobs, want 2: %v", len(jobs), err)
	}
	var deployJob store.Job
	for _, one := range jobs {
		if one.Name == "deploy" {
			deployJob = one
		}
	}

	s := &Server{store: st, log: logger.Discard(), cfg: &config.Config{}}

	if run == nil {
		t.Fatal("the run was not created, so there is nothing to deploy from")
	}
	image, err := s.imageForDeploy(ctx, &deployJob, run, func(string, ...any) {})
	if err != nil {
		t.Fatalf("work out what to deploy: %v", err)
	}

	if !strings.HasSuffix(image, ":v1.2.3") {
		t.Fatalf("deployed as %q, which is not the name the runner pushed. A cluster "+
			"reading that manifest asks the registry for :latest, and no such tag was ever "+
			"pushed, so the pod cannot start.", image)
	}
	repository, tag := splitImage(image)
	if tag != "v1.2.3" {
		t.Errorf("the tag came back as %q, so the cluster would ask for the wrong name", tag)
	}
	if repository != "registry.example.com/test/versions" {
		t.Errorf("deployed as %q, want the repository the build pushed to", image)
	}
}
