package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The registry's half of the channel.
//
// What matters here is not that a command is answered — that is the client and the socket, and
// they are tested where they live — but the two things this module is responsible for: refusing
// the questions it will not answer, and answering a repeated command the same way both times.

// A command that arrives twice must not be worked out twice, because a tag can be re-pushed in
// between and the two answers would disagree. The core takes whichever it read first, and the
// other is a lie it has no way to detect.
func TestARepeatedCommandIsAnsweredFromWhatWasAlreadyWorkedOut(t *testing.T) {
	done := &answered{}

	if _, repeated := done.remember("cmd-1"); repeated {
		t.Fatal("the first time a command arrives is not a repeat")
	}
	done.record("cmd-1", []byte(`{"digest":"sha256:one"}`))

	had, repeated := done.remember("cmd-1")
	if !repeated {
		t.Fatal("the second time a command arrives was treated as a new question")
	}
	if string(had.said) != `{"digest":"sha256:one"}` {
		t.Errorf("the repeat was answered with %q, want what the first one was answered with", had.said)
	}
}

// Two copies of one command can be on the socket at once, and the second must wait rather than ask
// the registry again while the first is still asking.
func TestACommandIsClaimedBeforeItIsWorkedOut(t *testing.T) {
	done := &answered{}

	if _, repeated := done.remember("cmd-1"); repeated {
		t.Fatal("the first arrival is a repeat")
	}
	if _, repeated := done.remember("cmd-1"); !repeated {
		t.Error("a second arrival while the first is in flight was treated as a new question")
	}
}

// Different commands are different questions, however close together they arrive.
func TestDifferentCommandsAreNotConfusedWithEachOther(t *testing.T) {
	done := &answered{}

	done.record("cmd-1", []byte(`{"digest":"sha256:one"}`))
	if _, repeated := done.remember("cmd-2"); repeated {
		t.Error("a different command was read as a repeat of one already answered")
	}
}

// What is remembered has to stop being remembered. The core stops sending a command once its own
// command lifetime runs out and nothing here is asked about after that — so a map that only grows
// is a registry that leaks an entry per resolve for as long as it runs.
func TestWhatIsRememberedIsForgotten(t *testing.T) {
	done := &answered{}

	done.record("new", []byte(`{}`))
	done.record("old", []byte(`{}`))
	done.items["old"] = answer{said: []byte(`{}`), written: time.Now().Add(-2 * rememberedFor)}

	done.forget(rememberedFor)

	if _, found := done.items["old"]; found {
		t.Error("something older than the window is still remembered")
	}
	if _, found := done.items["new"]; !found {
		t.Error("something inside the window was forgotten")
	}
}

// A question this module will not answer, and why. Checked here and not only through the HTTP
// endpoint because the channel has to refuse in the same shape, and a refusal arriving in a shape
// nobody reads is a caller waiting for an answer that was never coming.
func TestAQuestionWithNoImageIsRefused(t *testing.T) {
	reg := &registry{core: &coreClient{baseURL: "http://core.invalid"}}

	_, refusal := reg.resolveTag(context.Background(), "grp", "  ", "v1", "token")
	if refusal == nil {
		t.Fatal("a question naming no image was answered")
	}
	if refusal.reason != "no image was named" {
		t.Errorf("refused with %q, want the reason that says what was missing", refusal.reason)
	}
}

// The permission is this module's whole policy, and it is asked of the core before the registry is
// touched. A pull that was not allowed must not reach the registry at all, and must be refused in
// the same words whichever way it was asked.
func TestARefusedPullNeverReachesTheRegistry(t *testing.T) {
	asked := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allowed":false,"reason":"this project may not pull"}`))
	}))
	defer core.Close()

	reached := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached++
	}))
	defer upstream.Close()

	restore := upstreamURL
	upstreamURL = upstream.URL
	defer func() { upstreamURL = restore }()

	reg := &registry{core: &coreClient{baseURL: core.URL, token: "module-token"}}

	_, refusal := reg.resolveTag(context.Background(), "grp", "grp/prj", "v1", "pull-token")
	if refusal == nil {
		t.Fatal("a pull that was not allowed was answered")
	}
	if refusal.status != http.StatusForbidden {
		t.Errorf("refused with status %d, want 403", refusal.status)
	}
	if asked != 1 {
		t.Errorf("the core was asked %d times about permission, want once", asked)
	}
	if reached != 0 {
		t.Errorf("the registry was asked %d times about an image the caller may not pull", reached)
	}
}

// A tag that is really there comes back as a digest, and the same digest is written into both the
// field the caller reads and the one it pins with. Two names for one value exist so that an older
// caller reading either of them is not broken, and they must not be able to disagree.
func TestAResolvedTagIsOneDigestWrittenTwice(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allowed":true}`))
	}))
	defer core.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:"+"0123456789abcdef")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	restore := upstreamURL
	upstreamURL = upstream.URL
	defer func() { upstreamURL = restore }()

	reg := &registry{core: &coreClient{baseURL: core.URL, token: "module-token"}}

	answer, refusal := reg.resolveTag(context.Background(), "grp", "grp/prj", "v1", "pull-token")
	if refusal != nil {
		t.Fatalf("a tag the registry holds was refused: %s", refusal.reason)
	}
	if answer.Digest != "sha256:0123456789abcdef" {
		t.Errorf("the tag resolved to %q", answer.Digest)
	}
	if answer.Pinned != answer.Digest {
		t.Errorf("pinned says %q and digest says %q, and they are one answer", answer.Pinned, answer.Digest)
	}
	if answer.Image != "grp/prj" || answer.Tag != "v1" {
		t.Errorf("the answer describes %s:%s, want grp/prj:v1", answer.Image, answer.Tag)
	}
}

// A registry that cannot say what a tag is must not be answered with something that looks like a
// digest. The whole point of resolving is to pin, and a pin to nothing is a deploy that silently
// used a tag.
func TestAQuietRegistryIsNotAnsweredWithNothing(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"allowed":true}`))
	}))
	defer core.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A 200 with no digest header: the one answer that cannot be turned into a pin.
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	restore := upstreamURL
	upstreamURL = upstream.URL
	defer func() { upstreamURL = restore }()

	reg := &registry{core: &coreClient{baseURL: core.URL, token: "module-token"}}

	answer, refusal := reg.resolveTag(context.Background(), "grp", "grp/prj", "v1", "pull-token")
	if refusal == nil {
		t.Fatalf("a registry that did not say a digest was answered with %+v", answer)
	}
	if answer.Digest != "" {
		t.Errorf("a refusal still carried the digest %q", answer.Digest)
	}
}
