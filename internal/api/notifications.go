package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

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

	kind := integrationFrom(r.Context()).Kind

	// Only what was meant for this module. Reading everything would turn a second
	// installed channel into a second copy of every message.
	notes, err := s.store.Notifications().Since(r.Context(), kind, req.After, 100)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Where this module got to, so a restart resumes instead of starting again.
	//
	// Without it every redeploy replays the whole queue: a module that cannot say
	// what it has already sent will send all of it again, and the messages a person
	// has seen arrive twice are the reason people stop trusting a notifier.
	resume, err := s.store.Notifications().Cursor(r.Context(), kind)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"notifications": viewsOf(notes),
		"cursor":        cursorAfter(notes),
		"resume_from":   resume,
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
	// Keyed by the module's own kind: one shared cursor would mean the second
	// channel to be installed is told about nothing, because the first one has
	// already read past it.
	if err := s.store.Notifications().Acknowledge(r.Context(),
		integrationFrom(r.Context()).Kind, req.Cursor); err != nil {
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

	// A test goes to the module that pressed the button, whichever it is: the point
	// is to check that this particular channel works.
	if _, err := s.store.Notifications().Record(r.Context(), "test", integration.Kind,
		text, "test", nil, nil); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("a test notification was queued", "module", integration.Kind, "by",
		userFrom(r.Context()).Username)

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{"queued": true})
}

// notificationTarget is one recipient, resolved, and the module that delivers it.
type notificationTarget struct {
	store.EffectiveTarget
	// ModuleKind is the module that owns the row. The core does not know what any of
	// them are; it only needs to know which queue a message belongs in.
	ModuleKind string
	// Label is the row's name as somebody would say it out loud: a chat id, an
	// address. It comes from the settings the module asked about, so it is whatever
	// the channel calls its destination.
	Label string
}

// notificationTargets is who gets told about a project's events.
//
// Any number of them, from any number of modules: a deployment may want the shared
// chat, its own topic and a mailing list, and none of those is any more or less
// correct than the others. What is not allowed is guessing, so a recipient that
// cannot make up its mind about its own settings is reported rather than delivered
// to on a hunch.
//
// The rows are inherited from the instance down through the group to the project,
// and each is switched on or off by the most specific level that said anything.
func (s *Server) notificationTargets(ctx context.Context, projectPath string) []notificationTarget {
	project, err := s.store.Projects().ByPath(ctx, projectPath)
	if err != nil {
		// Nothing to inherit from without a project, and a project that does not
		// exist cannot be notified about anyway.
		s.log.Warn("could not read the project to resolve its recipients", "project", projectPath, "error", err)
		return nil
	}

	integrations, err := s.store.Integrations().List(ctx)
	if err != nil {
		s.log.Warn("read installed modules", "error", err)
		return nil
	}

	var groupID, projectID *uuid.UUID
	if project.GroupID != nil {
		groupID = project.GroupID
	}
	projectID = &project.ID

	out := []notificationTarget{}
	for _, integration := range integrations {
		if !strings.HasPrefix(integration.Kind, notifyKindPrefix) || !integration.Enabled {
			continue
		}

		s.adoptLegacySettings(ctx, integration)

		resolved, err := s.store.ModuleTargets().Effective(ctx, integration.ID, groupID, projectID)
		if err != nil {
			s.log.Warn("read notification recipients", "module", integration.Kind, "error", err)
			continue
		}
		if len(resolved.Stale) > 0 {
			s.log.Info("notification settings that no longer apply were dropped",
				"project", projectPath, "module", integration.Kind, "dropped", len(resolved.Stale))
		}

		for _, row := range resolved.Targets {
			if !row.Enabled {
				continue
			}
			out = append(out, notificationTarget{
				EffectiveTarget: row,
				ModuleKind:      integration.Kind,
				Label:           s.targetLabel(integration, row),
			})
		}
	}
	return out
}

// adoptLegacySettings moves a module's flat settings into a recipient row.
//
// Modules used to keep everything in one set of settings, which cannot express "two
// chats". An installation that has one set of settings gets one row out of it, once,
// so that upgrading turns its configuration into a recipient rather than losing it.
//
// Only for a module that has not said what a recipient of it is. A module that has —
// which is every module written against the recipients list — keeps its own
// recipients, and inventing one beside them would leave somebody receiving the same
// message twice.
func (s *Server) adoptLegacySettings(ctx context.Context, integration *models.Integration) {
	if target := integration.Capabilities.Target; target != nil &&
		(len(target.Identify) > 0 || target.Title != "") {
		return
	}

	targets := s.store.ModuleTargets()
	existing, err := targets.At(ctx, integration.ID, store.ScopeInstance, nil)
	if err != nil || len(existing) > 0 {
		return
	}

	settings, err := s.store.Integrations().SettingsAt(ctx, integration.ID, store.ScopeInstance, nil)
	if err != nil {
		return
	}
	values := map[string]json.RawMessage{}
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	if len(values) == 0 {
		return
	}

	if _, err := targets.Create(ctx, &store.ModuleTarget{
		IntegrationID: integration.ID,
		ScopeType:     store.ScopeInstance,
		Label:         integration.Name,
		Values:        values,
	}); err != nil {
		s.log.Warn("could not turn the module's settings into a recipient",
			"module", integration.Kind, "error", err)
		return
	}
	s.log.Info("the module's existing settings became a notification recipient",
		"module", integration.Kind, "settings", len(values))
}

// targetLabel is what to call a recipient in a list.
//
// The module says which of its settings identify one — a chat id, an address —
// rather than the core guessing from a column name, because only the module knows
// what its settings mean. A module that names none gets its own row name.
func (s *Server) targetLabel(integration *models.Integration, row store.EffectiveTarget) string {
	if row.Own.Label != "" {
		return row.Own.Label
	}
	var identify []string
	if target := integration.Capabilities.Target; target != nil {
		identify = target.Identify
	}
	for _, key := range identify {
		raw, ok := row.Values[key]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil && value != "" {
			return value
		}
	}
	return row.Root.Label
}

// notify queues a message for whoever asked to be told.
//
// It writes to the queue and returns. Nothing here waits for a module to answer,
// because the thing that happened has already happened and a slow notification
// module must not make a push slow.
//
// A queue nobody is reading is not an error: most installations have no
// notification module at all, and the table is pruned on its own.
func (s *Server) notify(ctx context.Context, projectPath, kind, text, level string, data map[string]any) {
	if strings.TrimSpace(text) == "" && len(data) == 0 {
		return
	}

	targets := s.notificationTargets(ctx, projectPath)
	if len(targets) == 0 {
		// Nobody asked to be told. That is the ordinary state of an installation
		// with no notification module, so it is not a thing to complain about.
		return
	}

	for _, target := range targets {
		address := target.Address()
		if _, err := s.store.Notifications().Record(ctx, kind, target.ModuleKind, text,
			level, data, &address); err != nil {
			// A message that could not be queued is worth a line in the log and
			// nothing more: failing the build because a notification did not fit
			// would make the two worse things worse.
			s.log.Warn("could not queue a notification",
				"kind", kind, "recipient", target.Label, "error", err)
		}
	}
}

// handleModuleOwnTarget is how a module records the destination its deployment
// named for it.
//
// A deployment that is given a chat id in its environment is telling dogit where
// notifications go, and the only honest place for that is a recipient row like any
// other — otherwise the chat lives in module settings where it cannot be listed,
// inherited, overridden or switched off. The core takes the row and the label it is
// given: an administrator is expected to add a second row by hand, so a module
// cannot quietly take over a list.
//
// Only the module's own instance-level rows are writable here. A project or a group
// belongs to whoever manages it, not to a module that happens to be installed.
func (s *Server) handleModuleOwnTarget(w http.ResponseWriter, r *http.Request) {
	if !s.isNotificationModule(r) {
		s.writeError(w, r, errForbidden("this module does not receive notifications"))
		return
	}

	var req struct {
		Label  string            `json:"label"`
		Values map[string]string `json:"values"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		s.writeError(w, r, errBadRequest("a recipient needs a name"))
		return
	}

	integration := integrationFrom(r.Context())
	values := map[string]json.RawMessage{}
	for key, value := range req.Values {
		values[key] = json.RawMessage(strconv.Quote(value))
	}

	rows, err := s.store.ModuleTargets().At(r.Context(), integration.ID, store.ScopeInstance, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Same name, same row: a module restarting must not add a second recipient every
	// time it comes up.
	for _, row := range rows {
		if row.Label != req.Label {
			continue
		}
		merged := map[string]json.RawMessage{}
		for key, value := range row.Values {
			merged[key] = value
		}
		for key, value := range values {
			merged[key] = value
		}
		row.Values = merged
		if _, err := s.store.ModuleTargets().Update(r.Context(), &row); err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeJSON(w, r, http.StatusOK, map[string]any{"target": targetView(row)})
		return
	}

	created, err := s.store.ModuleTargets().Create(r.Context(), &store.ModuleTarget{
		IntegrationID: integration.ID,
		ScopeType:     store.ScopeInstance,
		Label:         req.Label,
		Position:      len(rows),
		Values:        values,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("the module recorded the destination its deployment named",
		"module", integration.Kind, "recipient", req.Label)

	s.writeJSON(w, r, http.StatusOK, map[string]any{"target": targetView(*created)})
}

// targetView is one recipient as the interface sees it: its name, whether it is
// switched on, and its values.
func targetView(row store.ModuleTarget) map[string]any {
	values := map[string]any{}
	for key, raw := range row.Values {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		values[key] = value
	}
	return map[string]any{
		"id": row.ID, "label": row.Label, "enabled": row.Enabled,
		"position": row.Position, "values": values, "scope_type": row.ScopeType,
		"scope_id": row.ScopeID, "overrides": row.Overrides,
	}
}

// value is one of this recipient's settings, read as text.
func (t notificationTarget) value(key string) string {
	raw, ok := t.Values[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
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
