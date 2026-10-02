package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// uninstallSilence is the same deadline the janitor uses, named here so the
// handler that starts a job and the loop that judges it cannot disagree.
const uninstallSilence = store.StalledAfter

// uninstallStreamLine is one line of what a module says while it removes itself.
//
// The last line of the stream is a summary rather than a message, which is why
// these are two shapes and not one with an optional summary.
type uninstallStreamLine struct {
	Message  string         `json:"message"`
	Level    string         `json:"level"`
	Progress map[string]any `json:"progress"`
	Summary  map[string]any `json:"summary"`
	// Anything else the module wants to say. Unknown fields are tolerated here,
	// unlike everywhere else in the API, because this stream is written by a
	// version of the module the core has never read.
	Error string `json:"error"`
}

// handleStartModuleUninstall begins removing a module.
//
// The response comes back at once and the removal continues in the background:
// it may take hours, and the administrator asking for it is not going to sit
// there. What they get instead is a job to watch.
func (s *Server) handleStartModuleUninstall(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req struct {
		Options []string `json:"options"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	// The module declares its own options, and the core checks the answer against
	// that declaration rather than against anything it knows. An unknown key means
	// the core and the module disagree about what removal means — often an old
	// module against a new interface — and guessing which of them is right is how
	// data gets deleted that somebody meant to keep.
	if err := validateUninstallOptions(integration.Capabilities.Uninstall, req.Options); err != nil {
		s.writeError(w, r, err)
		return
	}

	job, err := s.store.ModuleUninstall().Start(r.Context(), integration.ID, req.Options)
	if errors.Is(err, store.ErrJobRunning) {
		s.writeError(w, r, errConflictf("module %q is already being removed", integration.Kind))
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("module removal started",
		"kind", integration.Kind, "module_id", integration.ID,
		"options", strings.Join(req.Options, ","), "user", userFrom(r.Context()).Username)

	// The request context dies with the request, and so must not cancel work that
	// outlives it.
	go s.runUninstall(context.WithoutCancel(r.Context()), integration, job)

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

// validateUninstallOptions checks the chosen keys against what the module offered.
func validateUninstallOptions(spec models.UninstallSpec, chosen []string) error {
	declared := map[string]models.UninstallOption{}
	for _, option := range spec.Options {
		declared[option.Key] = option
	}

	for _, key := range chosen {
		if _, ok := declared[key]; !ok {
			return errBadRequestf("this module did not offer an option called %q", key)
		}
	}

	// A module that says an option is required is saying "removing me without
	// this loses data". Obeying that is the point of the field.
	for _, option := range spec.Options {
		if !option.Required {
			continue
		}
		if !containsString(chosen, option.Key) {
			return errBadRequestf("this module cannot be removed without %q: %s",
				option.Key, option.Description)
		}
	}
	return nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// runUninstall asks a module to remove itself and keeps its log.
//
// Everything the module says is written to the database as it arrives, so the
// administrator can close the tab and come back. The core does not summarise,
// judge or second-guess: the module knows what it is deleting, and the core's job
// is to keep the record.
func (s *Server) runUninstall(ctx context.Context, integration *models.Integration, job *models.UninstallJob) {
	repo := s.store.ModuleUninstall()

	if err := repo.MarkRunning(ctx, job.ID); err != nil {
		s.log.Error("could not mark the removal as running", "job_id", job.ID, "error", err)
		return
	}

	s.appendLine(ctx, job.ID, "info",
		fmt.Sprintf("asking %s to remove itself", integration.Name), nil)

	err := s.streamUninstall(ctx, integration, job, repo)
	switch {
	case err == nil:
		s.appendLine(ctx, job.ID, "info", "the module finished removing itself", nil)
		if failure := repo.Finish(ctx, job.ID, models.UninstallDone, nil, ""); failure != nil {
			s.log.Error("could not record the finished removal", "job_id", job.ID, "error", failure)
		}
		s.log.Info("module removed", "kind", integration.Kind, "module_id", integration.ID)
	case errors.Is(err, context.Canceled):
		// Only the core going away produces this. What the module managed to do
		// stays in the log, and the outcome is marked unknown rather than bad.
		if failure := repo.Finish(ctx, job.ID, models.UninstallInterrupted, nil,
			"the core stopped while this removal was running; its outcome is unknown"); failure != nil {
			s.log.Error("could not record the interrupted removal", "job_id", job.ID, "error", failure)
		}
	default:
		s.appendLine(ctx, job.ID, "error", err.Error(), nil)
		if failure := repo.Finish(ctx, job.ID, models.UninstallFailed, nil, err.Error()); failure != nil {
			s.log.Error("could not record the failed removal", "job_id", job.ID, "error", failure)
		}
		s.log.Warn("module removal failed",
			"kind", integration.Kind, "module_id", integration.ID, "error", err)
	}
}

// streamUninstall reads the module's ndjson log until the stream ends.
func (s *Server) streamUninstall(ctx context.Context, integration *models.Integration,
	job *models.UninstallJob, repo *store.ModuleUninstallRepo) error {

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(integration.Endpoint, "/")+"/uninstall",
		strings.NewReader(marshalUninstallRequest(job.Options)))
	if err != nil {
		return fmt.Errorf("could not address the module: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")

	// A removal is not something the module can refuse politely by timing out: it
	// may be hours long, and the core waits for as long as it takes.
	client := &http.Client{Timeout: 0}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("the module did not answer: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("the module refused: %s: %s",
			response.Status, strings.TrimSpace(string(body)))
	}

	var summary map[string]any
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var said uninstallStreamLine
		if err := json.Unmarshal([]byte(line), &said); err != nil {
			// A line we cannot read is still a line the module wrote, and it is
			// passed through as text rather than swallowed: it may be the one line
			// explaining what went wrong.
			s.appendLine(ctx, job.ID, "warn", line, nil)
			continue
		}

		if len(said.Summary) > 0 {
			summary = said.Summary
			continue
		}

		if said.Progress != nil {
			_ = repo.Progress(ctx, job.ID,
				int64FromAny(said.Progress["done"]), int64FromAny(said.Progress["total"]))
		}

		level := said.Level
		if level == "" {
			level = "info"
		}
		if said.Error != "" {
			level = "error"
		}
		s.appendLine(ctx, job.ID, level, said.Message, said.Progress)
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("the module stopped talking: %w", err)
	}

	if len(summary) > 0 {
		if err := repo.Finish(ctx, job.ID, models.UninstallDone, summary, ""); err != nil {
			return fmt.Errorf("could not record the summary: %w", err)
		}
		s.appendLine(ctx, job.ID, "info", describeSummary(summary), nil)
	}
	return nil
}

// appendLine writes one line and notes that the module is alive.
func (s *Server) appendLine(ctx context.Context, jobID uuid.UUID, level, message string, progress map[string]any) {
	if message == "" {
		return
	}
	if _, err := s.store.ModuleUninstall().AppendLog(ctx, jobID, level, message, progress); err != nil {
		s.log.Error("could not append to the removal log", "job_id", jobID, "error", err)
		return
	}
	_ = s.store.ModuleUninstall().Touch(ctx, jobID)
}

// handleGetModuleUninstall reports a module's current removal, if it has one.
func (s *Server) handleGetModuleUninstall(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The unfinished job if there is one, otherwise the last one that ran: an
	// administrator who has just watched a removal finish still wants to read it.
	job, err := s.store.ModuleUninstall().Current(r.Context(), integration.ID)
	if errors.Is(err, store.ErrNotFound) {
		job, err = s.store.ModuleUninstall().Latest(r.Context(), integration.ID)
	}
	if errors.Is(err, store.ErrNotFound) {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"job": nil})
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"job": job})
}

// logPollInterval is how often an open log stream asks the database for new
// lines.
//
// One second, rather than a notification: a removal log is a handful of lines a
// minute at most, and polling a table by index is cheap in a way that waking
// every watcher on every line is not. The cost of this choice is at most a
// second of lag, which nobody watching a deletion can see.
const logPollInterval = time.Second

// handleModuleUninstallLog streams a removal's log.
//
// The stream is served from the database rather than from a live pipe, so an
// administrator who reconnects — after a laptop lid, a tunnel, a redeploy — picks
// up exactly where they were and misses nothing. That is the whole reason the
// log is stored.
func (s *Server) handleModuleUninstallLog(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	job, err := s.store.ModuleUninstall().Current(r.Context(), integration.ID)
	if errors.Is(err, store.ErrNotFound) {
		job, err = s.store.ModuleUninstall().Latest(r.Context(), integration.ID)
	}
	if err != nil {
		s.writeError(w, r, errBadRequest("this module has no removal to show"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, errBadRequest("this connection cannot stream"))
		return
	}

	// A reconnecting browser says where it got to, and the standard header is the
	// one thing both ends already agree on.
	after := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	} else if raw := r.URL.Query().Get("after"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx must not hold this back
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	repo := s.store.ModuleUninstall()
	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()

	for {
		lines, err := repo.LogAfter(r.Context(), job.ID, after, 0)
		if err != nil {
			// The database is gone. There is nothing honest left to send.
			return
		}
		for _, line := range lines {
			payload, err := json.Marshal(line)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", line.ID, payload)
			after = line.ID
		}
		if len(lines) > 0 {
			flusher.Flush()
		}

		// The job's state travels on the same stream, so the interface learns a
		// removal finished without a second connection to ask.
		if current, err := repo.ByID(r.Context(), job.ID); err == nil {
			state, err := json.Marshal(map[string]any{"job": current})
			if err == nil {
				fmt.Fprintf(w, "event: job\ndata: %s\n\n", state)
				flusher.Flush()
			}
			if current.Finished() {
				return
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func marshalUninstallRequest(options []string) string {
	body, err := json.Marshal(map[string]any{"options": options})
	if err != nil {
		return `{"options":[]}`
	}
	return string(body)
}

// int64FromAny reads a number out of a decoded JSON value, or nil when it is not
// one. Progress the module reported in a different shape is ignored rather than
// turned into a zero, which would look like real progress.
func int64FromAny(value any) *int64 {
	number, ok := value.(float64)
	if !ok {
		return nil
	}
	converted := int64(number)
	return &converted
}

// describeSummary turns the module's own totals into a line for the log, so the
// last thing an administrator reads is the point of the exercise.
func describeSummary(summary map[string]any) string {
	parts := []string{}
	for _, key := range []string{"removed_repositories", "removed_tags", "removed_images",
		"freed_bytes", "dropped_database"} {
		value, ok := summary[key]
		if !ok {
			continue
		}
		switch key {
		case "freed_bytes":
			if bytes, ok := value.(float64); ok {
				parts = append(parts, fmt.Sprintf("freed %s", humanBytes(int64(bytes))))
				continue
			}
		case "dropped_database":
			parts = append(parts, fmt.Sprintf("dropped the database %v", value))
			continue
		default:
			parts = append(parts, fmt.Sprintf("%s: %v", strings.ReplaceAll(key, "_", " "), value))
		}
	}
	if len(parts) == 0 {
		return "the module finished removing itself"
	}
	return "done — " + strings.Join(parts, ", ")
}

func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for size := value / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}
