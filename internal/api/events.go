package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// eventReconnectMS is how long a client waits before reconnecting a dropped event
// stream. It is short because the stream carries nothing that cannot be fetched
// again, so being disconnected costs a moment of delay and nothing else.
const eventReconnectMS = 2000

// eventStreamPing is how often something is written to an idle event connection.
//
// Not decoration. A stream that says nothing for a while is a stream something in
// the middle has concluded is dead: a proxy, a load balancer, a NAT table, or the
// client itself, which is entitled to time a connection out and does not have to say
// so. Whichever it is, the page stops hearing about anything until something else
// happens to make it ask — and a deployment that carries on regardless is the worst
// way for that to find out.
//
// Written twice on purpose. A comment keeps intermediaries moving, and browsers
// ignore it; a named event is real content, which browsers do count and can watch.
// Fifteen seconds rather than twenty or thirty because the cost is a few dozen
// bytes every fifteen seconds on a connection that was going to be open anyway, and
// the alternative is a page that quietly stops knowing what is happening.
const eventStreamPing = 15 * time.Second

// eventStreamCatchUpLimit is how many events are read at once while a connection
// is catching up to the present.
const eventStreamCatchUpLimit = 200

// handleEventLive is the feed as a stream rather than a poll.
//
// The same events, the same cursor and the same visibility rules as the cursor
// read, so a client can be pointed at either and neither knows the difference.
// What changes is the delivery: an event is written the moment it happens instead
// of up to one poll interval after it, and an idle page costs one open connection
// rather than a request every few seconds for ever.
//
// Each event is read back through the same query the cursor read uses. That is one
// query per event, and it is on purpose: the rules about which events a given
// person may see, how they are summarised and what they are called live in that
// query, and a second path that formatted events itself would be a second answer
// to "what is this person allowed to see" — the kind that agrees until it does
// not.
func (s *Server) handleEventLive(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	projectID, ok := s.eventProjectFor(w, r, user)
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, errBadRequest("this connection cannot stream"))
		return
	}

	// Where to resume. A browser reconnecting on its own says so in a header,
	// which is the one thing both ends already agree on; the query parameter is
	// for the first connection, which has no history to be given.
	after := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	} else if raw := r.URL.Query().Get("since"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	}

	// The server sets a write deadline for every response, which is right for a
	// request that finishes and wrong for one that does not: at two minutes this
	// stream would be cut, and the browser would reconnect, and the reader would
	// watch a connection that keeps dropping for no reason it could see. Cleared
	// for this response, and only this one.
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	_ = controller.SetReadDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx must not hold this back
	w.WriteHeader(http.StatusOK)

	// The interval a client waits before reconnecting, said once so the browser
	// does not have to be told twice: once here and once by a client-side constant
	// that would be free to disagree with it.
	fmt.Fprintf(w, "retry: %d\n\n", eventReconnectMS)
	flusher.Flush()

	// Subscribed before catching up, never after. An event published between the
	// catch-up read and the subscription would be in neither, and the cursor would
	// move past it silently. The duplicate this can cause is filtered by id below.
	events, unsubscribe := s.events.Subscribe(256)
	defer unsubscribe()

	ping := time.NewTicker(eventStreamPing)
	defer ping.Stop()

	// A deployment reports every phase and every pod count, so a burst of events is
	// ordinary and arrives all at once. Reading the feed once per event turns a burst
	// of fifteen into fifteen queries per connection, on the same database everything
	// else is using — and a connection that stops getting served looks exactly like one
	// that has died.
	//
	// So the first event of a burst starts a short wait, and everything that arrives
	// in that window goes out in one read. Sixty milliseconds is far below what a
	// person can read as a delay, and far above the gap between the events of one
	// operation.
	settle := time.NewTimer(0)
	if !settle.Stop() {
		<-settle.C
	}
	settled := true
	defer settle.Stop()

	// Everything the client was told is remembered by id, so a slow client that
	// reconnects is sent nothing it has already seen, and an event that arrives by
	// both routes — published live, then read again as the tail catches up — is
	// only sent once.
	lastSent := after

	// What this connection did and when it ended, said once, on the way out.
	//
	// The client knows only that events stopped. It cannot tell the difference between
	// a browser that timed the connection out, something in the middle that closed it,
	// and a server that stopped writing — and the three are fixed in three different
	// places. One line per stream is a few hundred bytes an hour on a busy instance,
	// and it is the difference between knowing why a page went stale and guessing.
	started := time.Now()
	eventsSent := 0
	// Why the stream ended, when we know. A client that goes away mid-burst does not
	// announce it: the next write fails, and that failure is the only evidence of which
	// side hung up. Without it a close looks the same whether the browser decided it was
	// finished, something in the middle closed it, or this process did.
	var lastWriteErr string
	defer func() {
		s.log.Info("event stream closed", "user", user.Username,
			"project", filterName(projectID), "for", time.Since(started).Round(time.Second),
			"events", eventsSent, "cursor", lastSent, "write_error", lastWriteErr)
	}()


	// Writes everything after the cursor and says how many. Zero means the backlog
	// is done — which is the only thing that can mean it, since a query that
	// succeeds with nothing new is not a failure.
	send := func() (int, bool) {
		entries, err := s.store.Events().VisibleSince(r.Context(), user.ID, projectID, lastSent,
			eventStreamCatchUpLimit)
		if err != nil {
			return 0, false
		}
		for _, entry := range entries {
			payload, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", entry.ID, payload); err != nil {
				lastWriteErr = err.Error()
				return len(entries), false
			}
			if entry.ID > lastSent {
				lastSent = entry.ID
			}
			eventsSent++
		}
		if len(entries) > 0 {
			flusher.Flush()
		}
		return len(entries), true
	}

	// The backlog first, in as many reads as it takes. A client that was closed for
	// an hour comes back to an hour of events, and stopping at one page of them
	// would leave it permanently behind.
	//
	// Until a page comes back short. Looping on success instead would never stop:
	// this connection is meant to stay open, and a read that finds nothing new is
	// the normal state of a stream that has caught up, not a reason to read again.
	for {
		written, ok := send()
		if !ok || written < eventStreamCatchUpLimit {
			break
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return

		case <-ping.C:
			// A comment for anything that only counts bytes, and a named event for
			// anything that counts messages — including the client, which ignores the
			// first and can watch the second.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				lastWriteErr = err.Error()
				return
			}
			if _, err := fmt.Fprint(w, "event: ping\ndata: {}\n\n"); err != nil {
				lastWriteErr = err.Error()
				return
			}
			flusher.Flush()

		case ev, open := <-events:
			if !open {
				return
			}
			if settled {
				settle.Reset(60 * time.Millisecond)
				settled = false
			}
			// The event is only a reason to look: what is sent is read back through
			// the query, which decides whether this person may see it at all. A
			// project filter is applied there too, so an event about another project
			// costs nothing but the check.
			if ev.ID <= lastSent {
				continue
			}
			if projectID != nil && (ev.ProjectID == nil || *ev.ProjectID != *projectID) {
				continue
			}
			// Read on the settle timer rather than here, so a burst costs one read.
		case <-settle.C:
			settled = true
			if _, ok := send(); !ok {
				return
			}
		}
	}
}

// handleEventSocket is the same feed as the stream above, over a WebSocket.
//
// Two transports for one feed, deliberately, during the changeover. The events are
// read by the same code and pass the same checks — what this project may see is
// decided in one place and is not a property of how the bytes arrive. What differs is
// the connection: a WebSocket has ping and pong in the protocol itself and reconnects
// by agreement, rather than by the browser's own idea of when an event stream has had
// enough of not arriving.
func (s *Server) handleEventSocket(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	projectID, ok := s.eventProjectFor(w, r, user)
	if !ok {
		return
	}

conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The interface is served from the same origin as the API in every layout, and
		// the credential is a cookie either way.
		InsecureSkipVerify: true,
		// No compression. What travels here is small lines arriving constantly, and
		// squeezing them buys nothing while putting a negotiated extension between the
		// two ends.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.log.Debug("event socket refused", "error", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// A socket has no query string to be reopened with, so where to resume from
	// travels in the first message. Without it the client is sent everything it has not
	// seen; with it, only what it missed while it was away.
	after := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		_, data, err := conn.Read(ctx)
		cancel()
		if err == nil {
			var resume struct {
				Since int64 `json:"since"`
			}
			if json.Unmarshal(data, &resume) == nil {
				after = resume.Since
			}
		}
	}

	started := time.Now()
	eventsSent := 0
	var lastWriteErr string
	defer func() {
		s.log.Info("event socket closed", "user", user.Username,
			"project", filterName(projectID), "for", time.Since(started).Round(time.Second),
			"events", eventsSent, "write_error", lastWriteErr)
	}()

	lastSent := after

	// Subscribed before the catch-up read and never after: an event published between
	// the two would be in neither, and the cursor would move past it silently.
	events, unsubscribe := s.events.Subscribe(256)
	defer unsubscribe()

	send := func() (int, bool) {
		entries, err := s.store.Events().VisibleSince(r.Context(), user.ID, projectID, lastSent,
			eventStreamCatchUpLimit)
		if err != nil {
			return 0, false
		}
		for _, entry := range entries {
			payload, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			err = conn.Write(ctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				lastWriteErr = err.Error()
				return len(entries), false
			}
			if entry.ID > lastSent {
				lastSent = entry.ID
			}
			eventsSent++
		}
		return len(entries), true
	}

	// The backlog, in as many reads as it takes, until a page comes back short.
	for {
		written, ok := send()
		if !ok || written < eventStreamCatchUpLimit {
			break
		}
	}

	// Ping on a timer, as the stream does. The protocol answers with a pong of its own
	// accord, so this is checked rather than merely sent.
	ping := time.NewTicker(eventStreamPing)
	defer ping.Stop()

	settle := time.NewTimer(0)
	if !settle.Stop() {
		<-settle.C
	}
	settled := true
	defer settle.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-ping.C:
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			err := conn.Ping(ctx)
			cancel()
			if err != nil {
				lastWriteErr = err.Error()
				return
			}

		case <-settle.C:
			settled = true
			if _, ok := send(); !ok {
				return
			}

		case ev, open := <-events:
			if !open {
				return
			}
			if projectID != nil && (ev.ProjectID == nil || *ev.ProjectID != *projectID) {
				continue
			}
			if ev.ID <= lastSent {
				continue
			}
			if settled {
				settle.Reset(60 * time.Millisecond)
				settled = false
			}
		}
	}
}

// filterName is what a log line can say about a stream's project filter without
// carrying the uuid, which tells a reader nothing.
func filterName(projectID *uuid.UUID) string {
	if projectID == nil {
		return "the whole instance"
	}
	return projectID.String()
}

// handleEventStream serves the event feed as a cursor read.
//
// Kept beside the stream rather than replaced by it: a client that cannot hold a
// connection open — a script, a proxy, a browser tab in a state where the stream
// is not welcome — can still ask what has happened since a number, and the two
// answer the same question the same way.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	afterID := int64(queryInt(r, "since", 0, 1<<62))
	limit := queryInt(r, "limit", 50, 200)

	// A project filter lets a page subscribe narrowly. It is checked here rather
	// than left to the query, because refusing to answer at all is clearer than an
	// empty feed that looks like "nothing is happening".
	projectID, ok := s.eventProjectFor(w, r, user)
	if !ok {
		return
	}

	entries, err := s.store.Events().VisibleSince(r.Context(), user.ID, projectID, afterID, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The cursor is the highest id seen, so a client filtering on one project still
	// advances past events meant for others instead of re-reading them forever.
	cursor := afterID
	for _, entry := range entries {
		if entry.ID > cursor {
			cursor = entry.ID
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"events":   entries,
		"cursor":   cursor,
		"retry_ms": eventReconnectMS,
	})
}

// eventProjectFor reads the project filter and says whether this person may have
// it.
//
// Both feed shapes ask this first, and asking it in two places is how a filter
// ends up enforced on one path and forgotten on the other.
func (s *Server) eventProjectFor(w http.ResponseWriter, r *http.Request, user *models.User) (*uuid.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("project"))
	if raw == "" {
		return nil, true
	}

	project, err := s.resolveProjectRef(r.Context(), raw)
	if err != nil {
		s.writeError(w, r, errNotFoundf("project %q does not exist", raw))
		return nil, false
	}

	full, err := s.store.Projects().ByID(r.Context(), *project)
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}

	allowed, err := s.store.Permissions().Can(r.Context(), user, full, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return nil, false
	}
	if !allowed {
		// Reported as missing rather than forbidden: a caller who cannot see the
		// project should not learn that it exists.
		s.writeError(w, r, errNotFound("project does not exist"))
		return nil, false
	}
	return project, true
}
