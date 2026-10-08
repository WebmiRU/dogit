package store_test

import (
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/resource"
)

// The whole life of a described database, against a real database.
//
// Every query here has its column list and its arguments changed at least once, and they are
// checked in the one place where a mistake is honest: the place that would run them. A query
// whose arguments were lost while its placeholders stayed fails as "expected 1 arguments, got
// 0" from the driver, which is at least a true message — but it is a 500 on an administrator's
// request, and nothing else in the build notices it.
//
// The secret is checked in both directions, because the two failures look identical from the
// page: a secret that was never sealed reads back as a password in the table, and one that was
// sealed wrongly does not come back at all.
func TestADescribedDatabaseLivesAndDiesWithoutLosingItsSecret(t *testing.T) {
	st := dbtest.Open(t)
	resources := st.Resources()

	module, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("res"), "http://module-deploy:8094", []byte("hash"), models.Manifest{})
	if err != nil {
		t.Fatalf("register a module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	host := dbtest.Unique("db.example.com")
	written, err := resources.Put(t.Context(), models.Resource{
		Kind:     "db",
		Software: "postgresql",
		Name:     "the deploy database",
		Origin:   models.OriginManual,
		Parts: resource.Parts{
			"host":          host,
			"port":          "5432",
			"database_name": "our_deploy_db",
			"username":      "dp",
			"password":      "a-password-with-@-in-it",
		},
		LastIntegrationKind: "deploy:kubernetes",
	})
	if err != nil {
		t.Fatalf("write the resource down: %v", err)
	}

	// The list: read without the secret, so a page of resources can be pasted into a ticket.
	listed, err := resources.List(t.Context())
	if err != nil {
		t.Fatalf("list the resources: %v", err)
	}
	var found *models.Resource
	for i := range listed {
		if listed[i].ID == written.ID {
			found = &listed[i]
		}
	}
	if found == nil {
		t.Fatal("the resource that was just written down is not in the list")
	}
	if found.Parts["host"] != host {
		t.Fatalf("the host did not come back from the list: %q", found.Parts["host"])
	}
	if found.Parts["username"] != "dp" || found.Parts["database_name"] != "our_deploy_db" {
		t.Fatalf("the facts did not come back from the list: %v", found.Parts)
	}
	if found.Secret["password"] != "" {
		t.Fatalf("the list handed over a password: %v", found.Secret)
	}

	// One resource, so reading it back must not fail on a missing row that is not missing.
	one, err := resources.ByID(t.Context(), written.ID)
	if err != nil {
		t.Fatalf("read the resource back: %v", err)
	}
	if one.Secret["password"] != "a-password-with-@-in-it" {
		t.Fatalf("the password did not come back out of the envelope: %q", one.Secret["password"])
	}

	// The module is given it, and sees the same secret.
	if err := resources.Grant(t.Context(), written.ID, module.ID, "database"); err != nil {
		t.Fatalf("give the resource to the module: %v", err)
	}
	held, err := resources.HeldBy(t.Context(), module.ID)
	if err != nil {
		t.Fatalf("read what the module holds: %v", err)
	}
	if held.ID != written.ID {
		t.Fatalf("the module is holding %s, not the one it was given", held.ID)
	}
	if held.Secret["password"] != "a-password-with-@-in-it" {
		t.Fatalf("the module would be given a password of %q", held.Secret["password"])
	}

	// The settings page reads the same row with the secret left shut.
	shown, err := resources.ByIDFor(t.Context(), module.ID)
	if err != nil {
		t.Fatalf("read the resource for the settings page: %v", err)
	}
	if shown.Secret["password"] != "" {
		t.Fatalf("the settings page was handed a password: %v", shown.Secret)
	}
	if shown.Parts["host"] != host {
		t.Fatalf("the settings page was not handed the host: %v", shown.Parts)
	}

	// Withdrawn by hand, by an administrator rather than by removing the module.
	released, err := resources.ReleaseByID(t.Context(), written.ID)
	if err != nil {
		t.Fatalf("take the resource back: %v", err)
	}
	if released.Held() {
		t.Fatal("the resource is still held after being taken back")
	}
	if released.Parts["host"] != host {
		t.Fatalf("the facts did not survive being given up: %v", released.Parts)
	}

	if _, err := resources.Forget(t.Context(), written.ID); err != nil {
		t.Fatalf("forget the resource: %v", err)
	}
	if _, err := resources.ByID(t.Context(), written.ID); err == nil {
		t.Fatal("a forgotten resource is still readable")
	}
}

// The same path through removing a module, which releases by module rather than by id.
//
// This is the query that lost its arguments: it names a placeholder three times and has to be
// given the module's id for each, and the page it backs is the one an administrator is on when
// they remove a module and go to look at what it left behind.
func TestRemovingAModuleLeavesItsDatabaseBehind(t *testing.T) {
	st := dbtest.Open(t)
	resources := st.Resources()

	module, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("gone"), "http://module-deploy:8094", []byte("hash"), models.Manifest{})
	if err != nil {
		t.Fatalf("register a module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	host := dbtest.Unique("left-behind.example.com")
	written, err := resources.Put(t.Context(), models.Resource{
		Kind: "db", Software: "postgresql", Name: "left behind",
		Origin: models.OriginManual,
		Parts:  resource.Parts{"host": host, "port": "5432", "database_name": "gone_db", "username": "gone"},
	})
	if err != nil {
		t.Fatalf("write the resource down: %v", err)
	}
	if err := resources.Grant(t.Context(), written.ID, module.ID, "database"); err != nil {
		t.Fatalf("give the resource to the module: %v", err)
	}

	released, err := resources.Release(t.Context(), module.ID, module.Kind)
	if err != nil {
		t.Fatalf("release on the removal of a module: %v", err)
	}
	if released.Held() {
		t.Fatal("the resource is still held after the module was given up")
	}
	// Whose it was, which is the whole reason this row survives the module: somebody looking
	// at a resource nobody holds asks that, and the module's row is gone.
	if released.LastIntegrationKind != "deploy:kubernetes" {
		t.Fatalf("the orphan does not say whose it was, says %q", released.LastIntegrationKind)
	}
	if released.LastIntegrationID == nil || *released.LastIntegrationID != module.ID {
		t.Fatalf("the orphan does not name the module it belonged to: %v", released.LastIntegrationID)
	}
	// And the fact of where it is, so the page can show it without decrypting anything.
	if released.Parts["host"] != host {
		t.Fatalf("the orphan lost where it is: %v", released.Parts)
	}

	// Given away again to the next module of the same kind: the reinstall case.
	next, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("back"), "http://module-deploy:8094", []byte("hash"), models.Manifest{})
	if err != nil {
		t.Fatalf("register the module that comes back: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, next.ID)
	})

	free, err := resources.Free(t.Context())
	if err != nil {
		t.Fatalf("list what is free: %v", err)
	}
	var candidate *models.Resource
	for i := range free {
		if free[i].LastIntegrationKind == next.Kind {
			candidate = &free[i]
		}
	}
	if candidate == nil {
		t.Fatalf("no free resource names %q as its last kind of module", next.Kind)
	}
	if err := resources.Grant(t.Context(), candidate.ID, next.ID, "database"); err != nil {
		t.Fatalf("give the free resource to the module that came back: %v", err)
	}
	if _, err := resources.HeldBy(t.Context(), next.ID); err != nil {
		t.Fatalf("the module that came back does not hold the database it had before: %v", err)
	}
}

// A resource is only recorded once, and the check is on where it is rather than on what it is
// called — two names for one database is the mistake, and a stale password is not a second one.
func TestTheSamePlaceIsNotRecordedTwice(t *testing.T) {
	st := dbtest.Open(t)
	resources := st.Resources()
	database, _ := resource.ByKey("db")

	host := dbtest.Unique("twice.example.com")
	parts := resource.Parts{
		"host": host, "port": "5432", "database_name": "twice", "username": "twice",
	}
	if _, err := resources.Put(t.Context(), models.Resource{
		Kind: "db", Name: "first", Origin: models.OriginManual, Parts: parts,
	}); err != nil {
		t.Fatalf("write the first one down: %v", err)
	}

	taken, err := resources.Taken(t.Context(), database, parts)
	if err != nil {
		t.Fatalf("look for it: %v", err)
	}
	if !taken {
		t.Fatal("a resource that is on the shelf was not found by where it is")
	}

	// The same place, the same user, the same database — a different name and a password that
	// has since been rotated. Still the same resource.
	stale := resource.Parts{}
	for key, value := range parts {
		stale[key] = value
	}
	stale["password"] = "a-rotated-password"
	taken, err = resources.Taken(t.Context(), database, stale)
	if err != nil {
		t.Fatalf("look for it with a rotated password: %v", err)
	}
	if !taken {
		t.Fatal("a rotated password made the same database look like a new one")
	}

	// A different database on the same host is a different resource.
	elsewhere := resource.Parts{}
	for key, value := range parts {
		elsewhere[key] = value
	}
	elsewhere["database_name"] = "another"
	taken, err = resources.Taken(t.Context(), database, elsewhere)
	if err != nil {
		t.Fatalf("look for another database on the same host: %v", err)
	}
	if taken {
		t.Fatal("another database on the same host is the same resource as this one")
	}
}

// An object store has none of a database's parts and must still be writable, sealed, and told
// apart from a database that happens to be on the same machine.
func TestAnObjectStoreIsNotADatabaseWithAnEmptyHost(t *testing.T) {
	st := dbtest.Open(t)
	resources := st.Resources()
	store, known := resource.ByKey("s3")
	if !known {
		t.Fatal("there is no s3 kind")
	}

	endpoint := "https://" + dbtest.Unique("minio.example.com") + ":9000"
	written, err := resources.Put(t.Context(), models.Resource{
		Kind: "s3", Name: "images", Origin: models.OriginManual,
		Parts: resource.Parts{
			"endpoint": endpoint, "region": "eu-central-1", "bucket": "images",
			"access_key": "AKIAEXAMPLE", "secret_key": "the-secret-half",
		},
	})
	if err != nil {
		t.Fatalf("write the object store down: %v", err)
	}

	back, err := resources.ByID(t.Context(), written.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if back.Parts["endpoint"] != endpoint || back.Parts["bucket"] != "images" {
		t.Fatalf("where it is did not come back: %v", back.Parts)
	}
	if back.Parts["access_key"] != "AKIAEXAMPLE" {
		t.Fatalf("the access key is the readable half and must come back: %v", back.Parts)
	}
	if back.Secret["secret_key"] != "the-secret-half" {
		t.Fatalf("the secret key did not come back out of the envelope: %v", back.Secret)
	}
	// And a database is not found where an object store is, however they are written down.
	taken, err := resources.Taken(t.Context(), store, back.Parts)
	if err != nil {
		t.Fatalf("look for it by where it is: %v", err)
	}
	if !taken {
		t.Fatal("an object store on a machine is not found by its endpoint")
	}
	if back.Where() == "" {
		t.Fatal("a page has no way to say where this resource is")
	}
}

// Without a key there is no record at all, rather than one with the password written plainly.
//
// A resource with no secret at all needs no key, and is written: an object store nobody has
// finished describing yet, or a database whose host answers and whose user trusts the network.
// Refusing those would mean a key is required before anything may be recorded, which is the
// wrong shape — the key protects a password, and there is no password here to protect.
func TestAResourceWithNoSecretNeedsNoKey(t *testing.T) {
	st := dbtest.Open(t)

	written, err := st.Resources().Put(t.Context(), models.Resource{
		Kind: "db", Name: "no password", Origin: models.OriginManual,
		Parts: resource.Parts{"host": "db.example.com", "port": "5432",
			"database_name": "d", "username": "u"},
	})
	if err != nil {
		t.Fatalf("a resource with no secret must be writable: %v", err)
	}
	back, err := st.Resources().ByID(t.Context(), written.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if len(back.Secret) != 0 {
		t.Fatalf("a resource written with no secret came back with %v", back.Secret)
	}
}

// A module whose resource was deleted still has the database's name in its row, and until now
// nothing looked. It went on being told it had a database and was never given the one somebody
// had described for it, so it kept reaching for a database that no longer had its data — which
// from the outside is a network fault and is not one.
func TestAModuleWithNoResourceBehindItsDatabaseNameHoldsNothing(t *testing.T) {
	st := dbtest.Open(t)
	resources := st.Resources()

	module, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("orphan"), "http://module-deploy:8094", []byte("hash"), models.Manifest{})
	if err != nil {
		t.Fatalf("register a module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	// The name is left behind by a resource that is gone.
	if err := st.Integrations().SetModuleDatabase(t.Context(), module.ID, "a_deleted_db", ""); err != nil {
		t.Fatalf("record a database name: %v", err)
	}
	held, err := resources.IsHeldBy(t.Context(), module.ID)
	if err != nil {
		t.Fatalf("ask whether it holds one: %v", err)
	}
	if held {
		t.Fatal("a module with a name and no resource claims to hold one")
	}

	// And once there is a resource, the question answers differently.
	written, err := resources.Put(t.Context(), models.Resource{
		Kind: "db", Name: "the new one", Origin: models.OriginManual,
		Parts: resource.Parts{"host": "db.example.com", "database_name": "a_real_db", "username": "u"},
	})
	if err != nil {
		t.Fatalf("write a resource down: %v", err)
	}
	if err := resources.Grant(t.Context(), written.ID, module.ID, "database"); err != nil {
		t.Fatalf("give it to the module: %v", err)
	}
	held, err = resources.IsHeldBy(t.Context(), module.ID)
	if err != nil {
		t.Fatalf("ask again: %v", err)
	}
	if !held {
		t.Fatal("a module holding a resource claims to hold nothing")
	}
}

// The two ways of reading what a module holds differ in one thing, and it is the difference
// between a module that can open its database and one that cannot.
//
// This was a real failure and it looked like nothing: the resource was in place, the page showed
// it, and the module was refused registration with "no password" — because the list a handover
// is built from had its secrets shut, which is right for a page and wrong for handing over. Both
// callers were correct and they wanted opposite things, so the flag is named at the call rather
// than chosen once here.
func TestWhatAModuleHoldsIsReadWithTheSecretOnlyForAHandover(t *testing.T) {
	st := dbtest.Open(t)
	resources := st.Resources()

	module, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("secret"), "http://module-deploy:8094", []byte("hash"), models.Manifest{})
	if err != nil {
		t.Fatalf("register a module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	written, err := resources.Put(t.Context(), models.Resource{
		Kind: "db", Name: "with a password", Origin: models.OriginManaged,
		Parts: resource.Parts{"host": "postgres", "database_name": "d",
			"username": "u", "password": "the-password"},
	})
	if err != nil {
		t.Fatalf("write the resource down: %v", err)
	}
	if err := resources.Grant(t.Context(), written.ID, module.ID, "history"); err != nil {
		t.Fatalf("give it to the module: %v", err)
	}

	closed, err := resources.ByModule(t.Context(), module.ID, false)
	if err != nil {
		t.Fatalf("read it for a page: %v", err)
	}
	if len(closed) != 1 {
		t.Fatalf("a module holding one resource reads back %d", len(closed))
	}
	if closed[0].Secret["password"] != "" {
		t.Fatalf("a page was handed a password: %v", closed[0].Secret)
	}

	opened, err := resources.ByModule(t.Context(), module.ID, true)
	if err != nil {
		t.Fatalf("read it for a handover: %v", err)
	}
	if opened[0].Secret["password"] != "the-password" {
		t.Fatalf("a module would be given a password of %q, and refused registration for "+
			"having none while the page showed it there all along",
			opened[0].Secret["password"])
	}
}
