package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// One question, one answer, whichever way the question arrived.
//
// This module serves two transports and they are not interchangeable. A question arrives either
// as an HTTP request — from a core older than its channel, or from something speaking the
// deploy module's own protocol — or as a message on the channel this module keeps open to the
// core. Both end up in the same function, which takes the question and gives an answer, because a
// question that can be answered two ways will eventually be answered two different ways.
//
// # What is not here, and why
//
// `outcome` is one answer. A deployment narrates as it goes, line by line, for as long as a
// rollout takes, and that is a stream rather than an answer — so `/deploy` and `/revert` stay on
// HTTP and are not wrapped in one of these.
//
// The reason is the thing that would be lost. A deployment's live log is what a person watches
// while pods come up one at a time, and a stream on a channel that can drop and reconnect needs
// its own rules about where a stream restarts and what a half-delivered line means. Trading that
// for uniformity of transport would be a bad deal, and it is a deal this shape makes unnecessary
// rather than one it makes easy to get wrong later.

// outcome is what a question produced: a status and a body, in whichever of the two shapes this
// module uses.
type outcome struct {
	status int
	body   any
}

// refusalBody is what this module says when it will not answer.
//
// The same shape as an HTTP error body, so that a caller reading a refusal over the channel and a
// caller reading one over HTTP read the same field for the same sentence.
func refusalBody(message string) any {
	return map[string]any{"error": map[string]any{"message": message}}
}

// writeOutcome puts an answer on the wire, over HTTP.
//
// The only place the two transports meet for a question that has been asked, and it is a plain
// rendering: the channel takes the body itself, because there is no status code on a message.
func writeOutcome(w http.ResponseWriter, said outcome) {
	writeJSON(w, said.status, said.body)
}

// queryFrom is a message's query, for the functions that take one.
//
// The channel has no URL, so the parameters a question carries come as an object rather than as
// a path. Spelled the same as an HTTP query on purpose: a reader comparing the two transports
// should see the same parameter names and not have to learn a second vocabulary.
func queryFrom(payload url.Values) url.Values { return payload }

// queryInt reads one number out of a question, falling back when it is not one.
func queryInt(query url.Values, name string, fallback int) int {
	return queryIntOf(query.Get(name), fallback)
}

// readBody is what a question carried, for the functions that take one.
//
// Read once here rather than in each question, and read without a limit beyond a generous one:
// a question that names a list of images grows with the page a person is looking at, and a limit
// that was right for one of them truncates the next silently.
func readBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxQuestionBody))
	return body
}

// maxQuestionBody is how much of a question this module reads.
//
// A megabyte: enough for a page of a few hundred images with their registry credentials, which is
// far past any page this module serves, and small enough that a runaway caller cannot make this
// process hold a gigabyte.
const maxQuestionBody = 1 << 20

// decodeBody reads a question's body into a shape.
//
// The same reading the HTTP path does, over bytes rather than a request, so a question that
// arrives on the channel is refused for exactly the reasons one that arrives over HTTP is —
// including a body that is not JSON at all, which is the refusal an author of a module needs to
// see when their own encoding is wrong.
func decodeBody(data []byte, into any) error {
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("the request is not valid JSON: %w", err)
	}
	return nil
}

// refusedOutcome is an answer that is a refusal, with a status for the HTTP path.
//
// There is no status on a channel message, so a refusal is recognised by its shape — an `error` with
// a `message` — rather than by a code. Both transports therefore agree on what a refusal *is*, and
// the status is kept only because the HTTP endpoint still has to answer with one.
func refusedOutcome(status int, message string) outcome {
	return outcome{status: status, body: refusalBody(message)}
}

// refusal is what an answer says when it is a refusal rather than an answer, or empty when it is a
// real answer.
//
// Read off the shape rather than off the status, because the status is for the HTTP path: what
// distinguishes a refusal is that it says why, and that is what a caller has to be shown.
func (o outcome) refusal() string {
	wrapper, ok := o.body.(map[string]any)
	if !ok {
		return ""
	}
	inner, ok := wrapper["error"].(map[string]any)
	if !ok {
		return ""
	}
	message, ok := inner["message"].(string)
	if !ok || message == "" {
		return ""
	}
	return message
}
