package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulechan"
)

// The channel a module keeps open to the core.
//
// One connection per module process, opened by the module, because an outgoing connection passes
// NAT and a module behind a firewall cannot be reached the other way round. It carries commands
// down and answers up, and it is the only channel commands travel on: two channels would mean two
// places where the truth about what a module was told could differ.
//
// # A connection is not an identity
//
// The token arrives in every message and is checked in every message, so nothing about "which
// module is this" is decided once at connect and kept. That is not a simplification — it is the
// property that makes the channel recoverable. A connection can die at any moment and a new one
// is complete, with nothing to resume and nothing that has to be restored. It also means the
// date on a token is enforced on a channel that has been open for hours, which is exactly where
// a connect-time check would have gone stale and let a module keep working on a credential that
// ended.
//
// # More than one connection per module is allowed
//
// A token is a set of clients, not a single caller. A command goes to all of them and the core
// does not choose which one acts and does not promise that exactly one will. That is the price of
// not having an opinion about what a module is, and it is deliberate: a module may run a second
// process that only gathers statistics, and whether that process acts on a deploy command is a
// question for the author of the module rather than for the core.
//
// Two answers to the same command from one module's connections is therefore possible and is not
// an error the core tries to resolve.

// channelClient is one open connection.
type channelClient struct {
	moduleID uuid.UUID
	name     string

	mu       sync.Mutex
	conn     *websocket.Conn
	openedAt time.Time
	// gone is set once, so a write that fails does not each of them log their own opinion.
	gone bool
}

func (c *channelClient) send(ctx context.Context, message modulechan.Message) error {
	frame, err := message.Envelope()
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone || c.conn == nil {
		return modulechan.ErrNotConnected
	}

	if err := c.conn.Write(ctx, websocket.MessageText, frame); err != nil {
		c.gone = true
		return fmt.Errorf("send a %q message: %w", message.Kind, err)
	}
	return nil
}

func (c *channelClient) close(status websocket.StatusCode, reason string) {
	c.mu.Lock()
	c.gone = true
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close(status, reason)
	}
}

// ModuleChannel is the core's half of the channel: who is connected, and how a message reaches
// them.
type ModuleChannel struct {
	s *Server

	mu      sync.Mutex
	clients map[uuid.UUID]map[*channelClient]struct{}
}

// moduleChannel returns the core's channel.
//
// One per server, held on it, because the clients are the point: a second channel would be a
// second set of them, and a message sent down one of them would arrive at a module that another
// set had already written off as gone.
func (s *Server) moduleChannel() *ModuleChannel {
	s.channelOnce.Do(func() { s.channel = &ModuleChannel{s: s, clients: map[uuid.UUID]map[*channelClient]struct{}{}} })
	return s.channel
}

// Connected is how many connections a module has open right now.
//
// Not a decision and not a lock: it is what the interface shows so that an operator looking at a
// module which is not answering can tell the two apart — a module that is not connected and a
// module that is connected and has nothing to say are different problems, and they look the same
// from the outside.
func (ch *ModuleChannel) Connected(moduleID uuid.UUID) int {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return len(ch.clients[moduleID])
}

// Send delivers a message to every connection of one module, and says how many took it.
//
// Zero delivered is not a failure here: it is the case the command cache exists for, and the
// caller decides what an undelivered command means. A message that could not be written to one
// connection but reached another is logged and not returned — refusing the whole send because one
// of several connections had gone would mean a module's second process going quietly silent
// stops its first from being commanded.
func (ch *ModuleChannel) Send(ctx context.Context, moduleID uuid.UUID,
	kind string, payload any) (int, error) {

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode a %q message: %w", kind, err)
	}

	ch.mu.Lock()
	targets := make([]*channelClient, 0, len(ch.clients[moduleID]))
	for client := range ch.clients[moduleID] {
		targets = append(targets, client)
	}
	ch.mu.Unlock()

	delivered := 0
	for _, client := range targets {
		message := modulechan.Message{Kind: kind, Payload: body}
		if err := client.send(ctx, message); err != nil {
			ch.s.log.Warn("a module's channel would not take a message",
				"module", client.name, "kind", kind, "error", err)
			continue
		}
		delivered++
	}
	return delivered, nil
}

// announceWork tells the runners that there is something to take.
//
// A fact, not a command, and that is what makes it free of the machinery the other messages
// need: nothing is cached for it and nothing waits on it, because it cannot be stale. A runner
// that hears this in four minutes calls claim and either takes the job or is told the queue is
// empty, which is the answer it would have got by polling anyway.
//
// Which means a missed announcement costs a runner its usual polling interval and nothing else.
// That is why this is a plain call after the commit rather than something hooked into the store
// transaction: a failure to announce is a failure to be quick, and it must not be able to fail a
// pipeline that has already been created.
func (s *Server) announceWork(ctx context.Context) {
	installed, err := s.store.Integrations().List(ctx)
	if err != nil {
		s.log.Warn("could not tell the runners that there is work", "error", err)
		return
	}

	waiting := s.pendingJobCount(ctx)
	for _, module := range installed {
		if !module.Enabled || !strings.HasPrefix(module.Kind, "runner:") {
			continue
		}
		delivered, err := s.moduleChannel().Send(ctx, module.ID, modulechan.WorkAvailable,
			map[string]any{"waiting": waiting})
		if err != nil {
			s.log.Warn("could not send work to a runner", "module", module.Name, "error", err)
			continue
		}
		s.log.Debug("work announced", "module", module.Name, "waiting", waiting,
			"connections", delivered)
	}
}

// handleModuleChannel is the upgrade, and then nothing but a read loop.
//
// Every message is authenticated on its own. The upgrade carries a bearer header so that the
// handshake itself is a real request rather than an anonymous one that becomes trusted a moment
// later — and then the token in each message is checked anyway, because a connection is not an
// identity and a header said once at the start of a connection that may live for days is not
// enough.
func (s *Server) handleModuleChannel(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Modules are services, not browsers: they do not send an Origin, and there is no
		// cookie for this route to be refused by.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		s.log.Debug("a module channel was refused at the upgrade", "error", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Detached from the request on purpose. A request context ends when the handler
	// returns, and this handler does not return until the module goes away — which would
	// close the connection the instant it opened.
	ctx := context.WithoutCancel(r.Context())
	ch := s.moduleChannel()

	var client *channelClient
	defer func() {
		if client != nil {
			ch.detach(client)
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if client != nil && !isQuietClose(err) {
				s.log.Info("a module channel closed", "module", client.name,
					"for", time.Since(client.since()).Round(time.Second), "error", err)
			}
			return
		}

		message, err := modulechan.Decode(data)
		if err != nil {
			s.refuseAndClose(ctx, conn, "", modulechan.Refusal{Reason: err.Error()})
			return
		}

		module, err := s.moduleFromToken(ctx, message.Token)
		if err != nil {
			// The reason is sent before the connection goes. A module that is cut off
			// with no word reconnects, is refused again, and never learns anything — and the
			// author of the module has no way to tell that from a network fault.
			s.refuseAndClose(ctx, conn, message.ID, moduleRefusal(err))
			return
		}

		if client == nil {
			client = ch.attach(ctx, conn, module)
			s.log.Info("a module opened its channel", "module", client.name,
				"kind", module.Kind, "endpoint", module.Endpoint)
		}
		// A connection that changes which module it is mid-stream is refused rather than
		// reassigned. It means one token was presented as two, and the answer to that is
		// neither of the modules involved.
		if client.moduleID != module.ID {
			s.refuseAndClose(ctx, conn, message.ID, modulechan.Refusal{
				Reason: "this connection presented one token and then another"})
			return
		}

		s.deliverToModule(ctx, client, message)
	}
}

// deliverToModule handles a message a connected module sent.
//
// Empty for now, and honest about being so: the first thing to travel down this channel is a
// notification, and the first thing to travel up is an answer to a message the core sent. Until
// something is asked of a module there is nothing to do with what it says, and inventing a reply
// to keep the loop busy would be a protocol with a message in it that means nothing.
func (s *Server) deliverToModule(ctx context.Context, client *channelClient, message modulechan.Message) {
	// Answered only when the sender asked by putting an id on it.
	//
	// A message without one is a notification — "there is work", "I am still here" — and a
	// notification that is answered turns a channel into a request-reply loop that both ends
	// have to implement for no gain. The id is the request for an answer, and its absence is
	// the request not to make one.
	//
	// Unknown kinds are somebody else's business on a version of the core that has heard of
	// more than this one, so they are accepted rather than refused. What must not happen is
	// silence: a module that sent something with an id and got nothing back cannot tell a core
	// that ignored it from a core that is broken, and it will wait.
	if message.ID == "" {
		return
	}
	if err := client.send(ctx, modulechan.Message{
		ID:      message.ID,
		Kind:    modulechan.Answer,
		Payload: mustMarshal(map[string]any{"accepted": true}),
	}); err != nil {
		s.log.Warn("a module's channel would not take an answer",
			"module", client.name, "kind", message.Kind, "error", err)
	}
}

// refusalEndedOn is the detail an expired token's refusal carries, and the field of the same name
// on the wire.
const refusalEndedOn = "ended_on"

// moduleRefusal turns a refusal into something a module can be told.
//
// Both halves of an apiError are used: the sentence is the reason, and the detail is the date a
// sentence cannot be parsed for. Anything that is not an apiError has no reason the core wrote
// down, so its own text is used — better a clumsy sentence than a refusal that names nothing.
func moduleRefusal(err error) modulechan.Refusal {
	var known *apiError
	if !errors.As(err, &known) {
		return modulechan.Refusal{Reason: err.Error()}
	}

	refusal := modulechan.Refusal{Reason: known.message}
	if refusal.Reason == "" {
		refusal.Reason = modulechan.ReasonUnknownToken
	}
	refusal.EndedOn = known.details[refusalEndedOn]
	return refusal
}

// refuseAndClose says why, then ends the connection.
//
// Saying it is best-effort: a connection that has already gone cannot be told anything, and that
// is not a second failure worth logging on top of the first. The reason is sent before the close
// rather than in the close frame's text because a close reason is limited to a hundred and twenty
// three bytes and these sentences are longer than that.
func (s *Server) refuseAndClose(ctx context.Context, conn *websocket.Conn,
	id string, refusal modulechan.Refusal) {

	payload, err := json.Marshal(refusal)
	if err != nil {
		payload = []byte(`{"reason":"the core could not say why"}`)
	}
	frame, err := modulechan.Message{ID: id, Kind: modulechan.Refused, Payload: payload}.Envelope()
	if err == nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = conn.Write(writeCtx, websocket.MessageText, frame)
		cancel()
	}
	_ = conn.Close(websocket.StatusPolicyViolation, refusal.Reason)
}

// attach registers a connection under its module and returns the client that stands for it.
func (ch *ModuleChannel) attach(ctx context.Context, conn *websocket.Conn,
	module *models.Integration) *channelClient {

	client := &channelClient{
		moduleID: module.ID,
		name:     module.Kind + "/" + module.Name,
		conn:     conn,
		openedAt: time.Now(),
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.clients[module.ID] == nil {
		ch.clients[module.ID] = map[*channelClient]struct{}{}
	}
	ch.clients[module.ID][client] = struct{}{}
	return client
}

func (ch *ModuleChannel) detach(client *channelClient) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	open, ok := ch.clients[client.moduleID]
	if !ok {
		return
	}
	delete(open, client)
	if len(open) == 0 {
		delete(ch.clients, client.moduleID)
	}
}

func (c *channelClient) since() time.Time { return c.openedAt }

// isQuietClose says whether an error is a close that was meant.
//
// A socket closed with 1000 or 1001 is one end deciding, and a decision with no reason given is
// the hardest kind of fault to argue with later. Anything else is worth a line.
func isQuietClose(err error) bool {
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway
}

func mustMarshal(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return encoded
}
