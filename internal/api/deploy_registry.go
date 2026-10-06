package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// The registry a place pulls its images from.
//
// One registry per place, named on the place's own row, and exactly one — not a list with
// an order in it. A place that could pull the same image from several registries is a place
// where the answer to "which image is running" depends on which registry answered last, and
// two registries holding one image by name is two images with one name: the first one to be
// written to is the one that will ever be found. It is also why nobody needs the list — one
// registry holds many images, and it is the other way round that turns into a mess.
//
// What the place's setting buys is a route. The image was pushed once, to the registry this
// instance runs, and a place whose cluster cannot reach that address — through a mirror, or
// because the cluster is somewhere else entirely — is given the address it can reach. The
// digest does not change: it is the content, and the content is the same wherever it is
// served from. An address that serves something else does not quietly deploy something else
// either — the pull fails, out loud, with the registry's own reason in the pod's events.
type placePull struct {
	// Address is the registry host:port the cluster pulls from, and what the credential
	// is filed under. It has to match the image name exactly, which is why it is written
	// into the image rather than beside it.
	Address string
	// Username and Token are the credential, when there is one. A token goes in the
	// password field, which is the Docker convention every client follows; a registry that
	// issues neither is reached with its own login.
	Username string
	Token    string
	// Anonymous says there is nothing to authenticate with, so nothing is written into the
	// cluster. Said in the log, because "the cluster will pull with no credential" is the
	// answer to half of "why is this deployment failing".
	Anonymous bool
	// InsecureTLS is what the registry on the list says about its own certificate. It
	// changes nothing here: a cluster is told to accept a bad certificate by a flag on the
	// kubelet, not by a secret. It travels so the log can say what kind of registry this
	// is, which is the first thing anybody checks when a pull fails.
	InsecureTLS bool
	// Image is the image this place pulls: the same path and the same digest, at Address.
	Image string
	// TheInstanceRegistry says the place pulls from the registry this instance runs, which
	// is where the image was pushed and where the credential is a token of ours.
	TheInstanceRegistry bool
}

// credential is what goes to the module, or nil when there is nothing to write into the
// cluster: a public mirror needs no credential, and a secret built for an address nobody
// recognised would be a secret for nothing.
func (p *placePull) credential() *registryCredential {
	if p == nil || p.Anonymous || p.Address == "" {
		return nil
	}
	return &registryCredential{
		Address:    p.Address,
		Token:      p.Token,
		Username:   p.Username,
		SecretName: "dogit-registry",
	}
}

// pullSaid is what the deployment's log says about a registry beyond its address, in the
// words an operator would use: a credential of ours, no credential at all, a certificate
// that does not verify. All three are things people read a log looking for, and all three
// are invisible in a line that is only an address.
func pullSaid(p *placePull) string {
	switch {
	case p.Anonymous:
		return " (no credential: the cluster pulls from it without one)"
	case p.InsecureTLS:
		return " (a credential from the list of registries; its certificate does not verify)"
	default:
		return " (the cluster will pull with a credential of ours)"
	}
}

// placePullFor is where a place pulls from and what it pulls with, for a deployment.
//
// The place's own setting decides the address, and only the place's own setting: an address
// nobody wrote down for that place is never resolved here, so a module cannot ask for a
// credential to a registry the operator has not pointed that place at. The setting is read
// from the module's own rows through the core's store rather than taken on the module's
// word, because the module is the thing being asked — a module that could name any registry
// and be handed its password would make every registry on the instance its business.
//
// What the core knows about the address is then all it says. The address the image already
// carries is the registry the run pushed to, and a place that names nothing, or names that,
// deploys exactly as it did before any of this existed.
func (s *Server) placePullFor(ctx context.Context, project *models.Project,
	module *models.Integration, place, image string, log func(string, ...any)) (*placePull, error) {

	image = strings.TrimSpace(image)
	if image == "" {
		return nil, nil
	}
	repository, _ := splitImage(image)
	if repository == "" {
		return nil, nil
	}

	// The registry the image is already addressed to, which is where the run pushed it.
	instanceAddress := registryHostFrom(repository)

	configured := s.configuredRegistry(ctx, project, module, place, log)
	if configured == "" || sameRegistry(configured, instanceAddress) {
		return s.instanceRegistryPull(ctx, project, image, instanceAddress)
	}

	pull, err := s.registryCredentialFor(ctx, project, configured)
	if err != nil {
		return nil, err
	}
	pull.Image = imageAtRegistry(image, configured)
	return pull, nil
}

// placeRegistryCredential is what a rollback needs: the place's registry, when it names one.
//
// A rollback has no image of its own — the module reads it out of the record — so what the
// core can offer is the address and the credential for it, and nothing when the place names
// nothing. That case is the one that has always worked: the record carries the address, and
// the secret written by the last deployment is still in the namespace.
func (s *Server) placeRegistryCredential(ctx context.Context, project *models.Project,
	module *models.Integration, place string, log func(string, ...any)) (*registryCredential, error) {

	configured := s.configuredRegistry(ctx, project, module, place, log)
	if configured == "" {
		return nil, nil
	}
	pull, err := s.registryCredentialFor(ctx, project, configured)
	if err != nil {
		return nil, err
	}
	return pull.credential(), nil
}

// instanceRegistryPull is what a place pulls with when it pulls from the registry this
// instance runs: the address the image already carries, and a token minted for this project
// and nothing else.
//
// The same refusal as before any of this existed — an instance with no registry installed
// cannot mint a token, and a token minted for a job that built no image would be a
// credential for nothing at all.
func (s *Server) instanceRegistryPull(ctx context.Context, project *models.Project,
	image, address string) (*placePull, error) {

	registry, err := s.store.Integrations().ByKind(ctx, registryKind)
	if err != nil {
		return nil, fmt.Errorf("no registry is installed on this instance")
	}
	token, err := s.resolveToken(ctx, registry, project.Path)
	if err != nil {
		return nil, err
	}
	return &placePull{
		Address:             address,
		Token:               token,
		Image:               image,
		TheInstanceRegistry: true,
	}, nil
}

// registryCredentialFor is what the core knows about one address.
//
// Three answers, and which one it is depends only on what the core already holds: the
// address the registry module publishes is this instance's own and gets a token minted for
// the project; an address on the list of registries gets that record's login; and an address
// nobody has written down gets nothing at all — which is not a refusal but an admission. A
// public mirror needs no credential, and refusing it would be dogit insisting that every
// registry its clusters pull from is one dogit has heard of.
func (s *Server) registryCredentialFor(ctx context.Context, project *models.Project,
	address string) (*placePull, error) {

	address = registryAddressOf(address)
	pull := &placePull{Address: address, Anonymous: true}

	registry, err := s.store.Integrations().ByKind(ctx, registryKind)
	switch {
	case err == nil && registry.Enabled:
		if published, perr := s.registryAddress(ctx, registry); perr == nil && published != "" &&
			sameRegistry(published, address) {
			token, terr := s.resolveToken(ctx, registry, project.Path)
			if terr != nil {
				return nil, terr
			}
			pull.Token = token
			pull.Anonymous = false
			pull.TheInstanceRegistry = true
			return pull, nil
		}
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return nil, err
	}

	written, err := s.store.DockerRegistries().ByURL(ctx, address)
	switch {
	case err == nil:
		pull.Username = written.Login
		pull.Token = written.Password
		pull.Anonymous = written.Login == "" && written.Password == ""
		pull.InsecureTLS = written.InsecureTLS
	case errors.Is(err, store.ErrNotFound):
	default:
		return nil, err
	}
	return pull, nil
}

// configuredRegistry is the address the place's own row names.
//
// Read from the deploy module's own rows through the inheritance every module setting goes
// through, so a group can name a registry for its places and a project can change one
// without restating the rest. A place with no such setting, a row that has no field for it,
// or a row that cannot be read at all, means the instance's registry — which is what an
// empty row has always meant, and a preference that cannot be read must not stop a
// deployment that is otherwise perfectly deployable.
func (s *Server) configuredRegistry(ctx context.Context, project *models.Project,
	module *models.Integration, place string, log func(string, ...any)) string {

	place = strings.TrimSpace(place)
	if place == "" || module == nil {
		return ""
	}
	spec, found := settingSpecOf(module, "clusters")
	if !found || spec.Type != "list" || spec.Items == nil {
		return ""
	}

	projectID := project.ID
	settings, err := s.store.Integrations().SettingsFor(ctx, module.ID, project.GroupID,
		&projectID, module.Capabilities.Settings)
	if err != nil {
		if log != nil {
			log("  registry:   the place's registry could not be read (%v)\n", err)
		}
		return ""
	}
	raw, ok := settings["clusters"]
	if !ok {
		return ""
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		if log != nil {
			log("  registry:   the place's registry could not be read (%v)\n", err)
		}
		return ""
	}

	for _, row := range rows {
		var name string
		if err := json.Unmarshal(row["name"], &name); err != nil || strings.TrimSpace(name) != place {
			continue
		}
		address, ok := row[placeRegistryField]
		if !ok {
			return ""
		}
		var text string
		if err := json.Unmarshal(address, &text); err != nil {
			if log != nil {
				log("  registry:   the registry of place %q is not an address\n", place)
			}
			return ""
		}
		return registryAddressOf(text)
	}
	return ""
}

// placeRegistryField is the field, inside a place's row, that names its registry.
const placeRegistryField = "registry"

// registryAddressOf strips what an address is written with rather than what it is.
//
// A scheme and a trailing slash are how a person writes an address; neither is part of the
// host that an auth entry is keyed by or that an image name carries. Left in, "http://
// registry.example.com/" and "registry.example.com" compare as two different registries, and
// a place that names the registry its own image already carries would be treated as naming a
// different one — which would put it on an address nothing serves, and the pull would fail
// with the registry's own error rather than with anything that says what went wrong.
//
// A path is not stripped: "harbor.example.com/v2" is a registry served under a prefix, and
// the prefix is part of the address images are named by.
func registryAddressOf(address string) string {
	address = strings.TrimSpace(address)
	for _, scheme := range []string{"http://", "https://"} {
		if strings.HasPrefix(strings.ToLower(address), scheme) {
			address = address[len(scheme):]
			break
		}
	}
	return strings.TrimRight(address, "/")
}

// sameRegistry is whether two addresses name the same registry.
//
// Compared without a trailing slash and without regard to case, because an address written by
// a person and one written by a routing rule are the same place written twice — and a place
// that named the registry its own image already carries must not be treated as naming a
// different one, which would put it on somebody else's registry for no reason at all.
func sameRegistry(a, b string) bool {
	return strings.EqualFold(registryAddressOf(a), registryAddressOf(b))
}

// imageAtRegistry is the same image, at another registry.
//
// The path, the tag and the digest are all kept and only the host is replaced: the digest
// is the content, so an image served by a mirror of the same storage is the same image, and
// what a mirror of different storage is not — and that shows up as a pull that fails rather
// than as a deployment of something else, which is the whole reason the digest is pinned.
func imageAtRegistry(image, address string) string {
	image = strings.TrimSpace(image)
	address = registryAddressOf(address)
	if image == "" || address == "" {
		return image
	}
	// No slash means there is no host to replace: docker reads an unqualified name as
	// Docker Hub's, so the whole of "nginx:1.25" is the path and the address goes in front.
	slash := strings.Index(image, "/")
	if slash < 0 {
		return address + "/" + image
	}
	host := image[:slash]
	if sameRegistry(host, address) {
		return image
	}
	return address + "/" + image[slash+1:]
}
