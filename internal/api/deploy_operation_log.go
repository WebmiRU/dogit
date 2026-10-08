package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ewolf/dogit/internal/store"
)

// The log of one deployment, addressed by the operation itself.
//
// The other log endpoint in this file is addressed by a pipeline number and a job number, because
// the page that uses it is a pipeline's page and a pipeline is what it is looking at. This one is
// for a page whose unit is the operation, and the operation is a job — so it says so and stops
// there. Making a caller who has an operation go and find the pipeline it belonged to is asking it
// to know something it has no reason to know, and the answer changes when a job is retried.
//
// Asked of the core rather than of the module, because the core is what wrote it: every line the
// module narrates is appended here as it is relayed, under this operation's own key. Asking the
// module over the channel for a log the module handed over and the core kept would be the core
// asking a module for its own bookkeeping.

// handleDeployOperationLog reads one operation's log.
//
// Absent rather than empty for a deployment that has said nothing: a page draws "nothing was
// recorded" and a page draws an empty box, and only one of them is true.
func (s *Server) handleDeployOperationLog(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	jobID, err := strconv.ParseInt(r.PathValue("jobID"), 10, 64)
	if err != nil {
		s.writeError(w, r, errBadRequest("a deployment is named by a number"))
		return
	}

	job, err := s.store.Pipelines().JobByID(r.Context(), jobID)
	if err != nil {
		s.writeError(w, r, errNotFoundf("deployment %d does not exist", jobID))
		return
	}
	if job.ProjectID != project.ID {
		// Not "does not exist": a reader on one project must not be able to tell that a
		// deployment of another project's id is real.
		s.writeError(w, r, errNotFoundf("deployment %d does not exist", jobID))
		return
	}
	if job.Deploy == nil {
		// A build is not a deployment, and its log belongs to the pipeline page.
		s.writeError(w, r, errNotFoundf("deployment %d does not exist", jobID))
		return
	}

	entries, err := s.operationLog(r, job)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"job_id":  job.ID,
		"entries": entries,
	})
}

// operationLog reads one operation's log as entries a page can use.
//
// Returned as objects rather than as the raw text because the text is a storage format: it carries
// which stream a line came from and, since this change, when it was written — both of which a page
// wants as fields rather than as something it has to take apart and be wrong about later.
func (s *Server) operationLog(r *http.Request, job *store.Job) ([]map[string]any, error) {
	body, _, err := s.objects.Get(r.Context(), s.jobLogKey(job))
	if err != nil {
		// No log is an ordinary state, not a failure: the deployment may not have begun, or
		// may have begun and said nothing yet.
		return []map[string]any{}, nil
	}
	defer body.Close()

	raw := make([]byte, 0, 64*1024)
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buffer)
		raw = append(raw, buffer[:n]...)
		if readErr != nil {
			break
		}
	}

	return parseLogLines(string(raw)), nil
}

// logEntry is one line of a stored log, as a page is handed it.
type logEntry struct {
	// At is when the line was written, in the core's milliseconds. Nil for a line written
	// before times were recorded — a fact about the writing rather than about the log.
	At *int64 `json:"at,omitempty"`

	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// parseLogLines takes a stored log apart.
//
// Both shapes are understood, and the old one is not an error: logs written as `out| text` exist
// on every instance that ran before this, and reading them as broken would be a lie about data
// that is perfectly good apart from when it was written.
func parseLogLines(text string) []map[string]any {
	entries := []map[string]any{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		entries = append(entries, parseLogEntry(line))
	}
	return entries
}

func parseLogEntry(line string) map[string]any {
	stream := "out"
	body := line

	if rest, found := strings.CutPrefix(line, "err| "); found {
		stream, body = "err", rest
	} else if rest, found := strings.CutPrefix(line, "out| "); found {
		body = rest
	}

	entry := map[string]any{"stream": stream, "text": body}
	// A line with a moment on it. Anything else keeps the whole of its body as the text — a log
	// line that happens to start with digits and a pipe is text, and parsing it as a stamp would
	// take a word out of somebody's deploy output.
	if stamp, text, found := strings.Cut(body, "| "); found {
		if at, err := strconv.ParseInt(strings.TrimSpace(stamp), 10, 64); err == nil {
			entry["at"] = at
			entry["text"] = text
		}
	}
	return entry
}
