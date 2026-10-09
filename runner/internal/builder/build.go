// Building an image, through the builder daemon in the same pod.
//
// The Go client rather than the buildctl command, and the reason is one field: in this version
// the digest comes back in SolveResponse.ExporterResponse["containerimage.digest"], a value
// the API hands over. Asking a command line tool for it means asking for a metadata file and
// hoping a file is where it was last time, which is a thing to be wrong about on a job whose
// whole output is one address.
package builder

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Credential is what the core minted for one build: a project, a registry and a token that
// stops working in two hours. Never a long-lived one — a runner that held a credential of its
// own would hold access to every project it ever built, for ever, and the record of a push
// would name a machine rather than a build.
type Credential struct {
	Server   string
	Username string
	Token    string
}

// Request is one image to build.
type Request struct {
	// ContextDir is the checkout, on this machine. It is a path and not a tarball because the
	// runner already has the repository; sending it up again would be a second copy of
	// something already on disk.
	ContextDir string
	// Dockerfile is inside the context, which is where a project keeps it. Naming the path
	// rather than assuming a root Dockerfile is deliberate: a project that keeps its build
	// somewhere else should not need its layout flattened to be buildable.
	Dockerfile string
	Target     string
	Args       map[string]string

	// Image is where the result goes, and Push decides whether it gets there. A digest for
	// an image nobody can pull is a digest of nothing, so a build that is not going to be
	// pushed says so rather than quietly producing one.
	Image string
	Push  bool

	Registry *Credential
}

// Result is what a finished build produced.
type Result struct {
	// Digest is the address. Not a tag: dogit deploys by digest, and a tag is a promise that
	// something else may move before the deployment reads it.
	Digest  string
	Steps   int
	Cached  int
	Started time.Time
	Ended   time.Time
	// Failed is the step that broke, named. "the build failed" is what a log says when
	// nobody looked; the step name is what the person reading it needs.
	Failed string
}

func (r Result) Duration() time.Duration {
	if r.Started.IsZero() || r.Ended.IsZero() {
		return 0
	}
	return r.Ended.Sub(r.Started)
}

// Progress is one piece of news from a build, as it happens rather than at the end.
//
// Called from a single goroutine, so a handler may write to a channel or a file without
// locking, and is not called after Build returns.
type Progress struct {
	Step   string
	Line   string
	Cached bool
	Failed bool
	// Failed is a step that broke, and Line is a line of that step's output. A build page
	// with a live log is the difference between watching a build and watching a spinner.
}

// Client is a connection to one builder.
type Client struct {
	inner *client.Client
}

// Connect opens a client, with the credential this runner holds in memory.
//
// The transport credentials are assembled here and handed over as a gRPC dial option rather
// than as BuildKit's WithCredentials, which takes a file path and reads it on every dial. That
// is the whole reason the key can be deleted after startup: there is nothing left for the
// client to go back and read.
func Connect(ctx context.Context, address, serverName string, credential *Credentials) (*Client, error) {
	inner, err := client.New(ctx, "tcp://"+address,
		client.WithGRPCDialOption(
			grpc.WithTransportCredentials(credentials.NewTLS(credential.TLSConfig(serverName))),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to the builder at %s: %w", address, err)
	}
	return &Client{inner: inner}, nil
}

func (c *Client) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

// Build runs one solve and returns the address of what it made.
//
// onProgress may be nil. The solve is not reported to the core as it goes — that is the
// runner's job and it needs the core's endpoint, which this package has no business knowing.
func (c *Client) Build(ctx context.Context, request Request, onProgress func(Progress)) (Result, error) {
	result := Result{Started: time.Now()}

	context, err := fsutil.NewFS(request.ContextDir)
	if err != nil {
		return result, fmt.Errorf("read the build context at %s: %w", request.ContextDir, err)
	}
	// No Close to defer: fsutil.FS is Walk and Open and nothing else.

	attributes := map[string]string{"filename": request.Dockerfile}
	if request.Target != "" {
		attributes["target"] = request.Target
	}
	for name, value := range request.Args {
		attributes["build-arg:"+name] = value
	}

	export := map[string]string{"name": request.Image}
	if request.Push {
		export["push"] = "true"
	}
	option := client.SolveOpt{
		// The dockerfile frontend, so that a project's Dockerfile means what it means
		// everywhere else. nil for the definition, which is what the frontend is for.
		Frontend:      "dockerfile.v0",
		FrontendAttrs: attributes,
		LocalMounts: map[string]fsutil.FS{
			"context":    context,
			"dockerfile": context,
		},
		Exports: []client.ExportEntry{{Type: "image", Attrs: export}},
	}
	if request.Registry != nil {
		option.Session = []session.Attachable{registryAuth(request.Registry)}
	}

	// BuildKit re-sends the whole vertex list on every update, so a step already reported
	// has to be remembered or the log says "step 3 done" forty times.
	reported := map[string]bool{}
	// Digest to step name, so that a line of output can say which step produced it. A log
	// line attributed to "sha256:9f2a…" is attributed to nothing.
	names := map[string]string{}

	statuses := make(chan *client.SolveStatus)
	var consumer sync.WaitGroup
	consumer.Add(1)
	go func() {
		defer consumer.Done()
		for status := range statuses {
			for _, vertex := range status.Vertexes {
				names[string(vertex.Digest)] = vertex.Name
				if vertex.Completed == nil || reported[string(vertex.Digest)] {
					continue
				}
				reported[string(vertex.Digest)] = true
				result.Steps++
				if vertex.Cached {
					result.Cached++
				}
				if vertex.Error != "" {
					result.Failed = vertex.Name
				}
				if onProgress != nil {
					onProgress(Progress{
						Step:   vertex.Name,
						Cached: vertex.Cached,
						Failed: vertex.Error != "",
					})
				}
			}
			if onProgress == nil {
				continue
			}
			for _, line := range status.Logs {
				onProgress(Progress{
					Step: names[string(line.Vertex)],
					Line: strings.TrimRight(string(line.Data), "\n"),
				})
			}
		}
	}()

	// Solve on this goroutine and the consumer on its own, which is the order buildctl uses:
	// solve closes the channel, so ranging it to the end is what tells us the stream is over
	// rather than a guess.
	response, err := c.inner.Solve(ctx, nil, option, statuses)
	consumer.Wait()
	result.Ended = time.Now()

	if err != nil {
		return result, fmt.Errorf("build %s: %w", request.Image, err)
	}
	if result.Failed != "" {
		return result, fmt.Errorf("step %q failed", result.Failed)
	}

	result.Digest = response.ExporterResponse["containerimage.digest"]
	if result.Digest == "" {
		// A build that says it succeeded and cannot say what it made is not a build to
		// report as a success, and the address is the entire point of the thing.
		return result, fmt.Errorf("the builder finished but named no image; the exporter said %v",
			keysOf(response.ExporterResponse))
	}
	return result, nil
}

// registryAuth hands the builder one credential, for one host.
//
// Written as a provider rather than as a config file on disk, because a file in a pod is a
// credential at rest for no benefit, and because the core issues a different token for every
// project — a config file has to be rewritten each time, and a file that is rewritten is a
// file whose old contents outlive it on a layer or in a log.
func registryAuth(credential *Credential) session.Attachable {
	return authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{
		AuthConfigProvider: func(_ context.Context, host string, _ []string, _ authprovider.ExpireCachedAuthCheck) (types.AuthConfig, error) {
			// Empty and not an error for every other host, which was a bug: BuildKit asks
			// about every host a build touches, including Docker Hub for the base image, and
			// an error there stops the build before it has compiled anything. Empty means
			// "no credential", and for a public image that is the truth — the pull goes out
			// anonymously and comes back. A private registry the runner has no credential for
			// answers 401 itself, which is a better sentence than one invented here.
			if !strings.EqualFold(host, credential.Server) {
				return types.AuthConfig{}, nil
			}
			return types.AuthConfig{
				Username:      credential.Username,
				Password:      credential.Token,
				ServerAddress: credential.Server,
			}, nil
		},
	})
}

func keysOf(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
