package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NotificationRepo is the queue notification modules read from.
//
// It is a queue rather than a broadcast: a module that was down for an hour has to
// be able to deliver that hour when it comes back, and a fire-and-forget signal
// cannot do that. What has been sent is remembered, though not kept — the record
// answers "what happened after this point", which is the question a module that
// was reinstalled actually has.
type NotificationRepo struct{ s *Store }

func (s *Store) Notifications() *NotificationRepo { return &NotificationRepo{s: s} }

// Notification is one thing that happened, in words a module can send.
type Notification struct {
	ID     int64
	Kind   string
	Text   string
	URL    string
	Levels []string
	Data   map[string]any
	At     time.Time
}

// retention is how long notifications stay readable.
//
// Long enough for an outage to be recovered from, short enough that the table does
// not become the largest thing in the database. A month is longer than anybody has
// ever wanted a notification they did not get.
const notificationRetention = 30 * 24 * time.Hour

// Record queues one notification for the modules that asked for notifications.
func (r *NotificationRepo) Record(ctx context.Context, kind, text, url string,
	data map[string]any) (int64, error) {

	if strings.TrimSpace(text) == "" {
		return 0, nil
	}
	if data == nil {
		data = map[string]any{}
	}

	var id int64
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO notifications (kind, text, url, data)
		VALUES ($1, $2, $3, $4) RETURNING id`, kind, text, url, data).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record notification: %w", err)
	}

	if _, err := r.s.pool.Exec(ctx,
		`DELETE FROM notifications WHERE created_at < now() - $1::interval`,
		notificationRetention.String()); err != nil {
		// Pruning is housekeeping, not the job. A failure here is worth a line in the
		// log and not worth failing a pipeline over.
		return id, nil
	}
	return id, nil
}

// Since returns notifications a module of the given kind prefix has not seen.
func (r *NotificationRepo) Since(ctx context.Context, kindPrefix string, after int64, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT id, kind, text, url, levels, data, created_at
		FROM notifications
		WHERE id > $1 AND kind LIKE $2 || '%'
		ORDER BY id LIMIT $3`, after, kindPrefix, limit)
	if err != nil {
		return nil, fmt.Errorf("read notifications: %w", err)
	}
	defer rows.Close()

	notes := []Notification{}
	for rows.Next() {
		var note Notification
		if err := rows.Scan(&note.ID, &note.Kind, &note.Text, &note.URL,
			&note.Levels, &note.Data, &note.At); err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

// Acknowledge records how far a module has got.
//
// Nothing is deleted. A module that is reinstalled and asks "what did I miss while
// I was gone" gets an answer from this, and that is a question somebody always
// asks afterwards.
func (r *NotificationRepo) Acknowledge(ctx context.Context, kindPrefix string, cursor int64) error {
	if cursor <= 0 {
		return nil
	}

	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO notification_cursors (kind_prefix, cursor, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (kind_prefix) DO UPDATE
		SET cursor = GREATEST(notification_cursors.cursor, EXCLUDED.cursor),
		    updated_at = now()`, kindPrefix, cursor)
	if err != nil {
		return fmt.Errorf("acknowledge notifications: %w", err)
	}
	return nil
}

// Cursor is how far a module of this kind has got, which is where it resumes after
// a restart it did not choose.
func (r *NotificationRepo) Cursor(ctx context.Context, kindPrefix string) (int64, error) {
	var cursor int64
	err := r.s.pool.QueryRow(ctx,
		`SELECT cursor FROM notification_cursors WHERE kind_prefix = $1`, kindPrefix).Scan(&cursor)
	if errors.Is(err, pgxNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read notification cursor: %w", err)
	}
	return cursor, nil
}
