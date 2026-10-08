package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulechan"
)

// The command cache, from the module's side of the socket.
//
// The properties here cannot be seen from a handler's output: what matters is that a command
// sent to a module which was not there arrives when it comes, that a fact does not, and that a
// command stops arriving once its time is up. So these dial a real server the same way the
// tests in channel_test.go do.
//
// Reading is by readCommand rather than readOne throughout, because a module that attaches
// receives two streams at once: the commands it had missed, and the answer to the message that
// caused the attachment. A test that reads one frame and expects a command will sometimes get
// the answer, and will then look like a cache that lost a command.

// comeOnline opens the channel as the fixture's module and returns the connection.
func comeOnline(t *testing.T, f *moduleFixture) *websocket.Conn {
	t.Helper()

	conn := openChannel(t, standUp(t, f), f.moduleToken)
	greet(t, f, conn)
	return conn
}

func TestACommandSentWhileAModuleWasAwayIsGivenToItWhenItComes(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	// Nothing is connected, which is the case the cache exists for.
	if delivered, err := channel.Decide(context.Background(), f.module, Decision{
		Kind:    "deploy.requested",
		Payload: map[string]any{"cluster": "production"},
	}); err != nil || delivered != 0 {
		t.Fatalf("decide with nobody listening: delivered %d, err %v", delivered, err)
	}
	if got := channel.PendingCommands(f.module.ID); got != 1 {
		t.Fatalf("the core is holding %d commands, want 1", got)
	}

	command := readCommand(t, comeOnline(t, f))
	if command.Kind != "deploy.requested" {
		t.Fatalf("the module was sent %q, want the command it had missed", command.Kind)
	}
	if command.ID == "" {
		t.Error("the command arrived with no id, so the module cannot tell it from a new one")
	}

	var payload struct {
		Cluster string `json:"cluster"`
	}
	if err := command.PayloadInto(&payload); err != nil {
		t.Fatalf("the command's payload: %v", err)
	}
	if payload.Cluster != "production" {
		t.Errorf("the command names the cluster %q, want production", payload.Cluster)
	}
}

// A fact is not a command, and the difference is not a matter of taste: a module that was
// asleep does not need to be told afterwards that work existed.
func TestAFactIsNotHeldForAModuleThatMissedIt(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	if _, err := channel.Announce(context.Background(), f.module.ID, Fact{
		Kind:    modulechan.WorkAvailable,
		Payload: map[string]any{"waiting": 3},
	}); err != nil {
		t.Fatalf("announce: %v", err)
	}

	if got := channel.PendingCommands(f.module.ID); got != 0 {
		t.Fatalf("a fact was cached: %d commands waiting", got)
	}

	conn := comeOnline(t, f)
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// The core does not decide a command is stale. It keeps one for its time and then forgets it,
// whatever the module said — because the only thing that knows whether a deploy still means
// anything is the module that was asked to do it.
func TestACommandIsGivenAgainEvenAfterTheModuleHasAnswered(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	if _, err := channel.Decide(context.Background(), f.module, Decision{
		Kind: "deploy.requested", Payload: map[string]any{"cluster": "staging"},
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}

	// First time there: the module takes the command and answers it, which is what a
	// well-behaved module does with anything it has acted on.
	first := comeOnline(t, f)
	answered := readCommand(t, first)
	if answered.Kind != "deploy.requested" {
		t.Fatalf("the module was sent %q", answered.Kind)
	}
	write(t, first, modulechan.Message{ID: answered.ID, Kind: modulechan.Answer,
		Token: f.moduleToken})
	_ = first.Close(websocket.StatusNormalClosure, "")

	// Second time there: the same command, because nothing told the core it was finished.
	again := readCommand(t, comeOnline(t, f))
	if again.Kind != "deploy.requested" {
		t.Fatalf("on reconnect the module was sent %q, want the command again", again.Kind)
	}
	if again.ID != answered.ID {
		t.Errorf("the repeat carries the id %q, want the original %q — a repeat that looks like a new command is one",
			again.ID, answered.ID)
	}
}

// Zero is a value and not an absence: it is how an administrator says "do not hold commands for
// this module".
func TestACommandLifetimeOfZeroMeansNothingIsHeld(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()
	giveLifetime(t, f, 0)

	if _, err := channel.Decide(context.Background(), f.module, Decision{
		Kind: "deploy.requested", Payload: map[string]any{"cluster": "production"},
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got := channel.PendingCommands(f.module.ID); got != 0 {
		t.Fatalf("with a lifetime of zero the core is holding %d commands", got)
	}

	conn := comeOnline(t, f)
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// A field nobody filled in is not a request for a different kind of channel. It means five
// minutes, for a module registered before the field existed and for one whose field was cleared.
func TestAnAbsentCommandLifetimeMeansFiveMinutes(t *testing.T) {
	cases := map[string]struct {
		raw  json.RawMessage
		want time.Duration
	}{
		"nothing at all":  {nil, defaultCommandTTL},
		"an empty value":  {json.RawMessage(`""`), defaultCommandTTL},
		"explicit null":   {json.RawMessage(`null`), defaultCommandTTL},
		"a number":        {json.RawMessage(`60`), 60 * time.Second},
		"zero":            {json.RawMessage(`0`), 0},
		"a negative one":  {json.RawMessage(`-5`), 0},
		"nonsense":        {json.RawMessage(`"soon"`), defaultCommandTTL},
		"a bare fraction": {json.RawMessage(`1.5`), defaultCommandTTL},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := commandLifetimeOf(c.raw); got != c.want {
				t.Errorf("commandLifetimeOf(%s) = %s, want %s", c.raw, got, c.want)
			}
		})
	}
}

// The lifetime the module configured is the one used, which is the whole of what a setting is
// for. Checked through the store rather than against a value in the test, because the two
// would otherwise be the same number written down twice.
func TestTheLifetimeComesFromWhatTheModuleConfigured(t *testing.T) {
	f := newModuleFixture(t)
	giveLifetime(t, f, 42)

	if got := f.server.commandLifetime(context.Background(), f.module); got != 42*time.Second {
		t.Errorf("commandLifetime = %s, want 42s", got)
	}
}

// The commands a module is owed are in the order they were made. A deploy module handed two at
// once has to be able to see which came first.
func TestCommandsAreGivenBackInTheOrderTheyWereMade(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()
	giveLifetime(t, f, 300)

	order := []string{"first", "second", "third"}
	for _, cluster := range order {
		if _, err := channel.Decide(context.Background(), f.module, Decision{
			Kind: "deploy.requested", Payload: map[string]any{"cluster": cluster},
		}); err != nil {
			t.Fatalf("decide %s: %v", cluster, err)
		}
	}

	conn := comeOnline(t, f)
	for _, want := range order {
		command := readCommand(t, conn)
		var payload struct {
			Cluster string `json:"cluster"`
		}
		if err := command.PayloadInto(&payload); err != nil {
			t.Fatalf("the command's payload: %v", err)
		}
		if payload.Cluster != want {
			t.Fatalf("the module was given %q where %q belonged", payload.Cluster, want)
		}
	}
}

// Two commands are two commands and not one: the id is what makes them tellable apart, and a
// repeat of one does not stand in for the other.
func TestEachCommandCarriesItsOwnID(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	for range 3 {
		if _, err := channel.Decide(context.Background(), f.module, Decision{
			Kind: "deploy.requested", Payload: map[string]any{"cluster": "production"},
		}); err != nil {
			t.Fatalf("decide: %v", err)
		}
	}

	seen := map[string]bool{}
	conn := comeOnline(t, f)
	for range 3 {
		command := readCommand(t, conn)
		if command.ID == "" {
			t.Fatal("a command arrived with no id")
		}
		if seen[command.ID] {
			t.Fatalf("the id %q was used twice", command.ID)
		}
		seen[command.ID] = true
	}
}

// A command whose time is up is not given to a module that reconnects afterwards, however long
// ago it was made.
func TestACommandThatHasRunOutIsNotGivenOut(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	// Filed by hand rather than through the settings, because a test that waits five minutes
	// to find out what a five-minute setting does is a test nobody runs.
	channel.keep(f.module.ID, pendingCommand{
		message:   modulechan.Message{ID: uuid.NewString(), Kind: "deploy.requested"},
		expiresAt: time.Now().Add(-time.Second),
	})
	channel.keep(f.module.ID, pendingCommand{
		message:   modulechan.Message{ID: uuid.NewString(), Kind: "deploy.requested"},
		expiresAt: time.Now().Add(time.Minute),
	})

	if got := len(channel.takePending(f.module.ID)); got != 1 {
		t.Fatalf("the core would give back %d commands, want the one that is still live", got)
	}
	if got := channel.PendingCommands(f.module.ID); got != 1 {
		t.Fatalf("after reading them out the core holds %d, want 1", got)
	}
}

// The module page has to show both numbers, because the argument the cache exists for is an
// operator looking at a module that says it was never told. Without them that argument has no
// answer anywhere on the page.
func TestTheModulePageShowsWhatTheChannelIsHolding(t *testing.T) {
	f := newModuleFixture(t)
	channel := f.server.moduleChannel()

	if _, err := channel.Decide(context.Background(), f.module, Decision{
		Kind: "deploy.requested", Payload: map[string]any{"cluster": "production"},
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}

	// Without the /api/v1 prefix the server is mounted under in production: the fixture hands
	// Routes() straight to a recorder, so the path is the one Routes() knows.
	page := f.asAdmin(t, http.MethodGet, "/modules/"+f.module.ID.String(), "")
	if page.Code != http.StatusOK {
		t.Fatalf("the module page: status %d, body %s", page.Code, page.Body.String())
	}

	var shown struct {
		Module struct {
			Connections int `json:"channel_connections"`
			Pending     int `json:"channel_pending_commands"`
		} `json:"module"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &shown); err != nil {
		t.Fatalf("read the module page: %v", err)
	}
	if shown.Module.Connections != 0 {
		t.Errorf("the page reports %d connections, want none — nothing has dialed",
			shown.Module.Connections)
	}
	if shown.Module.Pending != 1 {
		t.Errorf("the page reports %d commands waiting, want the one the core is holding",
			shown.Module.Pending)
	}
}

// giveLifetime writes the command lifetime setting for the fixture's module.
//
// Straight through the store rather than through the settings endpoint, so the test is about the
// cache and not about the form that sets it — that form has its own tests.
func giveLifetime(t *testing.T, f *moduleFixture, seconds int) {
	t.Helper()

	value, err := json.Marshal(seconds)
	if err != nil {
		t.Fatalf("encode the lifetime: %v", err)
	}
	if err := f.store.Integrations().SetSetting(context.Background(), f.module.ID,
		models.ScopeInstance, nil, modulechan.CommandTTLSetting, value); err != nil {
		t.Fatalf("set the command lifetime: %v", err)
	}
}
