package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
)

// The same check the registration handler makes, against a module that really is registered with
// nothing set. It is here because the unit tests above cannot see whether the manifest's
// `required` survives the round trip through the database, and that is the one thing the whole
// check rests on.
func TestAModuleWithNothingSetIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a database")
	}
	st := dbtest.Open(t)
	ctx := context.Background()

	manifest := models.Manifest{
		Version: "0.1.0",
		Settings: []models.SettingSpec{{
			Key: "database_url", Type: "text", Label: "Database", Secret: true, Required: true,
		}},
	}
	module, err := st.Integrations().Register(ctx, "deploy:kubernetes",
		dbtest.Unique("needed"), "http://module:8094", []byte("hash"), manifest)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	srv := &Server{store: st}
	missing := srv.missingRequiredSettings(ctx, module)
	if len(missing) != 1 || missing[0] != "database_url" {
		t.Fatalf("a module that said it needs a database was found to be missing %v — the check "+
			"is empty and every module registers, which is what it exists to prevent", missing)
	}

	// And once it is set, it is not missing.
	if err := st.Integrations().SetSetting(ctx, module.ID, models.ScopeInstance, nil,
		"database_url", json.RawMessage(`"host=db user=x"`)); err != nil {
		t.Fatalf("set the setting: %v", err)
	}
	if missing := srv.missingRequiredSettings(ctx, module); len(missing) != 0 {
		t.Fatalf("a module that has what it needs was told it was missing %v", missing)
	}
}
