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

// An address nobody wrote down is a public mirror, and it is served as one: no credential is
// invented for it and nothing is written into the cluster. Refusing it would be dogit
// insisting that every registry its clusters pull from is one dogit has heard of.
func TestAnAddressNobodyWroteDownIsPulledFromWithoutACredential(t *testing.T) {
	f := newPlaceRegistryFixture(t)

	pull, err := f.placePull(t, "local-k3s", "192.168.1.103:8091/test/versions@sha256:abc",
		map[string]string{"registry": "mirror.gcr.io"})
	if err != nil {
		t.Fatalf("working out the registry: %v", err)
	}
	if !pull.Anonymous {
		t.Error("a registry nobody wrote down is not treated as one needing no credential")
	}
	if pull.credential() != nil {
		t.Error("a secret is being written into the cluster for a registry that needs none")
	}
	if pull.Image != "mirror.gcr.io/test/versions@sha256:abc" {
		t.Errorf("the image is %q", pull.Image)
	}
}

// A place that names nothing is the case that has always worked, and it has to keep working
// byte for byte: the address the image carries, and a token of ours.
func TestAPlaceThatNamesNothingIsServedAsItAlwaysWas(t *testing.T) {
	f := newPlaceRegistryFixture(t)
	const image = "192.168.1.103:8091/test/versions@sha256:abc"

	for _, fields := range []map[string]string{
		{},                  // the row has no such field at all
		{"registry": ""},    // the field, left empty
		{"registry": "   "}, // the field, with nothing in it
	} {
		pull, err := f.placePull(t, "local-k3s", image, fields)
		if err != nil {
			t.Fatalf("working out the registry for %v: %v", fields, err)
		}
		if !pull.TheInstanceRegistry || pull.Image != image || pull.Token == "" {
			t.Errorf("for %v: %+v — the instance's own registry, unchanged, is what this must be", fields, pull)
		}
	}
}

// A place whose row cannot be read is not refused: the deployment is real and the setting is
// a preference, so the preference is reported and the deployment goes ahead with the address
// the image carries.
func TestAPlaceRowThatCannotBeReadDoesNotStopTheDeployment(t *testing.T) {
	f := newPlaceRegistryFixture(t)

	said := ""
	pull, err := f.placePullWithLog(t, "no-such-place",
		"192.168.1.103:8091/test/versions@sha256:abc",
		func(format string, args ...any) { said = format })
	if err != nil {
		t.Fatalf("working out the registry: %v", err)
	}
	if pull.Address != "192.168.1.103:8091" || !pull.TheInstanceRegistry {
		t.Errorf("a place nobody configured is served as %+v", pull)
	}
	// Nothing is said about a registry that could not be read when there was no failure to
	// report: an unset place is not a broken one.
	if said != "" {
		t.Errorf("a place that simply names nothing produced %q", said)
	}
}

// The credential a rollback is given is the place's registry, and nothing at all when the
// place names none — which is the case that already worked, where the record carries the
// address and the secret from the last deployment is still in the namespace.
func TestARollbackIsToldItsPlacesRegistryOrNothing(t *testing.T) {
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

	f.setPlace(t, "local-k3s", nil)
	credential, err = f.placeRegistryCredential(t, "local-k3s")
	if err != nil {
		t.Fatalf("working out the registry: %v", err)
	}
	if credential != nil {
		t.Errorf("a rollback of a place that names no registry was told %+v", credential)
	}
}

// The setting is validated as an address while the operator is looking at the field, and it
// is not validated against the list of registries: an address nobody wrote down is a public
// mirror, and the field has to accept one.
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
		dbtest.Unique("registry"), "http://module-registry:8091", []byte("hash"),
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
		dbtest.Unique("deploy"), "http://module-deploy:8094", []byte("hash"),
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
