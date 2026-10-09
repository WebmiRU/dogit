package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// What a module's refusal looks like to a person looking at a page.
//
// This is the whole value of a refusal being a refusal and not an exception: the module said
// something specific, and it either reaches the page or it does not. When it did not, the page said
// "an unexpected error occurred" over "this cluster has no kubeconfig" — which is a sentence
// thrown away in favour of a sentence that is worse and true of nothing.

// A refusal carries the module's own words, and reaches the caller as a client error rather than
// as a crash: the request was fine, the module declined it, and the reader can act on the reason.
func TestARefusalReachesTheCallerWithItsReason(t *testing.T) {
	f := newModuleFixture(t)

	answer := json.RawMessage(`{"error":{"message":` +
		strconv.Quote(`cluster "local-k3s" has no kubeconfig, so there is no way in`) + `}}`)

	recorder := httptest.NewRecorder()
	f.server.writeError(recorder, httptest.NewRequest(http.MethodGet, "/", nil),
		moduleRefusalOf(f.module, "what is running", answer, nil))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("a refusal came back as %d, want 400 — the request was fine, the module declined it",
			recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "has no kubeconfig") {
		t.Errorf("the module's own sentence did not reach the page: %s", recorder.Body.String())
	}
}

// Silence is not a refusal, and must not wear its clothes. A page that cannot tell them apart
// shows a module's decision to a reader when what happened was that nobody answered.
func TestSilenceIsNotRefused(t *testing.T) {
	f := newModuleFixture(t)

	refusal := moduleRefusalOf(f.module, "what is running", nil, ErrNoAnswer)
	if refusal == nil {
		t.Fatal("silence was read as an answer")
	}
	if !errors.Is(refusal, ErrNoAnswer) {
		t.Error("the sentence lost the distinction between a refusal and a silence")
	}
	if strings.Contains(refusal.Error(), "said") {
		t.Errorf("a silence was reported as the module having said something: %v", refusal)
	}

	// And the caller can still tell, which is the reason ErrNoAnswer is wrapped rather than
	// swallowed.
	recorder := httptest.NewRecorder()
	f.server.writeError(recorder, httptest.NewRequest(http.MethodGet, "/", nil),
		moduleRefusalOf(f.module, "what is running", nil, ErrNoAnswer))
	if strings.Contains(recorder.Body.String(), "module said") {
		t.Errorf("the page was told the module said something: %s", recorder.Body.String())
	}
}

// An answer that happens to carry no digest is not a refusal either. The caller decides what it
// wanted; this only turns a decision into a sentence.
func TestAnAnswerIsNotARefusal(t *testing.T) {
	f := newModuleFixture(t)

	if refusal := moduleRefusalOf(f.module, "what is running",
		json.RawMessage(`{"digest":"sha256:abc"}`), nil); refusal != nil {
		t.Errorf("an answer was read as a failure: %v", refusal)
	}
	if refusal := moduleRefusalOf(f.module, "what is running",
		json.RawMessage(`{"images":[],"total":0}`), nil); refusal != nil {
		t.Errorf("an empty but successful answer was read as a failure: %v", refusal)
	}
}
