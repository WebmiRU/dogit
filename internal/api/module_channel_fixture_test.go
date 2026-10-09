package api

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/store"
)

// A module that is asked over the channel, in tests that are not about the channel.
//
// The fake deploy module answers the questions the core now sends down a socket, and it has to be
// *attached* before the test asks anything: otherwise every such test fails on a 30-second timeout
// and looks like a broken question rather than a module that was not there yet. These are the
// helpers for that.

// mintBoundToken gives a module its own credential, bound to a module.
//
// Two calls because the store keeps them apart, and both halves matter: a token that is not bound
// authenticates nobody, and a bound token is what lets the core say which module a message came
// from on every message rather than once per connection — which is the property the channel exists
// to keep.
func mintBoundToken(t *testing.T, st *store.Store, name string, integrationID uuid.UUID) (raw string, hash []byte) {
	t.Helper()

	raw, hash, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("mint a token: %v", err)
	}
	token, err := st.ModuleTokens().Create(context.Background(), name, "", hash, nil)
	if err != nil {
		t.Fatalf("create a token: %v", err)
	}
	if err := st.ModuleTokens().Bind(context.Background(), token.ID, integrationID); err != nil {
		t.Fatalf("bind the token: %v", err)
	}
	return raw, hash
}

// waitUntilAttached says when a module's channel is open on the core.
//
// Asked of the core rather than slept for. A sleep is slow when it is unlucky and fast when it is
// lucky, and every test using this would then be either 30 seconds long or flaky, and both read as
// a problem with the thing under test rather than with the test.
func waitUntilAttached(t *testing.T, srv *Server, moduleID uuid.UUID) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if srv.moduleChannel().Connected(moduleID) != 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the module's channel did not open on the core")
}

// fakeQuestion is what a question on the channel carries, read the way a module reads it.
//
// Body as a raw message rather than as bytes: JSON's null and its objects are not valid base64, so
// a []byte here would fail to decode every question that carries a body — with the same error a
// module author would get for sending something malformed, which is a confusing way to be wrong.
type fakeQuestion struct {
	Query map[string]string `json:"query"`
	Body  json.RawMessage   `json:"body"`
}

// values is the question's parameters as the functions that take one expect them.
func (q fakeQuestion) values() url.Values {
	values := make(url.Values, len(q.Query))
	for name, value := range q.Query {
		values.Set(name, value)
	}
	return values
}
