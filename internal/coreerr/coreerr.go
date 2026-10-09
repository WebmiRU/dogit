// Package coreerr turns a refusal from the core into an error a module can log and an operator can
// read.
//
// It exists because three modules each had their own version of this and two of them had it wrong.
// The wrong version reads the status line and throws the rest of the response away, so a module
// that cannot start prints "core said 400" — a fact about the server rather than about the
// problem. That is what a module on the stand did for half an hour while the reason sat in the
// body of a response nobody was reading, and finding it meant searching the core for messages that
// came out at the wrong length.
//
// One copy, so the next module to be written reads the body rather than re-deciding not to.
package coreerr

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Refusal reads what the core said about a request it refused, and returns it as an error.
//
// The body is read here and nowhere else, because a caller that has already drained it cannot
// get it back and would have to settle for the status code — which is how this went wrong in the
// first place. The reader is capped so that a core which answers with something enormous, or a
// proxy which answers with a page, cannot fill a module's memory to explain a refusal.
func Refusal(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))

	var answer struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &answer); err == nil && answer.Error.Message != "" {
		return fmt.Errorf("the core refused (%d): %s", response.StatusCode, answer.Error.Message)
	}
	// A body this code does not recognise is still more than a number, and is passed through
	// rather than dropped.
	if text := strings.TrimSpace(string(body)); text != "" {
		return fmt.Errorf("the core refused (%d): %s", response.StatusCode, text)
	}
	return fmt.Errorf("the core refused with %d and said nothing", response.StatusCode)
}
