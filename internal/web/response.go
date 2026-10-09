package web

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the JSON shape of every error response.
type ErrorBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Details map[string]string `json:"details,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(body)
}

func (s *Server) writeError(w http.ResponseWriter, _ *http.Request, status int, code, message string) {
	var body ErrorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}
