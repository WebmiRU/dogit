package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// A place this project has said nothing about is drawn as a row of empty fields, and an
// empty field says nothing: it looks like a place nobody configured, when in fact the
// instance has configured it and the deployment is running against that. So the values a
// level above decided are sent to the page — the address a place pulls from, its namespace,
// anything else that is a fact rather than a credential.
func TestAProjectsPageIsToldWhatTheInstanceDecidedAboutItsPlaces(t *testing.T) {
	env := newInheritedSettingsEnv(t)
	env.writeAtInstance(t, []map[string]any{{
		"name": "local-k3s", "default_namespace": "dogit-dev",
		"registry": "mirror.gcr.io", "rollout_timeout": 300,
	}})

	answer := env.settings(t, "project")
	inherited, err := decodeClusters(t, answer["inherited"])
	if err != nil {
		t.Fatalf("reading what the instance decided: %v", err)
	}
	if len(inherited) != 1 {
		t.Fatalf("the page was sent %d place rows, want 1", len(inherited))
	}
	if got := rowText(inherited[0], "registry"); got != "mirror.gcr.io" {
		t.Errorf("the registry was sent as %q, want the one the instance decided", got)
	}
	if got := rowText(inherited[0], "rollout_timeout"); got != "300" {
		t.Errorf("the rollout timeout was sent as %q, want 300", got)
	}

	// And this scope's own values are still its own: a page that cannot tell an answer it
	// was given from an answer it sent is a page whose Save button lies.
	own, err := decodeClusters(t, answer["own"])
	if err != nil {
		t.Fatalf("reading what this project decided: %v", err)
	}
	if _, decided := own[0]["registry"]; decided {
		t.Error("a value from the instance arrived as something this project decided")
	}
}

// The one thing that must not cross: a cluster's keys. A kubeconfig is shown to an
// administrator on the module's own page — the module that declares it says so about
// itself, and it is right — and it does not travel to a project's page, where anybody who
// can read the project can read it.
func TestAProjectsPageIsNotSentTheKubeconfig(t *testing.T) {
	env := newInheritedSettingsEnv(t)
	env.writeAtInstance(t, []map[string]any{{
		"name": "local-k3s", "kubeconfig": "apiVersion: v1\nclusters:\n- cluster:\n    server: https://192.168.1.103:6443",
		"registry": "mirror.gcr.io",
	}})

	answer := env.settings(t, "project")
	body := answerJSON(t, answer)
	if strings.Contains(body, "6443") {
		for _, part := range strings.Split(body, `","`) {
			if strings.Contains(part, "6443") {
				t.Fatalf("LEAK in: %s", part[:min(len(part), 200)])
			}
		}
		t.Fatal("LEAK somewhere in the answer")
	}

	inherited, err := decodeClusters(t, answer["inherited"])
	if err != nil {
		t.Fatalf("reading what the instance decided: %v", err)
	}
	if _, sent := inherited[0]["kubeconfig"]; sent {
		t.Error("the kubeconfig was sent to a project's page")
	}
	// And it is not there because the module did not publish it, not because the core
	// recognised a credential: a module that marks nothing as inheritable sends nothing
	// below, which is the only default that survives a module author forgetting a mark.
	if _, published := inherited[0]["chat_id"]; published {
		t.Error("a field the module did not publish downward was sent anyway")
	}
	// The name is what the page matches the row by, so it has to survive.
	if got := rowText(inherited[0], "name"); got != "local-k3s" {
		t.Errorf("the row was sent without its name: %v", inherited[0])
	}
}

// A field the module marked secret is not sent either, and never was. Two different reasons
// for the same absence, and a test for each: a secret is write-only everywhere, and a
// local-only value is readable here and nowhere below.
func TestASecretIsNotSentAtAllAndALocalOneOnlyNotBelow(t *testing.T) {
	env := newInheritedSettingsEnv(t)
	env.writeAtInstance(t, []map[string]any{{
		"name": "local-k3s", "chat_id": "-100123", "bot_token": "1234:AAAA-secret",
	}})

	answer := env.settings(t, "project")
	body := answerJSON(t, answer)
	if strings.Contains(body, "AAAA-secret") {
		t.Error("a secret was sent to a page")
	}

	inherited, err := decodeClusters(t, answer["inherited"])
	if err != nil {
		t.Fatalf("reading what the instance decided: %v", err)
	}
	if got := rowText(inherited[0], "registry"); got != "" {
		t.Errorf("nothing was published downward, so nothing should have arrived: %q", got)
	}
}

// Nothing is above the instance, so an instance page is sent nothing inherited — and a page
// that said "inherited" at the top of the chain would be pointing at nothing.
func TestAnInstancePageIsSentNothingInherited(t *testing.T) {
	env := newInheritedSettingsEnv(t)
	env.writeAtInstance(t, []map[string]any{{"name": "local-k3s", "registry": "mirror.gcr.io"}})

	answer := env.settings(t, "instance")
	inherited := answer["inherited"]
	if raw, ok := inherited.(map[string]any); ok && len(raw) > 0 {
		t.Errorf("the instance was sent %d values as inherited", len(raw))
	}
}

func newInheritedSettingsEnv(t *testing.T) *inheritedSettingsEnv {
	t.Helper()

	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "inherited-settings", nil)

	manifest := models.Manifest{
		Settings: []models.SettingSpec{{
			Key: "clusters", Label: "Clusters", Type: "list",
			Items: &models.SettingItems{
				Identify: []string{"name"},
				Fields: []models.SettingSpec{
					{Key: "name", Label: "Name", Type: "string"},
					{Key: "registry", Label: "Registry", Type: "registry", Inheritable: true},
					{Key: "rollout_timeout", Label: "Rollout timeout", Type: "int", Inheritable: true},
					{Key: "kubeconfig", Label: "Kubeconfig", Type: "text"},
					{Key: "chat_id", Label: "Chat", Type: "string"},
					{Key: "bot_token", Label: "Bot token", Type: "string", Secret: true},
				},
			},
		}},
	}
	module, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("deploy"), "http://module-deploy:8094", []byte("hash"), manifest)
	if err != nil {
		t.Fatalf("register the deploy module: %v", err)
	}

	user := dbtest.NewUser(t, st, "inherited", true)
	session := dbtest.NewSession(t, st, user.ID)
	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		repos: repos.New(st, gitx.New(gitx.Options{}), t.TempDir()),
	}

	return &inheritedSettingsEnv{
		store: st, project: project, module: module,
		router: srv.Routes(), session: session,
	}
}

type inheritedSettingsEnv struct {
	store   *store.Store
	project *models.Project
	module  *models.Integration
	router  http.Handler
	session string
}

// writeAtInstance is what an administrator's page does: the whole list, at the top scope.
func (e *inheritedSettingsEnv) writeAtInstance(t *testing.T, rows []map[string]any) {
	t.Helper()

	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("encoding the place rows: %v", err)
	}
	if _, err := e.store.Pool().Exec(t.Context(),
		`INSERT INTO integration_settings (integration_id, scope_type, key, value)
		 VALUES ($1, 'instance', 'clusters', $2)`, e.module.ID, encoded); err != nil {
		t.Fatalf("write the place rows: %v", err)
	}
}

// settings asks the question a page asks.
func (e *inheritedSettingsEnv) settings(t *testing.T, scope string) map[string]any {
	t.Helper()

	query := "scope=" + scope
	if scope == "project" {
		query += "&projectID=" + e.project.ID.String()
	}

	request := httptest.NewRequest(http.MethodGet,
		"/modules/"+e.module.ID.String()+"/settings?"+query, nil)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: e.session})
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("asking for the settings: %d %s", recorder.Code, recorder.Body.String())
	}
	var answer map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not settings: %v", err)
	}
	return answer
}

// decodeClusters is one setting out of an answer's map: the rows of the list the deploy
// module calls clusters.
func decodeClusters(t *testing.T, raw any) ([]map[string]json.RawMessage, error) {
	t.Helper()

	if section, ok := raw.(map[string]any); ok {
		raw = section["clusters"]
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var rows []map[string]json.RawMessage
	if len(encoded) == 0 || string(encoded) == "null" {
		return nil, nil
	}
	return rows, json.Unmarshal(encoded, &rows)
}

// rowText is one field of a row as text: a number is written as a string in one place and
// as a number in another, and a test that cared which would be testing the JSON encoder
// rather than what was sent.
func rowText(row map[string]json.RawMessage, key string) string {
	raw, ok := row[key]
	if !ok {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.Itoa(int(number))
	}
	return ""
}

// answerJSON is the whole answer as text, for the tests that ask what is in it rather than
// what is in one field of it.
func answerJSON(t *testing.T, answer map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("encoding the answer: %v", err)
	}
	return string(encoded)
}
