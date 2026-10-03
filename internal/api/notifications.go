package api

import (
	"net/http"
	"strings"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// notifyKindPrefix is what a notification module's kind starts with.
//
// The core does not know what Telegram is, or email, or anything else. It knows
// only that some modules want to be told what happened, and that is enough to
// build the queue without a single mention of any of them.
const notifyKindPrefix = "notify:"

// notification is one thing that happened, as a module will hear about it.
type notification struct {
	ID     int64          `json:"id"`
	Kind   string         `json:"kind"`
	Text   string         `json:"text"`
	URL    string         `json:"url,omitempty"`
	Levels []string       `json:"levels,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// handleModuleNotifications hands a notification module what it has not sent.
//
// It asks from a cursor rather than subscribing, so a module that was down for an
// hour delivers that hour. A notification system that loses what happened while it
// was broken is worse than none, because it is trusted.
func (s *Server) handleModuleNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.isNotificationModule(r) {
		s.writeError(w, r, errForbidden("this module does not receive notifications"))
		return
	}

	var req struct {
		After int64 `json:"after"`
	}
	if err := decodeOptionalJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	notes, err := s.store.Notifications().Since(r.Context(), notifyKindPrefix, req.After, 100)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"notifications": viewsOf(notes),
		"cursor":        cursorAfter(notes),
	})
}

// handleAcknowledgeNotifications records how far a module has got.
//
// It is a cursor rather than a deletion: a message that was sent and then deleted
// leaves nothing for a module that is reinstalled and asks what it missed, which
// is exactly the question somebody will ask afterwards.
func (s *Server) handleAcknowledgeNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.isNotificationModule(r) {
		s.writeError(w, r, errForbidden("this module does not receive notifications"))
		return
	}

	var req struct {
		Cursor int64 `json:"cursor"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.store.Notifications().Acknowledge(r.Context(), notifyKindPrefix, req.Cursor); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"cursor": req.Cursor})
}

// handleTestNotification asks for one test message to be sent.
//
// The button on the module page calls it, and it does not check that a module is
// even configured: somebody checking whether their settings work should not be
// refused by the settings they are trying to check.
func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integration, err := s.notificationModule(r)
	if err != nil {
		s.writeError(w, r, errNotFound("no notification module is installed"))
		return
	}

	text := strings.TrimSpace(pathParam(r, "text"))
	if text == "" {
		text = "This is dogit. If you are reading this in Telegram, the module works."
	}

	if _, err := s.store.Notifications().Record(r.Context(), "test", text, "/admin/modules", nil); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("a test notification was queued", "module", integration.Kind, "by",
		userFrom(r.Context()).Username)

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{"queued": true})
}

// notify queues a message for whoever asked to be told.
//
// It writes to the queue and returns. Nothing here waits for a module to answer,
// because the thing that happened has already happened and a slow notification
// module must not make a push slow.
//
// A queue nobody is reading is not an error: most installations have no
// notification module at all, and the table is pruned on its own.
func (s *Server) notify(r *http.Request, projectPath, kind, text, url string, data map[string]any) {
	if strings.TrimSpace(text) == "" {
		return
	}

	if _, err := s.store.Notifications().Record(r.Context(), kind, text, url, data); err != nil {
		// A notification that could not be queued is worth a line in the log and
		// nothing more: failing the push because a notification did not fit would
		// make the two worse things worse.
		s.log.Warn("could not queue a notification", "kind", kind, "error", err)
	}
}

// notificationModule finds the module that receives notifications.
func (s *Server) notificationModule(r *http.Request) (*models.Integration, error) {
	integrations, err := s.store.Integrations().List(r.Context())
	if err != nil {
		return nil, err
	}
	for _, integration := range integrations {
		if strings.HasPrefix(integration.Kind, notifyKindPrefix) && integration.Enabled {
			return integration, nil
		}
	}
	return nil, store.ErrNotFound
}

// isNotificationModule is the one place that decides whether a caller may read the
// notification queue.
func (s *Server) isNotificationModule(r *http.Request) bool {
	integration := integrationFrom(r.Context())
	return integration != nil && strings.HasPrefix(integration.Kind, notifyKindPrefix)
}

func viewsOf(notes []store.Notification) []notification {
	views := make([]notification, 0, len(notes))
	for _, note := range notes {
		views = append(views, notification{
			ID:     note.ID,
			Kind:   note.Kind,
			Text:   note.Text,
			URL:    note.URL,
			Levels: note.Levels,
			Data:   note.Data,
		})
	}
	return views
}

func cursorAfter(notes []store.Notification) int64 {
	var cursor int64
	for _, note := range notes {
		if note.ID > cursor {
			cursor = note.ID
		}
	}
	return cursor
}
