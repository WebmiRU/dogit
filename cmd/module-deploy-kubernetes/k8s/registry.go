package k8s

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The Docker Registry HTTP API, version 2, as much of it as an image needs to be found.
//
// The question being asked is the smallest one that has an honest answer: does this
// registry still serve this manifest? It is asked of the registry the place pulls from,
// with that place's credential, over the address the place pulls from — so the answer is
// about the pull that would actually happen and not about a registry that happens to hold
// the same layers somewhere else.
//
// Three answers rather than two, because "we could not tell" is not "no". A registry that
// answers 404 has said the manifest is not there; a registry that times out, refuses the
// credential, or answers 500 has said nothing at all, and reporting that as "missing"
// would turn a bad afternoon into a list of images that cannot be put back.

// Availability is what one registry said about one manifest.
type Availability string

const (
	// Present: the registry served the manifest.
	Present Availability = "present"
	// Missing: the registry said it does not have it.
	Missing Availability = "missing"
	// Unknown: the registry did not answer, and an answer that was not given is not a
	// "no".
	Unknown Availability = "unknown"
)

// probeTimeout is how long one registry gets to answer about one manifest.
//
// Short on purpose. Fifty images at three seconds each, five at a time, is half a minute
// of waiting for a page to draw itself, and an image that has been sitting in a registry
// that stopped answering an hour ago is just as absent as one that answers in a second.
// The page is not a health check.
const probeTimeout = 2500 * time.Millisecond

// probeWorkers is how many manifests are asked about at once.
const probeWorkers = 5

// manifestAccept is what a HEAD asks for. An image can be one of three things to a
// registry — a manifest, a manifest list, or an index — and a registry that is asked for
// one of them and serves another answers 404 for something it has, which would be reported
// as missing and block a rollback that would have worked.
const manifestAccept = "application/vnd.docker.distribution.manifest.v2+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.oci.image.index.v1+json"

// ImageReferenceAt is what to ask a registry about an image, at one address: the repository
// without its host, and the digest or the tag it is wanted by.
//
// The repository is moved to the address asked about first, because that is the name a pull
// would use — an image recorded at the address it was pushed to is at this address only if
// this place has not pulled from somewhere else since. A name with neither a digest nor a tag
// answers empty, because nothing can be fetched by it.
func ImageReferenceAt(image, address string) (path, reference string) {
	image = strings.TrimSpace(image)
	if at := strings.LastIndex(image, "@"); at > 0 {
		reference = strings.TrimSpace(image[at+1:])
		image = image[:at]
	}
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		if reference == "" {
			reference = strings.TrimSpace(image[colon+1:])
		}
		image = image[:colon]
	}
	// A reference that is nothing, or that is only its own separator, is not a reference:
	// there is no version to fetch by.
	if reference == "" || strings.HasSuffix(reference, ":") {
		return "", ""
	}

	// A record that carries a scheme is a record written by something that treats an
	// image name as an address, and taking the host off it here is the difference between
	// asking a registry about "team/app" and asking it about a repository called
	// "harbor.example.com".
	for _, scheme := range []string{"http://", "https://"} {
		if strings.HasPrefix(strings.ToLower(image), scheme) {
			image = image[len(scheme):]
			break
		}
	}

	repository := ImageAtRegistry(image, address)
	_, path, found := strings.Cut(repository, "/")
	if !found || strings.TrimSpace(path) == "" {
		return "", ""
	}
	return path, reference
}

// RegistryProbe asks one registry about manifests.
//
// The credential is the place's own, and insecureTLS is what the registry on the list says
// about its certificate: the same pair a pull would use, so a manifest reported as present
// is one this cluster could really pull.
type RegistryProbe struct {
	Address     string
	Username    string
	Token       string
	InsecureTLS bool
}

// has is whether this registry serves one manifest, and what it said.
func (p RegistryProbe) Has(ctx context.Context, path, reference string) Availability {
	if strings.TrimSpace(p.Address) == "" || strings.TrimSpace(path) == "" ||
		strings.TrimSpace(reference) == "" {
		return Unknown
	}
	// A path is a path and not a place to walk out of: the only paths that reach here are
	// image repositories the core built out of an image name, and this refuses anything
	// that is not one of them rather than trusting that.
	clean := strings.Trim(strings.TrimSpace(path), "/")
	if clean == "" || strings.Contains(clean, "..") {
		return Unknown
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodHead,
		p.url(clean, reference), nil)
	if err != nil {
		return Unknown
	}
	p.decorate(request, "")
	response, err := p.client().Do(request)
	if err != nil {
		return Unknown
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized {
		// A registry that issues tokens says so in a challenge rather than answering.
		// Following it is the whole of what makes this work against Docker Hub and
		// GitHub's registry, which are the two places somebody is most likely to be
		// pulling from.
		token, ok := p.token(ctx, response.Header.Get("WWW-Authenticate"), clean)
		if !ok || token == "" {
			return Unknown
		}
		return p.askAgain(ctx, clean, reference, token)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		return Present
	case response.StatusCode == http.StatusNotFound:
		return Missing
	default:
		return Unknown
	}
}

// askAgain is the same question with a token in hand.
func (p RegistryProbe) askAgain(ctx context.Context, path, reference, token string) Availability {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead,
		p.url(path, reference), nil)
	if err != nil {
		return Unknown
	}
	p.decorate(request, token)
	response, err := p.client().Do(request)
	if err != nil {
		return Unknown
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusOK:
		return Present
	case response.StatusCode == http.StatusNotFound:
		return Missing
	default:
		// A 401 with a token in hand is a token that was not accepted, and a 403 is a
		// registry refusing to say. Neither is evidence that the manifest is gone, and
		// both are answers a reader would act on wrongly if they were called missing.
		return Unknown
	}
}

// token is the bearer a challenge asks for, and whether there was one to get.
//
// One attempt and no retry: a registry that issues a token we then cannot use is a
// registry this cannot answer questions about, and asking twice would double the wait for
// an answer that is not coming.
func (p RegistryProbe) token(ctx context.Context, challenge, path string) (string, bool) {
	realm, parameters := parseChallenge(challenge)
	if realm == "" {
		return "", false
	}

	target, err := url.Parse(realm)
	if err != nil {
		return "", false
	}
	query := target.Query()
	if service := parameters["service"]; service != "" {
		query.Set("service", service)
	}
	// The scope is ours to write when the challenge does not carry one, and it is not
	// optional: the token service is asked "may this token pull this repository", and a
	// request that names no repository is refused as though it named none on purpose.
	// Docker's own client builds exactly this, and registry:2 — the registry this
	// instance runs — leaves it out of the challenge on purpose.
	scope := parameters["scope"]
	if scope == "" && path != "" {
		scope = "repository:" + path + ":pull"
	}
	if scope != "" {
		query.Set("scope", scope)
	}
	target.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", false
	}
	p.decorate(request, "")

	response, err := p.client().Do(request)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", false
	}

	var answer struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&answer); err != nil {
		return "", false
	}
	if answer.Token != "" {
		return answer.Token, true
	}
	return answer.AccessToken, answer.AccessToken != ""
}

// parseChallenge reads a WWW-Authenticate header.
//
// Only the Bearer form, and only what it needs: a registry that challenges with anything
// else is a registry whose protocol this does not speak, and there is nothing to be gained
// by pretending otherwise.
func parseChallenge(challenge string) (string, map[string]string) {
	challenge = strings.TrimSpace(challenge)
	if challenge == "" {
		return "", nil
	}
	scheme, rest, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "bearer") {
		return "", nil
	}
	parameters := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		parameters[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return parameters["realm"], parameters
}

// url is the address a manifest is asked for at.
//
// The scheme is http when the registry on the list says its certificate does not verify,
// because that is the same decision the cluster makes when it pulls, and https to a
// certificate nobody can verify is a request that fails before it is sent.
func (p RegistryProbe) url(path, reference string) string {
	scheme := "https"
	if p.InsecureTLS {
		scheme = "http"
	}
	address := strings.TrimRight(strings.TrimSpace(p.Address), "/")
	return fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, address,
		strings.TrimLeft(path, "/"), url.PathEscape(reference))
}

// decorate puts the credential on a request, in the form it was given in.
//
// A credential that is a token and nothing else goes as a bearer token, because that is
// what it is: this instance's own registry hands the core a token and keeps no password
// anywhere, and a token sent as a password with a made-up username is a credential
// shaped like a login that does not exist. A record's own login goes as basic auth, which
// is the only form a registry can exchange for a token.
//
// No credential at all is no header: a public mirror is reached anonymously, which is how
// anything else reaches it.
func (p RegistryProbe) decorate(request *http.Request, bearer string) {
	request.Header.Set("Accept", manifestAccept)
	switch {
	case bearer != "":
		request.Header.Set("Authorization", "Bearer "+bearer)
	case p.Username != "":
		request.SetBasicAuth(p.Username, p.Token)
	case p.Token != "":
		request.Header.Set("Authorization", "Bearer "+p.Token)
	}
}

func (p RegistryProbe) client() *http.Client {
	transport := &http.Transport{
		// No certificate check when the registry on the list says there is none to check,
		// which is the whole of what a self-signed registry has. Everywhere else the
		// default is used rather than loosened, because a probe is exactly where a
		// missed certificate check would go unnoticed.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: p.InsecureTLS},
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	return &http.Client{
		Transport: transport,
		Timeout:   probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ProbedImage is one manifest to ask about.
type ProbedImage struct {
	Path      string
	Reference string
}

// availabilityOf asks one registry about a page of manifests, and answers about each.
//
// Five at a time and no more: a page of ten images is not a lot of work, but a page of a
// thousand manifests asked of one registry at once is a request for a rate limit, and the
// answer that comes back is "unknown" for all of them, which is worse than no answer.
func (p RegistryProbe) AvailabilityOf(ctx context.Context, images []ProbedImage) []Availability {
	states := make([]Availability, len(images))
	if len(images) == 0 {
		return states
	}

	queue := make(chan int)
	var group sync.WaitGroup
	workers := probeWorkers
	if len(images) < workers {
		workers = len(images)
	}
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range queue {
				states[index] = p.Has(ctx, images[index].Path, images[index].Reference)
			}
		}()
	}
	for index := range images {
		queue <- index
	}
	close(queue)
	group.Wait()

	for index, state := range states {
		if state == "" {
			states[index] = Unknown
		}
	}
	return states
}
