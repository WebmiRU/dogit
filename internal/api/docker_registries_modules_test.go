package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
)

// A registry this instance runs as a module belongs in the list of registries without
// anybody copying it there. An administrator asking "where do images go" is answered by
// this page, and a page that lists only the addresses somebody remembered to write down
// answers a different question.
func TestTheModulesOwnRegistryIsInTheListWithoutBeingCopied(t *testing.T) {
	env := newRegistryEnv(t, true)
	module := installRegistryModule(t, env, "our own registry")

	rows := registryRowsFrom(t, env)
	if len(rows) != 1 {
		t.Fatalf("the list holds %d rows, want the one the module brought", len(rows))
	}
	row := rows[0]
	if row.Source != "module" {
		t.Errorf("the row is marked %q, want module", row.Source)
	}
	if row.Name != "our own registry" {
		t.Errorf("the row is called %q, want the module's own name", row.Name)
	}
	// Bare, though the module publishes it with a scheme: every other row on this page is
	// written that way, and a list showing one address each way round is a list somebody
	// reads as two registries where there is one. The module's own published address is
	// still what it is — this page shows the host, and the place that picks it gets the
	// host, which is what an image name and a pull secret are both written with.
	if row.URL != modulePublishedAddress {
		t.Errorf("the row says %q, want the address the module publishes, bare (%q)",
			row.URL, modulePublishedAddress)
	}
	if row.IntegrationID != module.ID.String() {
		t.Errorf("the row names the module %s, want %s", row.IntegrationID, module.ID)
	}
	if row.ID != "" {
		t.Errorf("the row carries the id %s, which would mean it could be edited here", row.ID)
	}

	// And nothing was copied. This is the whole reason the module's registry is read rather
	// than written into the table: a copy would go on listing a registry the day after the
	// module is uninstalled.
	if total := env.total(t); total != 0 {
		t.Errorf("the table of written-down addresses holds %d records, want none", total)
	}
}

// The two kinds of row are told apart in the answer, because the page has to decide what a
// reader may do with each one and cannot decide that from the absence of a field.
func TestEveryRowSaysWhereItCameFrom(t *testing.T) {
	env := newRegistryEnv(t, true)
	installRegistryModule(t, env, "our own registry")
	env.mustCreate(t, map[string]any{"url": env.host(), "name": "written down"})

	rows := registryRowsFrom(t, env)
	if len(rows) != 2 {
		t.Fatalf("the list holds %d rows, want two", len(rows))
	}
	if rows[0].Source != "module" || rows[1].Source != "written" {
		t.Errorf("the list reads %q then %q, want the module's own first", rows[0].Source, rows[1].Source)
	}
	if rows[1].ID == "" {
		t.Error("the written-down row carries no id, so it cannot be edited")
	}
}

// A module's registry is not editable here, and its id is what says so. A form that opened
// on one could change nothing and would look broken.
func TestAModulesRegistryCannotBeEditedAsIfItWereWrittenDown(t *testing.T) {
	env := newRegistryEnv(t, true)
	module := installRegistryModule(t, env, "our own registry")

	code, body := env.do(t, http.MethodGet, "/registry/docker/"+module.ID.String(), nil)
	if code == http.StatusOK {
		t.Fatalf("a module's registry is served as a written-down one: %s", body)
	}
}

// A module's registry is listed whether it is in use or not: somebody reading this page is
// asking where images go, and "your registry is switched off" is an answer. A row that was
// quietly absent would be a question, and the wrong one.
func TestAForbiddenModuleIsStillListedAndSaysSo(t *testing.T) {
	env := newRegistryEnv(t, true)
	module := installRegistryModule(t, env, "our own registry")

	if code, body := env.do(t, http.MethodPut, "/modules/"+module.ID.String()+"/state",
		map[string]any{"enabled": false}); code != http.StatusOK {
		t.Fatalf("forbidding the module: %d %s", code, body)
	}

	rows := registryRowsFrom(t, env)
	if len(rows) != 1 {
		t.Fatalf("the list holds %d rows after the module was forbidden, want 1", len(rows))
	}
	if rows[0].Enabled {
		t.Error("the row says the registry is in use, and the module was forbidden")
	}
}

// Uninstalling takes the row away by itself. This is what reading rather than copying buys:
// nothing has to be undone when the module goes, so nothing can be left behind claiming a
// registry that no longer exists.
func TestRemovingTheModuleTakesTheRowAway(t *testing.T) {
	env := newRegistryEnv(t, true)
	module := installRegistryModule(t, env, "our own registry")

	if got := len(registryRowsFrom(t, env)); got != 1 {
		t.Fatalf("the list holds %d rows, want 1", got)
	}
	if err := env.store.Integrations().Delete(t.Context(), module.ID); err != nil {
		t.Fatalf("removing the module: %v", err)
	}
	if got := len(registryRowsFrom(t, env)); got != 0 {
		t.Errorf("the list still holds %d rows after the module was removed", got)
	}
}

// A page holds ten rows whether they are the module's or the administrator's, and no row is
// skipped between pages or shown twice. This is the arithmetic of one list with two kinds of
// row in it: the first page carries the module's row and fills up with written-down ones,
// and every page after it is only written-down ones, offset by however many the first page
// was carrying.
func TestPagesOfAMixedListShowEveryRowExactlyOnce(t *testing.T) {
	env := newRegistryEnv(t, true)
	installRegistryModule(t, env, "our own registry")

	const written = 23
	for i := 0; i < written; i++ {
		env.mustCreate(t, map[string]any{"url": registryAddr(env.host(), i)})
	}

	seen := map[string]int{}
	total, firstPage := 0, 0
	for p := 1; p <= 20; p++ {
		answer := env.page(t, p)
		total = answer.Total
		if p == 1 {
			firstPage = len(answer.Registries)
			if answer.Registries[0].Source != "module" {
				t.Errorf("the first row is %q, want the module's own", answer.Registries[0].Source)
			}
		}
		for _, row := range answer.Registries {
			key := row.Source + ":" + row.URL
			if row.Source == "module" {
				key = "module:" + row.Name
			}
			seen[key]++
			if seen[key] > 1 {
				t.Fatalf("row %q is on more than one page", key)
			}
		}
		if p == answer.Pages {
			break
		}
	}

	if len(seen) != written+1 {
		t.Errorf("the pages showed %d rows between them, want %d", len(seen), written+1)
	}
	if total != written+1 {
		t.Errorf("the list counted %d rows, want %d", total, written+1)
	}
	if firstPage != 10 {
		t.Errorf("the first page holds %d rows, want 10", firstPage)
	}
}

// The module's registry is here, and so is everything that was written down: adding a
// module to the instance must not push the administrator's own addresses off the end of the
// list, which is what happens if the module's rows are counted against the page and then
// forgotten when the written-down ones are read.
func TestAModuleDoesNotPushTheWrittenOnesOffTheEnd(t *testing.T) {
	env := newRegistryEnv(t, true)
	installRegistryModule(t, env, "our own registry")

	const written = 10
	for i := 0; i < written; i++ {
		env.mustCreate(t, map[string]any{"url": registryAddr(env.host(), i)})
	}

	// Ten module-and-written rows fit on one page of ten: nine written-down ones and the
	// module's own. The tenth written-down one is on page two, and page two holds one.
	first := env.page(t, 1)
	if len(first.Registries) != 10 {
		t.Fatalf("the first page holds %d rows, want 10", len(first.Registries))
	}
	if first.Total != written+1 {
		t.Errorf("the list counted %d rows, want %d", first.Total, written+1)
	}
	if first.Pages != 2 {
		t.Errorf("says there are %d pages, want 2 for %d rows at ten to a page", first.Pages, written+1)
	}

	second := env.page(t, 2)
	if len(second.Registries) != 1 {
		t.Fatalf("the second page holds %d rows, want the one that did not fit", len(second.Registries))
	}
	if second.Registries[0].Source != "written" {
		t.Errorf("the second page holds a %q row", second.Registries[0].Source)
	}
}

// modulePublishedAddress is what a registry module says of itself when nothing overrides it:
// the same shape of address the instance's own registry is reached at.
const modulePublishedAddress = "192.168.1.103:8091"

// installRegistryModule registers a Docker registry module the way its own binary would, and
// removes it when the test ends so that the next one does not find somebody else's.
func installRegistryModule(t *testing.T, env *registryEnv, name string) *models.Integration {
	t.Helper()

	module, err := env.store.Integrations().Register(t.Context(), registryKind,
		dbtest.Unique("registry"), "http://module-registry:8091",
		models.Manifest{
			Routing: models.RoutingSpec{Domains: []string{modulePublishedAddress}},
			Settings: []models.SettingSpec{{
				Key:   "public_address",
				Type:  "string",
				Label: "Public address",
			}},
		})
	if err != nil {
		t.Fatalf("register the registry module: %v", err)
	}
	t.Cleanup(func() { _ = env.store.Integrations().Delete(t.Context(), module.ID) })

	// The name is the module's own, so a list that shows it is showing what the module
	// calls itself rather than what this test called it.
	if _, err := env.store.Pool().Exec(t.Context(),
		`UPDATE integrations SET name = $2 WHERE id = $1`, module.ID, name); err != nil {
		t.Fatalf("name the module: %v", err)
	}
	return module
}

// registryRow is one row of the list as this package reads it.
type registryRow struct {
	Source        string `json:"source"`
	ID            string `json:"id"`
	IntegrationID string `json:"integration_id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Enabled       bool   `json:"enabled"`
	Published     bool   `json:"published"`
	Status        string `json:"status"`
}

type registryAnswer struct {
	Registries []registryRow `json:"registries"`
	Total      int           `json:"total"`
	Page       int           `json:"page"`
	Pages      int           `json:"pages"`
	PerPage    int           `json:"per_page"`
	PastTheEnd bool          `json:"past_the_end"`
}

func (e *registryEnv) page(t *testing.T, page int) registryAnswer {
	t.Helper()

	path := "/registry/docker"
	if page > 1 {
		path += "?page=" + strconv.Itoa(page)
	}
	_, body := e.do(t, http.MethodGet, path, nil)

	var answer registryAnswer
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("the answer is not the list: %v (%s)", err, body)
	}
	return answer
}

func registryRowsFrom(t *testing.T, env *registryEnv) []registryRow {
	t.Helper()
	return env.page(t, 1).Registries
}
