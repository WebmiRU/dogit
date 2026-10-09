package api

import (
	"testing"

	"github.com/ewolf/dogit/internal/models"
)


// A run started by a push has nobody behind it, and saying so must not read a field out
// of nothing.
//
// This took the process down on the first real push: the author was correctly made nil
// because no person started it, and a line further down still read a name off it.
func TestARunNobodyStartedStillHasSomebodyToBlame(t *testing.T) {
	if got := startedBy(nil); got == "" {
		t.Fatal("a run with no author says nothing about who started it")
	}

	user := &models.User{Username: "alice"}
	if got := startedBy(user); got != "alice" {
		t.Fatalf("startedBy = %q, want alice", got)
	}
}
