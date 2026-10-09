package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The database is a setting an administrator fills in, and this module reads it on every start
// rather than being handed it once.
//
// That is the whole of the change from a credential handed over at registration: a password
// given once and kept in a file is a password nobody can rotate, because the core does not
// keep it either — changing it meant changing it in the database and re-issuing it by hand to
// a module that would not ask again.
func TestTheDatabaseIsReadAsASettingAndNotRemembered(t *testing.T) {
	var asked int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		if !strings.HasSuffix(r.URL.Path, "/module/settings") {
			t.Errorf("the module asked for %s, which is not its settings", r.URL.Path)
		}
		if got := r.URL.Query().Get("project"); got != "" {
			t.Errorf("a project scope was sent: %q. This is a setting about the module, and a "+
				"project that could choose where a module keeps its records would let two "+
				"projects point one module at two databases", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"effective": map[string]any{databaseSettingKey: "host=db user=deploy dbname=deploy"},
		})
	}))
	defer server.Close()

	core := &coreClient{baseURL: server.URL, token: "t"}
	if got := core.databaseSetting(context.Background()); got != "host=db user=deploy dbname=deploy" {
		t.Fatalf("the setting was not read: %q", got)
	}
	if asked != 1 {
		t.Fatalf("asked %d times, wanted once", asked)
	}
}

// No setting is an ordinary answer, not a failure. The core refuses to let this module
// register without one, so an empty value here means somebody removed it afterwards — and the
// caller has to be able to say what is lost and carry on deploying, not refuse.
func TestAnAbsentDatabaseSettingIsEmptyRatherThanAnError(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"no settings at all", map[string]any{"effective": map[string]any{}}},
		{"the key is not there", map[string]any{"effective": map[string]any{"other": "x"}}},
		{"it is empty", map[string]any{"effective": map[string]any{databaseSettingKey: ""}}},
		{"it is only whitespace", map[string]any{"effective": map[string]any{databaseSettingKey: "  \n "}}},
		{"it is not a string", map[string]any{"effective": map[string]any{databaseSettingKey: 42}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(c.body)
			}))
			defer server.Close()

			core := &coreClient{baseURL: server.URL, token: "t"}
			if got := core.databaseSetting(context.Background()); got != "" {
				t.Fatalf("wanted nothing, got %q", got)
			}
		})
	}
}

// A core that cannot be reached yields nothing rather than an error, for the same reason: the
// module still deploys, and refusing to start would turn an unreachable core into a module
// that looks completely dead.
func TestASettingThatCannotBeFetchedIsEmptyRatherThanAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	core := &coreClient{baseURL: url, token: "t"}
	if got := core.databaseSetting(context.Background()); got != "" {
		t.Fatalf("an unreachable core produced %q instead of nothing", got)
	}
}
