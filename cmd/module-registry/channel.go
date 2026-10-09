package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ewolf/dogit/internal/modulechan"
)

// The channel this module keeps open to the core, and the commands that arrive on it.
//
// Data does not come this way and neither does the registry protocol: docker clients speak HTTP
// to this module and will keep doing so, and a client that had to hold a socket open to push a
// layer would be a worse registry. What comes this way is the core asking this module something
// about what it holds — which used to be an HTTP request to an endpoint this module also serves,
// and that endpoint stays, because a core older than this module's channel still asks that way.

// resolveCommand is what the core calls to be told what a tag currently points at.
//
// In the closed vocabulary of channel kinds rather than a URL path, because a kind is something
// a module can switch on exhaustively and a path is something it has to recognise by guessing.
const resolveCommand = "registry.resolve"

// newRegistry is this module's own half, made once.
//
// Built here rather than inside the proxy so that the channel and the HTTP endpoints are looking
// at the same one: a resolve answered over the socket and the same resolve answered over HTTP
// have to consult the same policy, and two objects would be two policies.
func newRegistry(core *coreClient, endpoint string) *registry {
	reg := &registry{
		core:      core,
		endpoint:  endpoint,
		imageName: defaultImageName,
	}
	// The realm is where a client is sent to get a token, and it has to be an address it can
	// fetch: a name that is not a URL makes every docker client fail with "unsupported protocol
	// scheme".
	reg.realm = strings.TrimRight(endpoint, "/") + tokenPath
	return reg
}

// channelLog is where this module says what happened on the channel.
//
// The module logs with log.Printf and has a stdlib logger already told what the prefix is, so
// the channel's two-method interface is met by printing rather than by carrying a second logging
// setup through a binary that has no use for one.
type channelLog struct{}

func (channelLog) Info(msg string, args ...any) {
	log.Print("module-registry: ", msg, " ", channelAttrs(args))
}

func (channelLog) Warn(msg string, args ...any) {
	log.Print("module-registry: ", msg, " ", channelAttrs(args))
}

// channelAttrs renders the key/value pairs the channel logs with, in the slog spelling it uses:
// the keys arrive as plain values rather than as slog.Attr, so they are printed as they come.
func channelAttrs(args []any) string {
	parts := make([]string, 0, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		parts = append(parts, fmt.Sprint(args[i]), "=", fmt.Sprint(args[i+1]))
	}
	return strings.Join(parts, " ")
}

// serve keeps the channel open until the context ends, and answers what arrives on it.
//
// Each command is answered on its own goroutine, so a question that takes as long as the registry
// takes to answer does not hold up the next one — and neither does the read loop, which belongs
// to the connection and not to any single command.
func serve(ctx context.Context, client *modulechan.Client, registry *registry) {
	done := &answered{}

	client.OnMessage = func(ctx context.Context, message modulechan.Message) {
		if message.Kind != resolveCommand {
			// Not an error: a core with more to say than this module knows about is still a
			// core to listen to.
			return
		}
		// A command with no id is a notification, and there is nobody to answer and no
		// question to refuse: a repeat is only recognisable by its id.
		if message.ID == "" {
			return
		}
		go resolveIt(ctx, client, registry, done, message)
	}

	client.Run(ctx)
}

// answer does one command and puts the answer on the wire.
//
// Every exit answers, including the failures. A command the core is waiting on has to be told
// something: silence and a refusal look the same to the caller, and one of them costs it a wait
// it cannot tell apart from this module having hung.
func resolveIt(ctx context.Context, client *modulechan.Client, registry *registry,
	done *answered, message modulechan.Message) {

	if had, repeated := done.remember(message.ID); repeated {
		// A repeat, not a new question. The core sends one when it did not hear back, and
		// it cannot help doing so: the connection can drop between the command going out
		// and the answer arriving, and then a core with no memory has no way to tell that
		// from a module that ignored it.
		//
		// Answered with what was worked out the first time rather than by asking the
		// registry again. The question is the same one, and a tag can be re-pushed in
		// between — so working it out twice would answer the same command two different
		// ways, and the core would take whichever it read first.
		say(ctx, client, message.ID, had.said, "")
		return
	}

	// Anything remembered this long could not still be on its way: the core holds a command
	// for its own command lifetime and stops sending it after that, and no lifetime an
	// administrator can type is a day.
	done.forget(rememberedFor)

	var asked struct {
		Project string `json:"project"`
		Image   string `json:"image"`
		Tag     string `json:"tag"`
		Token   string `json:"token"`
	}
	if err := message.PayloadInto(&asked); err != nil {
		say(ctx, client, message.ID, nil, "this is not a question I can read")
		return
	}

	found, refusal := registry.resolveTag(ctx, asked.Project, asked.Image, asked.Tag, asked.Token)
	if refusal != nil {
		say(ctx, client, message.ID, nil, refusal.reason)
		return
	}

	said, err := json.Marshal(found)
	if err != nil {
		say(ctx, client, message.ID, nil, "this answer cannot be written out")
		return
	}
	done.record(message.ID, said)
	say(ctx, client, message.ID, said, "")
}

// say puts an answer on the wire, or a refusal.
//
// The refusal is the shape this module already answers HTTP errors in, so a caller that knows
// how to read a refusal from this module does not have to learn a second one. A refusal is not
// remembered, and deliberately: a command refused because a registry was briefly away would
// then be refused the same way for the rest of the day, and the second attempt — which is what
// the core makes after a connection drops — is exactly the one that ought to try again.
func say(ctx context.Context, client *modulechan.Client, id string,
	said []byte, refusal string) {

	if refusal != "" {
		payload := map[string]any{"error": map[string]any{"message": refusal}}
		if err := client.Answer(ctx, id, payload); err != nil {
			log.Printf("module-registry: could not refuse %s: %v", resolveCommand, err)
		}
		return
	}
	if err := client.Answer(ctx, id, said); err != nil {
		log.Printf("module-registry: could not answer %s: %v", resolveCommand, err)
	}
}

// answered is what this module has already worked out, by the id of the command.
//
// Only answers that succeeded are kept. A refusal is worth repeating — "not allowed to pull
// this" is the same however many times it is asked — and is answered by refusing again, because
// the permission is asked of the core every time and costs nothing to ask twice.
type answered struct {
	mu    sync.Mutex
	items map[string]answer
}

type answer struct {
	said    []byte
	written time.Time
}

// remember files a command and says whether it has been answered before.
//
// Builds the map here as well as in record, so that whichever of the two a caller reaches first
// works: a map that only one of them creates is a map that panics for whoever went the other way.
func (a *answered) remember(id string) (answer, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.items == nil {
		a.items = map[string]answer{}
	}
	if had, found := a.items[id]; found {
		return had, true
	}
	// Recorded as unanswered and empty: two copies of the same command can be answered at the
	// same time on two goroutines, and the second must not work it out again while the first
	// is still asking the registry.
	a.items[id] = answer{}
	return answer{}, false
}

// record files what a command turned out to be worth.
func (a *answered) record(id string, said []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.items == nil {
		a.items = map[string]answer{}
	}
	a.items[id] = answer{said: said, written: time.Now()}
}

// forget drops what has been remembered for too long to still be asked about.
//
// When this module is asked something rather than on a timer: a goroutine kept alive only to
// delete a map is a goroutine that has to be reasoned about at shutdown, and this map is only
// written when a command arrives.
func (a *answered) forget(olderThan time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	for id, item := range a.items {
		if !item.written.IsZero() && item.written.Before(cutoff) {
			delete(a.items, id)
		}
	}
}

// rememberedFor is longer than any command lifetime an administrator can type here, so anything
// still in the map could still be asked about.
const rememberedFor = 24 * time.Hour
