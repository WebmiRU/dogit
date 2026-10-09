package modulechan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The client, and what makes it stop.
//
// The property that matters here is the one that was wrong: a module that is asked to shut down
// must shut down. It is invisible in a test that never cancels its context, and it is the kind of
// bug that only shows up as "the suite takes four minutes and times out" in a package somebody
// else has spent the morning on.

// echoCore accepts a channel and never says anything, which is the situation a client has to be
// able to give up on.
func echoCore(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/module/channel") {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			// Deliberately no compression: the client asks for none, and negotiating one
			// would be a second thing to differ between the two ends.
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		// Held open until the test ends, which is the point: nothing sends anything and
		// nothing closes it, so the only thing that can end the client is its own context.
		<-r.Context().Done()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// The bug this is here for: Run returned only when the socket broke, so a cancelled context left
// it in a read nothing would ever end. A module asked to stop could not stop.
func TestACancelledContextEndsTheClient(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	client := &Client{URL: echoCore(t), Token: "a-token"}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Run(ctx)
	}()

	// Long enough to be attached and reading, and short enough that a test waiting on it is
	// not a slow test.
	select {
	case <-stopped:
		t.Fatal("Run returned before anything was cancelled")
	case <-time.After(200 * time.Millisecond):
	}

	cancel()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// And the same when the client is stopped while reconnecting, which is where a naive fix would
// still leak: the backoff sleep has to notice the cancellation too.
func TestACancelledContextEndsAClientThatIsReconnecting(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	// An address nothing is listening on, so every attempt fails and the client spends its
	// time in the backoff between them.
	client := &Client{
		URL:     "http://127.0.0.1:1",
		Token:   "a-token",
		Backoff: 50 * time.Millisecond,
		Log:     discardLog{},
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Run(ctx)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled while reconnecting")
	}
}

// A module whose channel has been stopped must not be holding a connection the core still counts.
func TestAStoppedClientLeavesNothingBehind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// A core that counts connections, so "nothing left behind" means something.
	var open int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
			OriginPatterns:     []string{"*"},
		})
		if err != nil {
			return
		}
		open++
		// Read until the client goes, which is how a close is noticed rather than guessed at.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				break
			}
		}
		open--
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer server.Close()

	client := &Client{URL: server.URL, Token: "a-token"}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Run(ctx)
	}()

	// Wait for it to be attached rather than sleeping: the property under test is about what
	// is left behind, which means there has to have been something.
	deadline := time.Now().Add(5 * time.Second)
	for open == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if open == 0 {
		t.Fatal("the client never opened a connection")
	}

	cancel()
	<-stopped

	// The core learns of a close by reading, on its own goroutine, so this waits rather than
	// asserting immediately.
	deadline = time.Now().Add(5 * time.Second)
	for open != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if open != 0 {
		t.Errorf("the core still counts %d connections after the client stopped", open)
	}
}
