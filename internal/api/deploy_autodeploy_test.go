package api

import (
	"encoding/json"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// A switch on one cluster says nothing about another one, which is the whole reason it
// is a row's own field rather than a question about the project.
func TestAutodeployIsAskedOfOnePlaceAndNotOfAnother(t *testing.T) {
	f, project, module := setupAutodeploy(t, "gate", `[{"name":"prod"},{"name":"stage"}]`)

	if !f.server.autodeployAllowed(t.Context(), project, "kubernetes", "prod") {
		t.Error("a place nobody said anything about does not deploy by itself")
	}

	if err := f.store.Integrations().SetSetting(t.Context(), module.ID, store.ScopeProject,
		&project.ID, "clusters",
		json.RawMessage(`[{"name":"stage","auto_deploy":false}]`)); err != nil {
		t.Fatalf("switch one off: %v", err)
	}

	if f.server.autodeployAllowed(t.Context(), project, "kubernetes", "stage") {
		t.Error("the place that was switched off still deploys by itself")
	}
	if !f.server.autodeployAllowed(t.Context(), project, "kubernetes", "prod") {
		t.Error("one place was switched off by a change meant for another")
	}
}

// Off means "a push will not do it by itself", not "it is gone": a place that is not in
// use is not deployed to at all, and the second switch has nothing left to say.
func TestAPlaceThatIsNotInUseHasNothingToAutodeployTo(t *testing.T) {
	f, project, module := setupAutodeploy(t, "in-use", `[{"name":"prod"}]`)

	if err := f.store.Integrations().SetSetting(t.Context(), module.ID, store.ScopeProject,
		&project.ID, "clusters",
		json.RawMessage(`[{"name":"prod","enabled":false,"auto_deploy":false}]`)); err != nil {
		t.Fatalf("switch it off: %v", err)
	}

	if !f.server.autodeployAllowed(t.Context(), project, "kubernetes", "prod") {
		t.Error("the question was asked about a place that is not in use, and answered no")
	}
}

// A module that cannot be answered for is not a reason to stop a deployment. Nothing is
// stopped unless somebody stopped it.
func TestAnUnanswerableModuleDoesNotStopADeployment(t *testing.T) {
	f, project, _ := setupAutodeploy(t, "unknown", `[{"name":"prod"}]`)

	if !f.server.autodeployAllowed(t.Context(), project, "no-such-module", "prod") {
		t.Error("a module nobody has installed stopped a deployment")
	}
	if !f.server.autodeployAllowed(t.Context(), project, "kubernetes", "") {
		t.Error("a deployment with no place named was stopped")
	}
}

// What the switch changes: the deployment to that place is left out of a run that
// started by itself, and everything else — the build, the push, the other place — stays.
func TestARunThatStartedByItselfSkipsOnlyThePlaceThatSaidNo(t *testing.T) {
	f, project, module := setupAutodeploy(t, "jobs", `[{"name":"prod"},{"name":"stage"}]`)

	if err := f.store.Integrations().SetSetting(t.Context(), module.ID, store.ScopeProject,
		&project.ID, "clusters",
		json.RawMessage(`[{"name":"stage","auto_deploy":false}]`)); err != nil {
		t.Fatalf("switch one off: %v", err)
	}

	jobs := []store.Job{
		{Name: "build", Stage: "build"},
		{Name: "push", Stage: "push"},
		{Name: "deploy:prod", Stage: "deploy", Deploy: map[string]any{
			"module": "kubernetes", "target": "prod"}},
		{Name: "deploy:stage", Stage: "deploy", Deploy: map[string]any{
			"module": "kubernetes", "target": "stage"}},
	}

	kept := f.server.withoutAutodeployJobs(t.Context(), project, jobs)
	names := make([]string, 0, len(kept))
	for _, job := range kept {
		names = append(names, job.Name)
	}
	want := []string{"build", "push", "deploy:prod"}
	if len(names) != len(want) {
		t.Fatalf("the run kept %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("the run kept %v, want %v", names, want)
		}
	}
}

// A place the module has never heard of is not stopped either: a module is free to
// deploy somewhere the core's own settings do not describe.
func TestAPlaceTheModuleDoesNotKnowIsNotStopped(t *testing.T) {
	f, project, _ := setupAutodeploy(t, "unknown-place", `[{"name":"prod"}]`)

	if !f.server.autodeployAllowed(t.Context(), project, "kubernetes", "somewhere-else") {
		t.Error("a place that is not in the settings was stopped")
	}
}

func setupAutodeploy(t *testing.T, name, clusters string) (*moduleFixture, *models.Project, *models.Integration) {
	t.Helper()

	f := newModuleFixture(t)

	// One module of a kind on an instance: the core asks "the" deploy module by kind,
	// and a leftover from an earlier test would be the one it asks. Removed here rather
	// than worked around, because that is what an instance looks like.
	if _, err := f.store.Pool().Exec(t.Context(),
		`DELETE FROM integrations WHERE kind = 'deploy:kubernetes'`); err != nil {
		t.Fatalf("clear the old modules: %v", err)
	}

	module, err := f.store.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("kubernetes"), "http://module-deploy:8094", []byte("hash"),
		models.Manifest{Settings: []models.SettingSpec{{
			Key:   "clusters",
			Type:  "list",
			Items: &models.SettingItems{Identify: []string{"name"}},
		}}})
	if err != nil {
		t.Fatalf("register the module: %v", err)
	}
	t.Cleanup(func() { deleteIntegration(t, f, module.ID) })

	if err := f.store.Integrations().SetSetting(t.Context(), module.ID, store.ScopeInstance,
		nil, "clusters", json.RawMessage(clusters)); err != nil {
		t.Fatalf("set the clusters: %v", err)
	}

	return f, dbtest.NewProject(t, f.store, name, nil), module
}
