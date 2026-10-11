package api

import (
	"encoding/json"
	"testing"

	"github.com/ewolf/dogit/internal/store"
)

// A name the project wrote is not a default, and this is the test that says so.
//
// The core works out an image name from the project — the module's template, or
// "{{group}}/{{project}}" — and writes it back onto the job so that everything said about
// the image afterwards can name it. That write-back is right for a job that named nothing,
// which is every configuration written before a build stage could name its outputs, and it
// is exactly wrong for one that did: a build stage with two outputs carries a different
// image in each, and a worked-out name gives both of them the same.
//
// Two builds then push the same tag. The second replaces the first. Each job records the
// digest of what it pushed, so both report success with two different digests, and the
// deployment resolves variables to two references of which only one can still be pulled.
// Nothing anywhere says so — which is how a run with two images in it is reported as a run
// that built two images.
func TestAnImageNameTheProjectWroteIsKept(t *testing.T) {
	credentials := map[string]any{"image": "registry.example/test/versions"}

	t.Run("a job that named its own image keeps it", func(t *testing.T) {
		job := &store.Job{Build: map[string]any{
			"image": "registry.example/test/versions-web",
		}}
		if got := imageNameForJob(job, credentials); got != "registry.example/test/versions-web" {
			t.Errorf("the name the project wrote became %q, want it left alone", got)
		}
	})

	t.Run("two outputs of one stage get two names", func(t *testing.T) {
		web := &store.Job{Build: map[string]any{"image": "registry.example/test/versions-web"}}
		content := &store.Job{Build: map[string]any{"image": "registry.example/test/versions-content"}}
		if web, content := imageNameForJob(web, credentials), imageNameForJob(content, credentials); web == content {
			t.Errorf("both outputs resolved to %q, so both would push the same tag", web)
		}
	})

	t.Run("a job that named nothing still gets the worked-out name", func(t *testing.T) {
		for _, build := range []map[string]any{nil, {}, {"image": ""}, {"image": "   "}} {
			job := &store.Job{Build: build}
			if got := imageNameForJob(job, credentials); got != "registry.example/test/versions" {
				t.Errorf("build %v became %q, want the name worked out from the project", build, got)
			}
		}
	})

	t.Run("no name anywhere leaves it unnamed rather than empty", func(t *testing.T) {
		if got := imageNameForJob(&store.Job{Build: map[string]any{}}, map[string]any{}); got != "" {
			t.Errorf("got %q, want no name at all", got)
		}
		if got := imageNameForJob(nil, map[string]any{"image": "registry.example/x"}); got != "registry.example/x" {
			t.Errorf("a job that is not there got %q, want the name from the credentials", got)
		}
	})
}

// The name on the job is the one the build actually pushed, so what the deploy resolves to
// is the image this run produced rather than the one the project would have had by default.
func TestTheRecordedNameIsWhatTheJobCarries(t *testing.T) {
	job := &store.Job{Build: map[string]any{"image": "registry.example/test/versions-web"}}
	name := imageNameForJob(job, map[string]any{"image": "registry.example/test/versions"})
	job.Build["image"] = name

	encoded, err := json.Marshal(job.Build)
	if err != nil {
		t.Fatalf("encode the build: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("decode it back: %v", err)
	}
	if back["image"] != "registry.example/test/versions-web" {
		t.Errorf("what the job carries is %v, want the project's own name", back["image"])
	}
}