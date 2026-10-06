package k8s

// What one registry said about one manifest, asked for over a real connection.
//
// A stubbed HTTP client would agree with whatever the code expects and prove nothing: the
// part of this that can be wrong is the wire — a challenge parsed wrongly, a token asked
// for with the wrong query, a header that sends the password where the protocol wants a
// bearer. So these tests run a server that behaves the way the registries behave, including
// the one that answers 401 with a challenge, and the only thing faked is where the server
// is.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A registry that has the manifest says 200, and one that has not says 404. Those are the
// two answers the whole of this rests on, and they are not interchangeable with anything
// else.
func TestARegistryAnswersForWhatItHas(t *testing.T) {
	probe, requests := registryWith(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v2/test/versions/manifests/sha256:here") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	if got := probe.Has(t.Context(), "test/versions", "sha256:here"); got != Present {
		t.Errorf("an image the registry has is %q", got)
	}
	if got := probe.Has(t.Context(), "test/versions", "sha256:gone"); got != Missing {
		t.Errorf("an image the registry does not have is %q", got)
	}

	// The path is the v2 path, the reference is escaped inside it, and the accept header
	// asks for all three kinds a manifest can be — a registry that serves an index and was
	// asked only for a manifest answers 404 for something it has.
	first := requests.first()
	if got := first.Method; got != http.MethodHead {
		t.Errorf("the manifest was asked for with %s, want a HEAD", got)
	}
	if got := first.URL.Path; got != "/v2/test/versions/manifests/sha256:here" {
		t.Errorf("the path asked for was %q", got)
	}
	if accept := first.Header.Get("Accept"); !strings.Contains(accept, "manifest.list") ||
		!strings.Contains(accept, "oci.image.index") {
		t.Errorf("the accept header names %q, which will 404 an image list", accept)
	}
}

// A registry that issues tokens challenges first, and this is the whole reason the client
// speaks the protocol rather than sending a password: the two registries somebody is most
// likely to pull from — Docker Hub and GitHub's — answer 401 to anything that is not a
// bearer token.
func TestARegistryThatIssuesTokensIsFollowed(t *testing.T) {
	var tokensAsked int32
	probe, requests := registryWith(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokensAsked, 1)
			if got := r.URL.Query().Get("service"); got != "registry.example.com" {
				t.Errorf("the token was asked for with service=%q", got)
			}
			if got := r.URL.Query().Get("scope"); got != "repository:test/versions:pull" {
				t.Errorf("the token was asked for with scope=%q", got)
			}
			if _, _, ok := r.BasicAuth(); !ok {
				t.Error("the token was asked for with no credential")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"a-token-of-our-own"}`))
			return
		}
		if r.Header.Get("Authorization") == "Bearer a-token-of-our-own" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// The realm as this server is really reached: a challenge that points somewhere
		// else is a challenge that cannot be followed, which is a test of nothing.
		w.Header().Set("WWW-Authenticate",
			`Bearer realm="http://`+r.Host+`/token",service="registry.example.com",`+
				`scope="repository:test/versions:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
	})

	if got := probe.Has(t.Context(), "test/versions", "sha256:here"); got != Present {
		t.Errorf("an image behind a token challenge is %q", got)
	}
	if atomic.LoadInt32(&tokensAsked) != 1 {
		t.Errorf("the token was asked for %d times, want once", atomic.LoadInt32(&tokensAsked))
	}
	if got := len(requests.all()); got != 3 {
		t.Errorf("the registry was asked %d times, want the challenge, the token and the retry", got)
	}
}

// Everything else is "we could not tell", and the difference matters: only an answer of 404
// says a manifest is gone, and a registry that is asleep has not said that.
func TestARegistryThatDoesNotAnswerHasNotSaidNo(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusTooManyRequests, http.StatusMethodNotAllowed} {
		probe, _ := registryWith(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		})
		if got := probe.Has(t.Context(), "test/versions", "sha256:any"); got != Unknown {
			t.Errorf("a registry answering %d said %q about the image, want that it did not say",
				code, got)
		}
	}

	// A credential that is refused is not an answer about the manifest either: the same
	// registry says 404 for an image it has not got and 401 for one it will not discuss,
	// and telling those apart is the whole job.
	refused, _ := registryWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	if got := refused.Has(t.Context(), "test/versions", "sha256:any"); got != Unknown {
		t.Errorf("a refused credential said %q about the image", got)
	}

	// Nothing answered at all: a registry nobody is running. Not a handler that says
	// nothing — one that answers 200 because that is what an empty handler does — but a
	// connection that is refused.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	address := strings.TrimPrefix(closed.URL, "http://")
	closed.Close()
	gone := RegistryProbe{Address: address, InsecureTLS: true}
	if got := gone.Has(t.Context(), "test/versions", "sha256:any"); got != Unknown {
		t.Errorf("a registry that is not there said %q about the image", got)
	}
}

// A path is a repository and not a place to walk out of, and a manifest that was never named
// is a question nobody can answer.
func TestWhatIsAskedForIsCheckedFirst(t *testing.T) {
	probe, requests := registryWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, c := range []struct{ path, reference string }{
		{"", "sha256:any"},
		{"   ", "sha256:any"},
		{"test/versions", ""},
		{"../../etc/passwd", "sha256:any"},
		{"test/../secrets", "sha256:any"},
	} {
		if got := probe.Has(t.Context(), c.path, c.reference); got != Unknown {
			t.Errorf("(%q, %q) was answered %q, want that nothing was asked", c.path, c.reference, got)
		}
	}
	if len(requests.all()) != 0 {
		t.Errorf("%d requests went out for a question that was never asked", len(requests.all()))
	}
}

// Whether a manifest is asked for over http or https is what the registry on the list says
// about its certificate, and nothing else. A probe that verified where the pull would not
// is a probe of a different registry, and the certificate is the same one the cluster will
// be asked about.
func TestTheSchemeIsWhatTheRegistryOnTheListSaysAboutIt(t *testing.T) {
	verifying := RegistryProbe{Address: "harbor.example.com"}
	if got := verifying.url("test/versions", "sha256:any"); !strings.HasPrefix(got, "https://") {
		t.Errorf("a registry with a certificate is asked for at %q", got)
	}
	if got := verifying.url("team/app", "sha256:any"); !strings.HasPrefix(got,
		"https://harbor.example.com/v2/team/app/manifests/") {
		t.Errorf("the path asked for was %q", got)
	}

	plain := RegistryProbe{Address: "192.168.1.103:8091", InsecureTLS: true}
	if got := plain.url("test/versions", "sha256:any"); !strings.HasPrefix(got,
		"http://192.168.1.103:8091/v2/test/versions/manifests/") {
		t.Errorf("a registry that does not verify its certificate is asked for at %q", got)
	}

	// A registry served under a prefix keeps it: the prefix is part of what an image is
	// named by, and asking without it would be asking a different registry.
	prefixed := RegistryProbe{Address: "harbor.example.com/v2", InsecureTLS: true}
	if got := prefixed.url("team/app", "sha256:any"); got !=
		"http://harbor.example.com/v2/v2/team/app/manifests/sha256:any" {
		t.Errorf("a prefixed registry is asked for at %q", got)
	}
}

// A page of manifests is asked about five at a time, and every row gets an answer of its
// own — which is checked here by a registry that answers according to the reference, so a
// row that kept another's answer would show up.
func TestAPageIsAskedAboutFiveAtATimeAndEachRowKeepsItsOwn(t *testing.T) {
	var running, highest int32
	probe, _ := registryWith(t, func(w http.ResponseWriter, r *http.Request) {
		now := atomic.AddInt32(&running, 1)
		for {
			was := atomic.LoadInt32(&highest)
			if now <= was || atomic.CompareAndSwapInt32(&highest, was, now) {
				break
			}
		}
		// Long enough that ten at once would be seen as ten at once.
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		if strings.HasSuffix(r.URL.Path, "sha256:0") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	images := make([]ProbedImage, 0, 10)
	for i := range 10 {
		images = append(images, ProbedImage{Path: "test/versions", Reference: "sha256:" + string(rune('0'+i))})
	}
	states := probe.AvailabilityOf(t.Context(), images)

	if got := atomic.LoadInt32(&highest); got > probeWorkers {
		t.Errorf("%d were asked about at once, want no more than %d", got, probeWorkers)
	}
	if got := highest; got < 2 {
		t.Errorf("only %d were asked about at once: this is a batch, not a queue", got)
	}
	if states[0] != Missing {
		t.Errorf("the image the registry does not have is %q", states[0])
	}
	for i := 1; i < 10; i++ {
		if states[i] != Present {
			t.Errorf("image %d is %q, want its own answer", i, states[i])
		}
	}
}

// What to ask about an image: the repository at the address being asked about, and the
// digest or the tag it is wanted by. An image recorded at the address it was pushed to is
// asked about under the name a pull at this address would use, and a name that could not be
// fetched at all is not asked about.
func TestWhatAnImageIsAskedForAt(t *testing.T) {
	const address = "192.168.1.103:8091"
	for _, c := range []struct{ image, path, reference string }{
		{address + "/test/versions@sha256:abc", "test/versions", "sha256:abc"},
		// The image was pushed to our registry and this place pulls from another one: the
		// repository is the place's, and the digest is what makes it the same image.
		{"192.168.1.103:8091/test/versions@sha256:abc", "test/versions", "sha256:abc"},
		{"192.168.1.103:8091/test/versions:v1.2", "test/versions", "v1.2"},
		{"https://harbor.example.com/v2/team/app@sha256:abc", "v2/team/app", "sha256:abc"},
		// A digest wins over a tag: an image pinned by digest is that digest wherever it
		// is served from, and the tag it also carries may have moved.
		{address + "/test/versions:v1.2@sha256:abc", "test/versions", "sha256:abc"},
		// Nothing to ask about: no version to fetch by, and a digest that is only its own
		// separator is not a version either.
		{address + "/test/versions", "", ""},
		{address + "/test/versions@sha256:", "", ""},
		{address + "/test/versions:", "", ""},
		{"", "", ""},
	} {
		path, reference := ImageReferenceAt(c.image, address)
		if path != c.path || reference != c.reference {
			t.Errorf("ImageReferenceAt(%q) = (%q, %q), want (%q, %q)",
				c.image, path, reference, c.path, c.reference)
		}
	}
}

// registryWith is a registry that behaves the way the test says, and the requests it
// received. Only the address is fake: everything above it is the real protocol.
//
// InsecureTLS is set because a test server speaks http, and http is what the client asks a
// registry that does not verify its certificate — which is the rule under test above, and is
// not the thing these tests are about.
func registryWith(t *testing.T, serve func(http.ResponseWriter, *http.Request)) (
	RegistryProbe, *seenRequests) {

	t.Helper()
	seen := &seenRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.add(&http.Request{
			Method: r.Method, URL: r.URL, Header: r.Header.Clone(),
		})
		serve(w, r)
	}))
	t.Cleanup(server.Close)

	return RegistryProbe{
		Address:     strings.TrimPrefix(server.URL, "http://"),
		Username:    "robot$deployer",
		Token:       "s3cret",
		InsecureTLS: true,
	}, seen
}

type seenRequests struct {
	mu   sync.Mutex
	seen []*http.Request
}

func (s *seenRequests) add(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, r)
}

func (s *seenRequests) all() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

func (s *seenRequests) first() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) == 0 {
		return &http.Request{}
	}
	return s.seen[0]
}
