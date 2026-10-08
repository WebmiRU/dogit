package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/ewolf/dogit/internal/modulechan"
)

// The channel this module keeps open to the core, and the questions that arrive on it.
//
// # What does not come this way, and why
//
// A deployment and a revert narrate themselves line by line, for as long as a rollout takes, and
// that is a stream rather than an answer. Those two stay on HTTP, and the reason is worth having
// in writing rather than as an omission: the live log of a deploy is what a person watches while
// pods come up one at a time, and a stream on a connection that can drop and reconnect needs its
// own rules about where a stream resumes and what a half-delivered line means. Uniformity of
// transport is worth less than that log staying live, so the two that stream stay put and the
// five questions below come here.
//
// # Nothing is remembered here, and that is a decision
//
// The core holds a question and sends it again when it did not hear back — the connection can drop
// between the question going out and the answer arriving, and a core with no memory has no way to
// tell that from a module that ignored it. A module that remembered its answers could answer a
// repeat from the first answer, and the registry does that because re-resolving a tag can produce
// a different digest and the core would take whichever it read first.
//
// Here, every question is a read of a cluster or of this module's own record, and answering a
// repeat by asking again is both cheaper than the bookkeeping and more correct: what is running
// in a cluster changes, and a page asking again is asking because it wants to know now rather than
// then. An answer to a second ask from the first ask is the one thing that must not happen here.
//
// A module reading this should not read the list below as the whole of what may be asked. The core
// can send a kind this module does not know, and the answer is to ignore it rather than to treat
// it as a deploy.

// serve keeps the channel open until the context ends, and answers what arrives on it.
//
// Each question is answered on its own goroutine: a question that asks a cluster how long a rollout
// has been taking must not hold up the one after it, and neither must the read loop, which belongs
// to the connection rather than to any single question.
func serve(ctx context.Context, client *modulechan.Client, core *coreClient) {
	client.OnMessage = func(ctx context.Context, message modulechan.Message) {
		// A notification: there is nobody to answer and no question to refuse. An id is the
		// only thing that makes a repeat recognisable, and it is the only thing here that
		// needs one.
		if message.ID == "" {
			return
		}
		// An unknown kind is somebody else's business, on a version of the core that has
		// heard of more than this one.
		if !answerable(message.Kind) {
			return
		}
		go answerQuestion(ctx, client, core, message)
	}

	client.Run(ctx)
}

// answerable says whether this module answers a kind on the channel.
func answerable(kind string) bool {
	switch kind {
	case modulechan.DeployImages, modulechan.DeployCurrent, modulechan.DeployDeployments,
		modulechan.DeployImagesAvailability, modulechan.DeployTestCluster:
		return true
	}
	return false
}

// answerQuestion does one question and puts the answer on the wire.
//
// Every exit answers, including the failures. A question the core is waiting on has to be told
// something: silence and a refusal look the same to the caller, and one of them costs it a wait it
// cannot tell apart from this module having hung.
func answerQuestion(ctx context.Context, client *modulechan.Client, core *coreClient,
	message modulechan.Message) {

	var asked question
	if err := message.PayloadInto(&asked); err != nil {
		refuse(ctx, client, message.ID, "this is not a question I can read")
		return
	}

	say(ctx, client, message.ID, core.answerChannelQuestion(ctx, message.Kind, asked))
}

// question is what a question carries: the parameters and, for the two that take one, a body.
//
// Query as a flat object of strings rather than as a URL's one-to-many shape, because the URL shape
// is an artefact of the HTTP path and this module has no URLs to keep compatible with on the other
// side. The parameter names are the same either way, which is what a reader comparing the two
// transports actually needs.
type question struct {
	Query map[string]string `json:"query,omitempty"`
	Body  json.RawMessage   `json:"body,omitempty"`
}

// values is the question's parameters as the functions that take one expect them.
func (q question) values() url.Values {
	values := make(url.Values, len(q.Query))
	for name, value := range q.Query {
		values.Set(name, value)
	}
	return values
}

// answerChannelQuestion is the one place both transports meet.
//
// The five questions, asked with what came on the channel rather than with what came in a request.
// Everything a question does lives in the functions it calls, which the HTTP handlers call too — a
// question that can be answered two ways is a question that will eventually be answered two
// different ways.
func (c *coreClient) answerChannelQuestion(ctx context.Context, kind string, asked question) outcome {
	query := asked.values()
	switch kind {
	case modulechan.DeployImages:
		return c.answerImages(ctx, query)
	case modulechan.DeployCurrent:
		return c.answerCurrent(ctx, query)
	case modulechan.DeployDeployments:
		return c.answerDeployments(ctx, query)
	case modulechan.DeployImagesAvailability:
		return c.answerImagesAvailability(ctx, query, asked.Body)
	case modulechan.DeployTestCluster:
		return c.answerTestCluster(ctx, query, asked.Body)
	}
	return refusedOutcome(503, "this module is not asked that on the channel")
}

// say puts an answer on the wire, or a refusal.
//
// The refusal is the shape this module already uses for its own errors, so a caller that knows how
// to read one over HTTP does not have to learn a second.
func say(ctx context.Context, client *modulechan.Client, id string, said outcome) {
	if message := said.refusal(); message != "" {
		refuse(ctx, client, id, message)
		return
	}
	if err := client.Answer(ctx, id, said.body); err != nil {
		log.Printf("module-deploy: could not answer: %v", err)
	}
}

// refuse says no, in the shape the caller reads.
func refuse(ctx context.Context, client *modulechan.Client, id, reason string) {
	payload := map[string]any{"error": map[string]any{"message": reason}}
	if err := client.Answer(ctx, id, payload); err != nil {
		log.Printf("module-deploy: could not refuse: %v", err)
	}
}

// channelLog is where this module says what happened on the channel.
//
// The module logs with log.Printf and has a stdlib logger already told what the prefix is, so the
// channel's two-method interface is met by printing rather than by carrying a second logging setup
// through a binary that has no use for one.
type channelLog struct{}

func (channelLog) Info(msg string, args ...any) {
	log.Print("module-deploy: ", msg, " ", channelAttrs(args))
}

func (channelLog) Warn(msg string, args ...any) {
	log.Print("module-deploy: ", msg, " ", channelAttrs(args))
}

// channelAttrs renders the key/value pairs the channel logs with, in the slog spelling it uses: the
// keys arrive as plain values rather than as slog.Attr, so they are printed as they come.
func channelAttrs(args []any) string {
	parts := make([]string, 0, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		parts = append(parts, fmt.Sprint(args[i]), "=", fmt.Sprint(args[i+1]))
	}
	return strings.Join(parts, " ")
}
