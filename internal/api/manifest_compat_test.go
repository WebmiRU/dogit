package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
)

// A manifest written by a module built against an older core still registers.
//
// Manifests are read with unknown fields refused, which is right — it catches a module that
// misspells a setting key rather than silently having it ignored. But it makes every removed
// manifest field a way to lock a module out of an instance it worked on yesterday, and the
// error it produces names a JSON field rather than the module.
//
// `database` was such a field: it asked the core to provision a database, the core stopped
// doing that, and every module still declaring it was refused registration with a message
// about a field the module author had no way of knowing had a name.
func TestAManifestFromAnOlderCoreStillParses(t *testing.T) {
	raw := `{"version":"0.1.0","scopes":[],"database":false,
		"settings":[{"key":"tags","type":"string","label":"Tags"}]}`

	var caps models.Manifest
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		t.Fatalf("a manifest every old module sends did not parse: %v", err)
	}
	if len(caps.Settings) != 1 || caps.Settings[0].Key != "tags" {
		t.Fatalf("the settings did not come through: %+v", caps.Settings)
	}
}

// The same thing through the decoder the handler actually uses, since `json.Unmarshal` is more
// forgiving than the strict one and the difference is the whole point of this test.
func TestTheStrictDecoderAcceptsADatabaseFieldItIgnores(t *testing.T) {
	var caps models.Manifest
	request := httptest.NewRequest(http.MethodPost, "/",
		bytes.NewReader([]byte(`{"version":"0.1.0","database":true}`)))

	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&caps); err != nil {
		t.Fatalf("a module declaring the old database field was refused: %v", err)
	}
}

// The field parses and nothing acts on it. A module that asks for a database the way it did
// two versions ago registers, is told nothing about databases, and gets what it declared for
// itself — which is the whole of what the compatibility is for.
func TestAStaleDatabaseFieldChangesNothingAboutRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a database")
	}
	st := dbtest.Open(t)
	ctx := context.Background()

	manifest := models.Manifest{Version: "0.1.0"}
	module, err := st.Integrations().Register(ctx, "deploy:kubernetes",
		dbtest.Unique("stale"), "http://module:8094", manifest)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	srv := &Server{store: st}
	if missing := srv.missingRequiredSettings(ctx, module); len(missing) != 0 {
		t.Fatalf("a module that says nothing about databases was refused for %v, which is the "+
			"old field still being read somewhere", missing)
	}
}

// A field nobody has ever heard of is still refused. The leniency is for fields that existed,
// not for typos — a misspelled setting key has to be caught.
func TestTheStrictDecoderStillRefusesAFieldNobodyHasHeardOf(t *testing.T) {
	var caps models.Manifest
	request := httptest.NewRequest(http.MethodPost, "/",
		bytes.NewReader([]byte(`{"version":"0.1.0","databse":true}`)))

	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&caps); err == nil {
		t.Fatal("a misspelled manifest field was accepted, and a module whose setting key is " +
			"wrong will find out at runtime instead of at registration")
	}
}
