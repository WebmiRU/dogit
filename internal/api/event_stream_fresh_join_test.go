package api

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/events"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// What a client is handed the moment it opens a socket, which is a different question from
// what it is handed after it has been away.
//
// The two were one question, and answered as one, and the cost fell on the wrong reader: a
// page opened for the first time has no cursor, so the whole table came back — every deploy
// that ever ran on the instance — and the browser, which keeps thirty seconds of events in
// memory to serve components that subscribe late, dropped all of it. On this instance that
// was 8996 events and 3.9 MiB to keep none.
//
// A client that reconnects with a cursor is the opposite case and must not be affected: it
// missed something, and the cursor is precisely what says what.

const (
	// seededEvents is more than the tail a fresh join is given, so that the two answers
	// differ and the test is capable of failing.
	seededEvents = 260
)

// standUpStream starts the API and returns its address plus a session to use.
func standUpStream(t *testing.T) (string, *store.Store, string) {
	t.Helper()

	st := dbtest.Open(t)
	user := dbtest.NewUser(t, st, "join", true)
	session := dbtest.NewSession(t, st, user.ID)

	srv := &Server{
		cfg:    &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:    logger.Discard(),
		store:  st,
		git:    gitx.New(gitx.Options{}),
		repos:  repos.New(st, gitx.New(gitx.Options{}), t.TempDir()),
		events: events.New(st, logger.Discard()),
	}

	server := httptest.NewServer(srv.Routes())
	t.Cleanup(server.Close)
	return server.URL, st, session
}

// seedEvents writes events and returns the id of the last one.
func seedEvents(t *testing.T, st *store.Store, n int) int64 {
	t.Helper()

	for i := 0; i < n; i++ {
		if _, err := st.Pool().Exec(context.Background(), `
			INSERT INTO events (kind, payload, created_at)
			VALUES ('deploy.operation', $1::jsonb, now())`,
			[]byte(`{"status":"running"}`)); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}

	var last int64
	if err := st.Pool().QueryRow(context.Background(), `SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&last); err != nil {
		t.Fatalf("read the newest event id: %v", err)
	}
	return last
}

// readUntilSync collects the events a stream sends before it settles, and the ids it began at.
func readUntilSync(t *testing.T, conn *websocket.Conn) []int64 {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var ids []int64
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return ids
		}
		var entry struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(data, &entry) != nil {
			continue
		}
		// The stream announces that it has caught up; everything that was coming is here.
		if entry.Kind == "sync" {
			return ids
		}
		if entry.ID > 0 {
			ids = append(ids, entry.ID)
		}
	}
}

// dialStream opens the event socket as the browser does and gives it a resume cursor.
func dialStream(t *testing.T, address, session string, since int64) *websocket.Conn {
	t.Helper()

	header := nethttp.Header{}
	header.Set("Cookie", "dogit_session="+session)
	// The API answers the same-origin browser only; a request without this is not that.
	header.Set("Sec-Fetch-Site", "same-origin")

	url := "ws" + strings.TrimPrefix(address, "http") + "/events/socket"
	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial the event socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"since":`+strconv.FormatInt(since, 10)+`}`)); err != nil {
		t.Fatalf("give the socket its cursor: %v", err)
	}
	return conn
}

// A client with no cursor is joining, not catching up, and is given the tail.
func TestAJoiningClientIsNotGivenEveryEventThatEverHappened(t *testing.T) {
	address, st, session := standUpStream(t)
	last := seedEvents(t, st, seededEvents)

	conn := dialStream(t, address, session, 0)
	ids := readUntilSync(t, conn)

	if len(ids) > eventStreamFreshJoinEvents {
		t.Fatalf("a joining client was given %d events, which is more than the tail of %d: %d",
			len(ids), eventStreamFreshJoinEvents, last)
	}
	if len(ids) == 0 {
		t.Fatal("a joining client was given nothing at all, so it cannot tell a quiet instance from a broken stream")
	}
	// The ones it gets must be the newest, not the oldest: the whole point of a tail.
	if ids[0] <= last-int64(eventStreamFreshJoinEvents) {
		t.Fatalf("a joining client was given events from the beginning of time: the first it received was #%d, and the newest is #%d",
			ids[0], last)
	}
}

// A client with a cursor is owed everything it missed, and the tail rule must not touch it.
func TestAClientWithACursorStillGetsEverythingItMissed(t *testing.T) {
	address, st, session := standUpStream(t)
	last := seedEvents(t, st, seededEvents)

	cursor := last - 30
	conn := dialStream(t, address, session, cursor)
	ids := readUntilSync(t, conn)

	if len(ids) != 30 {
		t.Fatalf("a client resuming from #%d was given %d events, want 30: the tail rule has swallowed a real catch-up",
			cursor, len(ids))
	}
	for _, id := range ids {
		if id <= cursor {
			t.Fatalf("a client resuming from #%d was sent #%d, which it already had", cursor, id)
		}
	}
}

// The newest event a user may see, and not merely the newest that exists.
func TestTheNewestVisibleEventRespectsVisibility(t *testing.T) {
	st := dbtest.Open(t)
	stranger := dbtest.NewUser(t, st, "stranger", false)
	owner := dbtest.NewUser(t, st, "owner", false)
	secret := dbtest.NewProject(t, st, "secret", nil)

	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `UPDATE projects SET visibility = 'private' WHERE id = $1`, secret.ID); err != nil {
		t.Fatalf("make the project private: %v", err)
	}

	// What the stranger could already see. The instance carries events of its own — a module
	// registering, a project being made — and those belong to everybody, so the question is
	// not "did they see anything" but "did this event reach them".
	var before int64
	if err := st.Pool().QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&before); err != nil {
		t.Fatalf("read the newest event before seeding: %v", err)
	}

	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO events (kind, project_id, actor_id, payload, created_at)
		VALUES ('deploy.operation', $1, $2, $3::jsonb, now())`,
		secret.ID, owner.ID, []byte(`{}`)); err != nil {
		t.Fatalf("seed a private event: %v", err)
	}

	seen, err := st.Events().LatestVisibleID(ctx, stranger.ID, nil)
	if err != nil {
		t.Fatalf("ask what a stranger may see: %v", err)
	}
	if seen > before {
		t.Fatalf("a stranger was pointed at event #%d, which belongs to a private project: the newest they may see is #%d",
			seen, before)
	}

	forProject, err := st.Events().LatestVisibleID(ctx, stranger.ID, &secret.ID)
	if err != nil {
		t.Fatalf("ask what a stranger may see of one project: %v", err)
	}
	if forProject != 0 {
		t.Fatalf("a stranger was offered event #%d from a private project they cannot open", forProject)
	}
}
