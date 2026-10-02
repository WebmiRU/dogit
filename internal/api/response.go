package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ewolf/dogit/internal/store"
)

// errorBody is the JSON shape of every error response.
type errorBody struct {
	Error struct {
		Status  int               `json:"-"`
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Details map[string]string `json:"details,omitempty"`
	} `json:"error"`
}

// apiError couples an HTTP status with a machine-readable code, so handlers can
// return errors instead of writing responses.
type apiError struct {
	status  int
	code    string
	message string
	details map[string]string
}

func (e *apiError) Error() string { return e.message }

func newError(status int, code, message string) *apiError {
	return &apiError{status: status, code: code, message: message}
}

// errNotFoundf and friends build errors with a formatted message, keeping the
// user-facing text close to where the failure is understood.
func errNotFoundf(format string, args ...any) *apiError {
	return newError(http.StatusNotFound, "not_found", fmt.Sprintf(format, args...))
}

// Formatted variants keep user-facing messages close to where the failure is
// understood.
func errBadRequestf(format string, args ...any) *apiError {
	return newError(http.StatusBadRequest, "bad_request", fmt.Sprintf(format, args...))
}

func errForbiddenf(format string, args ...any) *apiError {
	return newError(http.StatusForbidden, "forbidden", fmt.Sprintf(format, args...))
}

func errConflictf(format string, args ...any) *apiError {
	return newError(http.StatusConflict, "conflict", fmt.Sprintf(format, args...))
}

func (e *apiError) withDetail(key, value string) *apiError {
	if e.details == nil {
		e.details = map[string]string{}
	}
	e.details[key] = value
	return e
}

var (
	errBadRequest   = func(msg string) *apiError { return newError(http.StatusBadRequest, "bad_request", msg) }
	errUnauthorized = func(msg string) *apiError {
		return newError(http.StatusUnauthorized, "unauthorized", msg)
	}
	errForbidden = func(msg string) *apiError { return newError(http.StatusForbidden, "forbidden", msg) }
	errNotFound  = func(msg string) *apiError { return newError(http.StatusNotFound, "not_found", msg) }
	errConflict  = func(msg string) *apiError { return newError(http.StatusConflict, "conflict", msg) }
	errInternal  = func(msg string) *apiError {
		return newError(http.StatusInternalServerError, "internal_error", msg)
	}
)

// writeJSON sends a JSON response with the given status.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	if body == nil {
		w.WriteHeader(status)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(body); err != nil {
		s.log.Error("write response", "path", r.URL.Path, "error", err)
	}
}

// writeError maps an error to a response, translating store sentinels so
// handlers can return them directly.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *apiError
	switch {
	case errors.As(err, &apiErr):
	case errors.Is(err, store.ErrNotFound):
		apiErr = errNotFound("the requested resource does not exist")
	case errors.Is(err, store.ErrConflict):
		apiErr = errConflict("the resource already exists")
	default:
		// Anything unmapped is a bug: log it with detail, return nothing useful.
		s.log.Error("unhandled error", "path", r.URL.Path, "error", err)
		apiErr = errInternal("an unexpected error occurred")
	}

	var body errorBody
	body.Error.Status = apiErr.status
	body.Error.Code = apiErr.code
	body.Error.Message = apiErr.message
	body.Error.Details = apiErr.details

	s.writeJSON(w, r, apiErr.status, body)
}

// decodeJSON reads a JSON request body with a size limit and strict field
// checking, so a typo in the client is reported instead of silently ignored.
func decodeJSON(r *http.Request, dst any) error {
	const maxBody = 1 << 20 // 1 MiB is plenty for our payloads

	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return errBadRequest("request body is not valid JSON: " + err.Error())
	}
	return nil
}

// pathParam returns a URL path parameter.
func pathParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// refParam returns a branch or tag name taken from the path, decoded.
//
// A slash is legal inside a branch name, so every client sends "feature/thing"
// percent-encoded. chi hands the segment back exactly as it appeared on the wire,
// so without decoding the handler sees "feature%2Fthing" and rejects a name that
// was perfectly valid.
func refParam(r *http.Request, name string) (string, error) {
	raw := pathParam(r, name)
	if raw == "" {
		return "", nil
	}

	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid reference name", name)
	}
	return strings.TrimSpace(decoded), nil
}

func queryInt(r *http.Request, name string, def, max int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
