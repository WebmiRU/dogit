package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/modulechan"
)

// The channel, between two processes that are not in the same one.
//
// Everything here is about what happens between two connections over time, and a recorder is the
// wrong tool for that: it can show what a handler wrote and cannot show what arrived, in what
// order, or whether the socket was still open when the message was sent. So these tests stand up
// a real server and dial it, which is the only place the properties below are visible at all.
//
// The two properties that matter most are the second and the fifth, and both are invisible to a
// test that checks the handler's output.

// standUp starts the API on a real port and returns its address.
//
// The path the client appends is written with the /api/v1 prefix the server is mounted under in
// production, so a test dialing Routes() directly has to drop it. That is the client knowing
// where it lives and the test not being mounted, and it is worth a line here because the two
// looking identical is how a test passes against a route that does not exist.
func standUp(t *testing.T, f *moduleFixture) string {
	t.Helper()

	server := httptest.NewServer(f.server.Routes())
	t.Cleanup(server.Close)
	return server.URL
}

// openChannel dials the core as a module with this token, and returns the connection.
func openChannel(t *testing.T, address, token string) *websocket.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, strings.Replace(address, "http://", "ws://", 1)+"/module/channel",
		&websocket.DialOptions{
			HTTPHeader:      map[string][]string{"Authorization": {"Bearer " + token}},
			CompressionMode: websocket.CompressionDisabled,
		})
	if err != nil {
		t.Fatalf("dial the channel: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func readOne(t *testing.T, conn *websocket.Conn) modulechan.Message {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read from the channel: %v", err)
	}
	message, err := modulechan.Decode(data)
	if err != nil {
		t.Fatalf("the core sent something that is not a message: %v", err)
	}
	return message
}

// send writes a message and, for the core's own frames, waits for the answer so that a test which
// expects a refusal sees it rather than racing the close.
func send(t *testing.T, conn *websocket.Conn, message modulechan.Message) modulechan.Message {
	t.Helper()

	frame, err := message.Envelope()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("write to the channel: %v", err)
	}
	return readOne(t, conn)
}

// write sends a message and reads nothing back.
//
// The other helper, send, reads one message because that is right when a test is only asking
// the core to authenticate a module. It is wrong once the module has commands waiting: attaching
// puts those out before the answer to the message that caused the attachment, so a test reading
// one message gets a command where it expected the answer.
func write(t *testing.T, conn *websocket.Conn, message modulechan.Message) {
	t.Helper()

	frame, err := message.Envelope()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("write to the channel: %v", err)
	}
}

// readCommand reads until a message that is not an answer arrives.
//
// Answers are skipped rather than treated as the wrong thing: the core sends one for every
// message that carried an id, and a test that has just attached a module has two streams
// arriving at once — the command it is here for, and the acknowledgement of its own hello.
func readCommand(t *testing.T, conn *websocket.Conn) modulechan.Message {
	t.Helper()

	for range 5 {
		if message := readOne(t, conn); message.Kind != modulechan.Answer {
			return message
		}
	}
	t.Fatal("the core sent five answers and no command")
	return modulechan.Message{}
}

// greet opens the channel the way a module does and waits until it is registered.
//
// Written as a message rather than a helper that answers, because the answer is what
// deliverToModule sends and a test that does not read it leaves it sitting on the socket where
// the next read finds it.
func greet(t *testing.T, f *moduleFixture, conn *websocket.Conn) {
	t.Helper()
	write(t, conn, modulechan.Message{ID: "m1", Kind: "hello", Token: f.moduleToken})
}

// A module's first message is authenticated, and so is every message after it.
func TestTheChannelTakesAModuleWithItsToken(t *testing.T) {
	f := newModuleFixture(t)
	conn := openChannel(t, standUp(t, f), f.moduleToken)

	answer := send(t, conn, modulechan.Message{
		ID:    "m1",
		Kind:  "runner.reporting",
		Token: f.moduleToken,
	})
	if answer.Kind != modulechan.Answer {
		t.Fatalf("the core answered %q to a message from a module holding a good token", answer.Kind)
	}
	if f.server.moduleChannel().Connected(f.module.ID) != 1 {
		t.Fatal("the core did not count the connection, so it has nobody to send anything to")
	}
}

// A token the core does not know is refused with a reason, and the connection ends.
//
// Both halves are the point. The reason is what the author of a module can act on — "authentication
// failed" is not, and the module cannot see the core's log. And the connection ends: a module
// that is left connected with a token the core will not accept is a module that believes it is
// talking to the core and is not.
func TestATokenTheCoreDoesNotKnowIsRefusedWithAReason(t *testing.T) {
	f := newModuleFixture(t)
	conn := openChannel(t, standUp(t, f), "glpat-this-was-never-issued")

	answer := send(t, conn, modulechan.Message{Kind: "anything", Token: "glpat-this-was-never-issued"})
	if answer.Kind != modulechan.Refused {
		t.Fatalf("the core answered %q to a token it does not know", answer.Kind)
	}

	var refusal modulechan.Refusal
	if err := answer.PayloadInto(&refusal); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(refusal.Reason) == "" {
		t.Fatal("the refusal has no reason, so a module author has nothing to act on")
	}

	// And the connection is finished, not merely quiet.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}

// The end date is enforced on a connection that has already been open.
//
// This is the property a connect-time check cannot have. The module connects with a good token,
// the date passes — here by rewriting it, because waiting five minutes is not a test — and the
// next message is refused. On HTTP the two are indistinguishable, because a request lives for
// milliseconds and there is no "later" to be wrong about. On a channel that lives for hours it is
// the whole question.
func TestATokenThatEndsWhileTheChannelIsOpenStopsWorking(t *testing.T) {
	f := newModuleFixture(t)
	ctx := context.Background()

	conn := openChannel(t, standUp(t, f), f.moduleToken)
	if answer := send(t, conn, modulechan.Message{ID: "m1", Kind: "hello", Token: f.moduleToken}); answer.Kind != modulechan.Answer {
		t.Fatalf("the first message was answered %q", answer.Kind)
	}

	ended := time.Now().Add(-time.Minute)
	if _, err := f.store.Pool().Exec(ctx,
		`UPDATE module_tokens SET expires_at = $2 WHERE token_hash = $1`,
		auth.HashToken(f.moduleToken), ended); err != nil {
		t.Fatalf("age the token: %v", err)
	}

	answer := send(t, conn, modulechan.Message{ID: "m2", Kind: "still here", Token: f.moduleToken})
	if answer.Kind != modulechan.Refused {
		t.Fatalf("a module kept talking on a token that had ended: the core answered %q", answer.Kind)
	}

	var refusal modulechan.Refusal
	if err := answer.PayloadInto(&refusal); err != nil {
		t.Fatal(err)
	}
	// The date is the only part of this a module author can act on: it is what tells them
	// what to write in the manifest.
	if !strings.Contains(refusal.Reason, ended.Format("2006")) {
		t.Fatalf("the refusal does not say when the token ended, so the author has a problem and "+
			"no date to go on: %q", refusal.Reason)
	}
	if refusal.EndedOn == "" {
		t.Fatal("the refusal carries no date field either")
	}
}

// A command goes to every connection of the module it is addressed to, and to none of another's.
//
// Not because it should — because the core has no way to choose, and pretending otherwise would
// be a promise it cannot keep. A module may run a second process that only gathers statistics,
// and whether that process acts on a deploy command is the author's business.
func TestACommandReachesEveryConnectionOfItsOwnModuleAndNoOther(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	first := openChannel(t, standUp(t, f), f.moduleToken)
	if answer := send(t, first, modulechan.Message{ID: "m1", Kind: "hello", Token: f.moduleToken}); answer.Kind != modulechan.Answer {
		t.Fatalf("the first connection was answered %q", answer.Kind)
	}

	// A second connection for the same module: the same token, a different socket.
	second := openChannel(t, standUp(t, f), f.moduleToken)
	if answer := send(t, second, modulechan.Message{ID: "m2", Kind: "hello", Token: f.moduleToken}); answer.Kind != modulechan.Answer {
		t.Fatalf("the second connection was answered %q", answer.Kind)
	}

	if got := channel.Connected(f.module.ID); got != 2 {
		t.Fatalf("the core counts %d connections, so a command would reach %d of 2", got, got)
	}

	delivered, err := channel.Announce(context.Background(), f.module.ID, Fact{
		Kind:    modulechan.WorkAvailable,
		Payload: map[string]any{"waiting": 1},
	})
	if err != nil {
		t.Fatalf("announce: %v", err)
	}
	if delivered != 2 {
		t.Fatalf("the command reached %d connections of 2", delivered)
	}
	for i, conn := range []*websocket.Conn{first, second} {
		if message := readOne(t, conn); message.Kind != modulechan.WorkAvailable {
			t.Fatalf("connection %d was sent %q", i+1, message.Kind)
		}
	}
}

// One connection may not present one token and then another.
//
// It means one token was presented as two modules, and the answer to that is neither of the
// modules involved. Without this, a connection could open as one module and be sent another's
// commands for as long as it liked.
func TestOneConnectionMayNotPresentTwoTokens(t *testing.T) {
	f := newModuleFixture(t)
	ctx := context.Background()

	other := registerRegistry(t, f.store, dbtest.Unique("registry"))
	otherToken := mintModuleToken(t, f.store, "someone else's", nil)
	if err := f.store.ModuleTokens().Bind(ctx, mustTokenID(t, f, "someone else's"), other.ID); err != nil {
		t.Fatalf("bind the second token: %v", err)
	}

	conn := openChannel(t, standUp(t, f), f.moduleToken)
	if answer := send(t, conn, modulechan.Message{ID: "m1", Kind: "hello", Token: f.moduleToken}); answer.Kind != modulechan.Answer {
		t.Fatalf("the first message was answered %q", answer.Kind)
	}

	answer := send(t, conn, modulechan.Message{ID: "m2", Kind: "hello again", Token: otherToken})
	if answer.Kind != modulechan.Refused {
		t.Fatalf("a connection presented a second token and the core answered %q", answer.Kind)
	}

	var refusal modulechan.Refusal
	_ = answer.PayloadInto(&refusal)
	if !strings.Contains(refusal.Reason, "another") {
		t.Fatalf("the refusal does not say what was wrong: %q", refusal.Reason)
	}
}

// A module that goes away is not counted afterwards, so the number the interface shows is about
// now rather than about everything that has ever connected.
func TestAClosedChannelIsNotCounted(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()
	conn := openChannel(t, standUp(t, f), f.moduleToken)
	if answer := send(t, conn, modulechan.Message{ID: "m1", Kind: "hello", Token: f.moduleToken}); answer.Kind != modulechan.Answer {
		t.Fatalf("the first message was answered %q", answer.Kind)
	}
	if got := channel.Connected(f.module.ID); got != 1 {
		t.Fatalf("the core counts %d connections, want 1", got)
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")

	// Waiting for the count rather than sleeping: the core learns of the close by reading
	// from the socket, which happens on its own goroutine, and a test that sleeps here is a
	// test that is slow when it is unlucky and fast when it is lucky.
	deadline := time.Now().Add(5 * time.Second)
	for channel.Connected(f.module.ID) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the core is still counting a connection that has been closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// mustTokenID finds a token by name, for a test that minted it.
func mustTokenID(t *testing.T, f *moduleFixture, name string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := f.store.Pool().QueryRow(context.Background(),
		`SELECT id FROM module_tokens WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("find the token %q: %v", name, err)
	}
	return id
}
