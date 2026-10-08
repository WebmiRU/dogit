package modulechan

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Log is where the client says what happened to its connection.
//
// Two methods, because two things happen and the difference matters: the channel opening is
// routine and the channel ending is not.
type Log interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

type discardLog struct{}

func (discardLog) Info(string, ...any) {}
func (discardLog) Warn(string, ...any) {}

// Client is a module's end of the channel.
//
// Deliberately not a type anybody has to hold: a module starts it and it runs until the process
// stops. Everything the module does on the channel is "tell the core something" and "react to
// what the core says", and neither of those should need error handling at three in the morning —
// so a channel that is down is a channel that comes back, and a message that could not be sent is
// logged and counted rather than returned to a caller that cannot do anything about it.
//
// The one thing it does not do is reconnect *to something*. There is nothing to resume: the
// core keeps no per-connection state, so a fresh connection is a complete one.
type Client struct {
	// URL is where the core is. An http or https address; wss is used for https.
	URL string

	// Token is this module's credential. Sent in every message rather than in a handshake,
	// which is why the connection carries no state worth restoring.
	Token string

	// OnMessage is called for each message from the core, on its own goroutine. It is not
	// called for messages this module cannot parse: those end the connection, because a
	// frame that is not a message means the two ends disagree and continuing would mean
	// guessing which is wrong.
	OnMessage func(context.Context, Message)

	// Log says what happened. Optional; nil discards.
	//
	// An interface rather than a logger, because this half of the channel runs inside somebody
	// else's program and that program has its own logging — the core has slog, this module
	// has log.Printf, and neither should be asked to change for the other. *slog.Logger
	// satisfies it as it stands.
	Log Log

	// Backoff is the wait between connection attempts, doubled each time up to MaxBackoff.
	// The default is a second, which is short enough that a restarted module is working
	// before anybody has looked, and long enough that a core which is down does not get
	// hammered by every module on the instance at once.
	Backoff    time.Duration
	MaxBackoff time.Duration

	conn   *websocket.Conn
	mu     sync.Mutex
	sent   int
	errors int
}

// ErrNotConnected is what Notify reports when the channel is down.
var ErrNotConnected = errors.New("the channel to the core is not open")

// Notify sends a message that expects no answer.
func (c *Client) Notify(ctx context.Context, kind string, payload any) error {
	body, err := jsonRaw(payload)
	if err != nil {
		return err
	}
	return c.send(ctx, Message{Kind: kind, Token: c.Token, Payload: body})
}

// Answer replies to a message the core sent, tying the reply to it.
func (c *Client) Answer(ctx context.Context, id string, payload any) error {
	body, err := jsonRaw(payload)
	if err != nil {
		return err
	}
	return c.send(ctx, Message{ID: id, Kind: Answer, Token: c.Token, Payload: body})
}

func (c *Client) send(ctx context.Context, message Message) error {
	frame, err := message.Envelope()
	if err != nil {
		return err
	}

	// One writer at a time. The protocol says frames may not be interleaved, and a module
	// that answers from a read loop and notifies from a job at the same time would
	// interleave them without this — which the core would read as one corrupt frame and
	// close the connection over.
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrNotConnected
	}
	if err := c.conn.Write(ctx, websocket.MessageText, frame); err != nil {
		c.conn = nil
		return fmt.Errorf("send a %q message: %w", message.Kind, err)
	}
	c.sent++
	return nil
}

// Run keeps the channel open until the context ends.
//
// Reconnects on its own, because a channel that has to be reopened by hand is a channel that is
// closed whenever the process is unlucky, and a module that is quietly disconnected for an hour
// looks exactly like a module with nothing to do.
func (c *Client) Run(ctx context.Context) {
	wait := c.Backoff
	if wait <= 0 {
		wait = time.Second
	}
	longest := c.MaxBackoff
	if longest <= 0 {
		longest = time.Minute
	}

	for ctx.Err() == nil {
		if err := c.session(ctx); err != nil && ctx.Err() == nil {
			c.log().Warn("the channel to the core ended", "error", err, "retry_in", wait)
		}
		if !sleep(ctx, wait) {
			return
		}
		// Doubled up to a ceiling, so a core that stays down is not asked again and again by
		// every module at once, and a core that comes back is not waited for.
		if wait *= 2; wait > longest {
			wait = longest
		}
	}
}

// session is one connection, from dial to close.
func (c *Client) session(ctx context.Context) error {
	endpoint, err := c.endpoint()
	if err != nil {
		return err
	}

	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
		// Nothing here is big enough for compression to pay for itself, and a negotiated
		// extension between the two ends is one more thing that can differ.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return fmt.Errorf("open the channel: %w", err)
	}

	// The bearer header is sent as well as the token in every message, and that is not
	// redundancy: the header is what the upgrade itself is authenticated with, before a
	// message exists to carry a token in.
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	c.log().Info("the channel to the core is open")

	// The context is detached from the request: a dial context that ends takes the
	// connection with it, and this one lives until the module stops.
	session := context.WithoutCancel(ctx)
	readErr := c.read(session, conn)

	if readErr != nil && !isExpectedClose(readErr) {
		return readErr
	}
	return nil
}

// read consumes until the connection ends.
//
// Nothing is done with a message beyond handing it on. Anything clever here — acknowledging,
// ordering, deduplicating — is state, and state is what the token in every message exists to
// avoid.
func (c *Client) read(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		message, err := Decode(data)
		if err != nil {
			return err
		}

		// The core's last word before hanging up on a bad token. Logged as an error because
		// the module cannot fix it on its own: it is the core's answer to a token that is
		// wrong, expired or cancelled, and only the author of the module can act on it.
		if message.Kind == Refused {
			var refusal Refusal
			_ = message.PayloadInto(&refusal)
			return fmt.Errorf("the core refused this module: %s", refusal.Reason)
		}

		if c.OnMessage != nil {
			c.OnMessage(ctx, message)
		}
	}
}

func (c *Client) endpoint() (string, error) {
	base := strings.TrimSpace(c.URL)
	if base == "" {
		return "", fmt.Errorf("no core address to open a channel to")
	}
	base = strings.TrimSuffix(base, "/")
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("the core address %q cannot be read: %w", c.URL, err)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("the core address %q is not http or https", c.URL)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/api/v1/module/channel"
	return parsed.String(), nil
}

func (c *Client) log() Log {
	if c.Log != nil {
		return c.Log
	}
	return discardLog{}
}

// isExpectedClose says whether an error is a close either end meant.
//
// The protocol's normal closure and a going-away are the two that happen without anything being
// wrong, and logging them as failures trains somebody to ignore the log line that means a token
// ended.
func isExpectedClose(err error) bool {
	return websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
