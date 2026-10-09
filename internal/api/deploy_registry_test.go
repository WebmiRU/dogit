package api

import (
	"encoding/json"
	"testing"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// The image is the same image wherever it is fetched from: the path and the digest stay and
// only the host changes. This is what makes a place's registry a route rather than a
// different thing, and it is also what keeps a mirror of different storage from quietly
// becoming a deployment of something else.
func TestTheImageMovesToAnotherRegistryAndKeepsItsDigest(t *testing.T) {
	cases := []struct {
		image, address, want string
	}{
		{"192.168.1.103:8091/test/versions@sha256:abc123", "mirror.example.com",
			"mirror.example.com/test/versions@sha256:abc123"},
		{"192.168.1.103:8091/test/versions@sha256:abc123", "mirror.example.com:5000",
			"mirror.example.com:5000/test/versions@sha256:abc123"},
		{"registry.example.com/group/app:v1.2", "harbor.example.com/v2",
			"harbor.example.com/v2/group/app:v1.2"},
		// The same address written two ways is the same address, and an image already
		// there is left exactly as it is rather than being taken apart and put back.
		{"192.168.1.103:8091/test/versions@sha256:abc", "192.168.1.103:8091/",
			"192.168.1.103:8091/test/versions@sha256:abc"},
		{"nginx:1.25", "registry.example.com", "registry.example.com/nginx:1.25"},
		// A person writes a scheme and a trailing slash; an image name carries neither.
		// Left in, the address would not match the one the image already has, and the
		// workload would be pointed at a host that serves nothing.
		{"192.168.1.103:8091/test/versions@sha256:abc", "https://192.168.1.103:8091/",
			"192.168.1.103:8091/test/versions@sha256:abc"},
		// A path is not a scheme: a registry served under a prefix is named with it.
		{"192.168.1.103:8091/test/versions@sha256:abc", "https://harbor.example.com/v2/",
			"harbor.example.com/v2/test/versions@sha256:abc"},
		// Nothing to move, nothing moved.
		{"", "mirror.example.com", ""},
		{"192.168.1.103:8091/test/versions@sha256:abc", "", "192.168.1.103:8091/test/versions@sha256:abc"},
	}

	for _, c := range cases {
		if got := imageAtRegistry(c.image, c.address); got != c.want {
			t.Errorf("imageAtRegistry(%q, %q) = %q, want %q", c.image, c.address, got, c.want)
		}
	}
}

// A place that names the registry its image already carries is naming this instance's
// registry, and is served by it: no rewrite, and the token of ours as before. Getting this
// wrong would put a place onto somebody else's registry for no reason at all.
func TestNamingTheInstanceRegistryChangesNothing(t *testing.T) {
	f := newPlaceRegistryFixture(t)

	pull, err := f.placePull(t, "local-k3s", "192.168.1.103:8091/test/versions@sha256:abc",
		map[string]string{"registry": "192.168.1.103:8091"})
	if err != nil {
		t.Fatalf("working out the registry: %v", err)
	}
	if !pull.TheInstanceRegistry {
		t.Error("a place naming the instance's registry is not treated as pulling from it")
	}
	if pull.Image != "192.168.1.103:8091/test/versions@sha256:abc" {
		t.Errorf("the image was rewritten to %q", pull.Image)
	}
	if pull.Token == "" {
		t.Error("no credential was minted for the instance's own registry")
	}
}

// An address on the list of registries brings that record's own login, because the cluster
// is being sent to somebody else's registry and the credential dogit holds for it is the one
// that will get it in.
func TestARegistryOnTheListBringsItsOwnLogin(t *testing.T) {
	f := newPlaceRegistryFixture(t)

	harbor := dbtest.Unique("harbor.example.com")
	written := store.DockerRegistry{
		URL: harbor, Login: "robot$ci", Password: "s3cret",
		InsecureTLS: true,
	}
	if err := f.store.DockerRegistries().Create(t.Context(), &written); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}

	pull, err := f.placePull(t, "local-k3s", "192.168.1.103:8091/test/versions@sha256:abc",
		map[string]string{"registry": harbor})
	if err != nil {
		t.Fatalf("working out the registry: %v", err)
	}
	if pull.Address != harbor {
		t.Errorf("pulling from %q", pull.Address)
	}
	if pull.Username != "robot$ci" || pull.Token != "s3cret" {
		t.Errorf("the credential is %q/%q, want the one on the list", pull.Username, pull.Token)
	}
	if !pull.InsecureTLS {
		t.Error("the registry's own note about its certificate was dropped")
	}
	if pull.Image != harbor+"/test/versions@sha256:abc" {
		t.Errorf("the image is %q", pull.Image)
	}
	if pull.TheInstanceRegistry {
		t.Error("somebody else's registry is reported as this instance's own")
	}
}

// An address nobody wrote down is refused, and the sentence says what to do about it.
//
// It used to be served as a public mirror, with no credential and nothing written into the
// cluster. That was dogit guessing that an address it had never heard of needed no
// credential, and the guess being wrong is a cluster at ImagePullBackOff for a rollout
// timeout with every event in the pod talking about a registry rather than about the
// deployment. Writing the address down takes one line and turns the guess into a fact.
func TestAnAddressNobodyWroteDownIsARefusal(t *testing.T) {
	f := newPlaceRegistryFixture(t)
	// An address of this test's own, because this database is shared with tests that write
	// registries down and one that had written this one would be answered from the list.
	mirror := dbtest.Unique("mirror.example.com")

	pull, err := f.placePull(t, "local-k3s", "192.168.1.103:8091/test/versions@sha256:abc",
		map[string]string{"registry": mirror})
	sentence, refused := registryRefusal(err)
	if !refused {
		t.Fatalf("a registry nobody wrote down was served as %+v, want a refusal", pull)
	}
	want := "the registry chosen for local-k3s is not in the list of registries"
	if sentence != want {
		t.Errorf("the refusal says %q, want %q", sentence, want)
	}

	// And once it is written down, the same address is served the way any registry on the
	// list is: with its own credential.
	written := store.DockerRegistry{URL: mirror, Login: "robot"}
	if err := f.store.DockerRegistries().Create(t.Context(), &written); err != nil {
		t.Fatalf("write the mirror down: %v", err)
	}
	pull, err = f.placePull(t, "local-k3s", "192.168.1.103:8091/test/versions@sha256:abc",
		map[string]string{"registry": mirror})
	if err != nil {
		t.Fatalf("working out the registry: %v", err)
	}
	if pull.Address != mirror || pull.Username != "robot" ||
		pull.Image != mirror+"/test/versions@sha256:abc" {
		t.Errorf("a written-down mirror is served as %+v", pull)
	}
}

// A place that names nothing is refused, and it is refused in the three shapes that all mean
// nothing: no field, an empty field, and a field holding whitespace.
//
// This used to mean the instance's own registry, which is why adding the field did not
// break anybody and also why nothing could tell whether a place had ever been looked at. A
// deployment now says which place needs a registry and where to say it.
func TestAPlaceThatNamesNothingIsRefused(t *testing.T) {
	f := newPlaceRegistryFixture(t)
	const image = "192.168.1.103:8091/test/versions@sha256:abc"

	for _, fields := range []map[string]string{
		{},                  // the row has no such field at all
		{"registry": ""},    // the field, left empty
		{"registry": "   "}, // the field, with nothing in it
	} {
		pull, err := f.placePull(t, "local-k3s", image, fields)
		sentence, refused := registryRefusal(err)
		if !refused {
			t.Fatalf("for %v: served as %+v, want a refusal", fields, pull)
		}
		want := "local-k3s has no registry chosen — choose one in the deploy module's settings"
		if sentence != want {
			t.Errorf("for %v: the refusal says %q, want %q", fields, sentence, want)
		}
	}
}

// A place nobody configured is refused the same way, because there is no row to have said
// anything: the setting is not a preference that can be left unread, it is where the images
// come from.
func TestAPlaceNobodyConfiguredIsRefusedAsWell(t *testing.T) {
	f := newPlaceRegistryFixture(t)

	pull, err := f.placePullWithLog(t, "no-such-place",
		"192.168.1.103:8091/test/versions@sha256:abc",
		func(format string, args ...any) {})
	_, refused := registryRefusal(err)
	if !refused {
		t.Fatalf("a place nobody configured was served as %+v, want a refusal", pull)
	}
}

// The credential a rollback is given is the place's registry, under the same rule a
// deployment is held to — so a place that names none cannot be rolled back at all.
func TestARollbackIsToldItsPlacesRegistryOrRefusedWithoutOne(t *testing.T) {
	f := newPlaceRegistryFixture(t)

	harbor := dbtest.Unique("harbor.example.com")
	written := store.DockerRegistry{URL: harbor, Login: "robot$ci", Password: "s3cret"}
	if err := f.store.DockerRegistries().Create(t.Context(), &written); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}
	f.setPlace(t, "local-k3s", map[string]string{"registry": harbor})

	credential, err := f.placeRegistryCredential(t, "local-k3s")
	if err != nil {
		t.Fatalf("working out the registry: %v", err)
	}
	if credential == nil || credential.Address != harbor ||
		credential.Username != "robot$ci" || credential.Token != "s3cret" {
		t.Fatalf("a rollback was told %+v, want harbor's credential", credential)
	}
	if credential.SecretName != "dogit-registry" {
		t.Errorf("the secret is called %q", credential.SecretName)
	}

	// Without a registry there is nothing to tell the module, and a rollback sent anyway
	// would look for a secret in the namespace that no deployment was written to leave
	// there. It used to be sent exactly like that, which is how a rollback kept working
	// while the setting it depended on went unread.
	f.setPlace(t, "local-k3s", nil)
	credential, err = f.placeRegistryCredential(t, "local-k3s")
	sentence, refused := registryRefusal(err)
	if !refused {
		t.Fatalf("a rollback of a place that names no registry was told %+v, want a refusal",
			credential)
	}
	if sentence != "local-k3s has no registry chosen — choose one in the deploy module's settings" {
		t.Errorf("the refusal says %q", sentence)
	}

	// The same for an address that is not on the list: a rollback is a pull of a version
	// that is not in the cluster any more, and there is no stored secret to fall back on.
	f.setPlace(t, "local-k3s", map[string]string{"registry": dbtest.Unique("nowhere.example.com")})
	credential, err = f.placeRegistryCredential(t, "local-k3s")
	if _, refused := registryRefusal(err); !refused {
		t.Errorf("a rollback from a registry nobody wrote down was told %+v", credential)
	}
}

// The setting is validated as an address while the operator is looking at the field, and it
// is not validated against the list of registries: the point of the field is to name the
// address somebody needs, and whether it is one this instance knows about is a separate
// question that a deployment asks and answers in a sentence.
func TestARegistryFieldIsValidatedAsAnAddress(t *testing.T) {
	spec := models.SettingSpec{Key: "registry", Label: "Registry", Type: "registry"}

	for _, good := range []string{"", "harbor.example.com", "192.168.1.103:8091",
		"https://harbor.example.com/v2", "[::1]:5000", "mirror.gcr.io"} {
		value, _ := json.Marshal(good)
		if err := checkSettingType(spec, value); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
	for _, bad := range []string{"harbor example.com", "ftp://harbor.test", "https://",
		"harbor.example.com:notaport", "::1:5000"} {
		value, _ := json.Marshal(bad)
		if err := checkSettingType(spec, value); err == nil {
			t.Errorf("%q was accepted as a registry address", bad)
		}
	}
	// And a value that is not text at all is refused rather than read as an address.
	if err := checkSettingType(spec, json.RawMessage(`{"address":"harbor.example.com"}`)); err == nil {
		t.Error("an object was accepted where an address belongs")
	}
}

// A place's registry is one registry, and that is enforced where it is written rather than
// hoped for: a field of any other type in the same row is still the core's business, and a
// row that names two registries cannot be built out of one field.
func TestTheRegistryIsOneFieldInAPlacesRow(t *testing.T) {
	spec := models.SettingSpec{
		Key: "clusters", Label: "Clusters", Type: "list",
		Items: &models.SettingItems{
			Identify: []string{"name"},
			Fields: []models.SettingSpec{
				{Key: "name", Label: "Name", Type: "string"},
				{Key: "registry", Label: "Registry", Type: "registry"},
			},
		},
	}

	rows, _ := json.Marshal([]map[string]string{{"name": "local-k3s", "registry": "harbor.example.com"}})
	if err := checkSettingType(spec, rows); err != nil {
		t.Fatalf("a place naming a registry was refused: %v", err)
	}

	bad, _ := json.Marshal([]map[string]string{{"name": "local-k3s", "registry": "harbor example.com"}})
	if err := checkSettingType(spec, bad); err == nil {
		t.Error("a place naming something that is not an address was accepted")
	}
}

// placeRegistryFixture is an instance with a registry module, a deploy module with one place,
// and the two ways of asking what that place would pull from.
type placeRegistryFixture struct {
	store    *store.Store
	project  *models.Project
	deployer *models.Integration
	registry *models.Integration
}

func newPlaceRegistryFixture(t *testing.T) *placeRegistryFixture {
	t.Helper()

	st := dbtest.Open(t)
	for _, kind := range []string{registryKind, "deploy:kubernetes"} {
		if _, err := st.Pool().Exec(t.Context(),
			`DELETE FROM integrations WHERE kind = $1`, kind); err != nil {
			t.Fatalf("clear the modules left by earlier tests: %v", err)
		}
	}

	registry, err := st.Integrations().Register(t.Context(), registryKind,
		dbtest.Unique("registry"), "http://module-registry:8091",
		models.Manifest{
			Routing: models.RoutingSpec{Domains: []string{"192.168.1.103:8091"}},
			Settings: []models.SettingSpec{
				{Key: "public_address", Label: "Public address", Type: "string"},
			},
		})
	if err != nil {
		t.Fatalf("register the registry module: %v", err)
	}

	deployer, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("deploy"), "http://module-deploy:8094",
		models.Manifest{
			Settings: []models.SettingSpec{{
				Key: "clusters", Label: "Clusters", Type: "list",
				Items: &models.SettingItems{
					Identify: []string{"name"},
					Fields: []models.SettingSpec{
						{Key: "name", Label: "Name", Type: "string"},
						{Key: "registry", Label: "Registry", Type: "registry"},
					},
				},
			}},
		})
	if err != nil {
		t.Fatalf("register the deploy module: %v", err)
	}

	project := dbtest.NewProject(t, st, "place-registry", nil)
	return &placeRegistryFixture{
		store: st, project: project, deployer: deployer, registry: registry,
	}
}

// setPlace writes what the place's row says, which is what the core reads it back through.
func (f *placeRegistryFixture) setPlace(t *testing.T, place string, fields map[string]string) {
	t.Helper()
	if _, err := f.store.Pool().Exec(t.Context(),
		`DELETE FROM integration_settings WHERE integration_id = $1`, f.deployer.ID); err != nil {
		t.Fatalf("clear the place rows: %v", err)
	}

	row := map[string]any{"name": place, "default_namespace": "dogit-dev"}
	for key, value := range fields {
		row[key] = value
	}
	rows, err := json.Marshal([]map[string]any{row})
	if err != nil {
		t.Fatalf("describe the place row: %v", err)
	}
	if _, err := f.store.Pool().Exec(t.Context(),
		`INSERT INTO integration_settings (integration_id, scope_type, key, value)
		 VALUES ($1, 'instance', 'clusters', $2)`, f.deployer.ID, rows); err != nil {
		t.Fatalf("write the place row: %v", err)
	}
}

// placePull is what a deployment of this image to this place would pull from.
func (f *placeRegistryFixture) placePull(t *testing.T, place, image string,
	fields map[string]string) (*placePull, error) {
	t.Helper()
	if fields != nil {
		f.setPlace(t, place, fields)
	}
	return f.placePullWithLog(t, place, image, nil)
}

func (f *placeRegistryFixture) placePullWithLog(t *testing.T, place, image string,
	log func(string, ...any)) (*placePull, error) {
	t.Helper()

	s := &Server{store: f.store, cfg: &config.Config{}, log: logger.Discard()}
	return s.placePullFor(t.Context(), f.project, f.deployer, place, image, log)
}

func (f *placeRegistryFixture) placeRegistryCredential(t *testing.T, place string) (*registryCredential, error) {
	t.Helper()

	s := &Server{store: f.store, cfg: &config.Config{}, log: logger.Discard()}
	return s.placeRegistryCredential(t.Context(), f.project, f.deployer, place, nil)
}
