package api

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// dummyHash is a valid argon2id hash of an unguessable value. Verifying against
// it when a login does not exist makes the failure path as expensive as the
// success path, so response timing does not disclose which accounts exist.
var dummyHash = mustDummyHash()

func mustDummyHash() []byte {
	hash, err := auth.HashPassword("dogit-timing-equaliser-not-a-real-password")
	if err != nil {
		panic(err)
	}
	return hash
}

type createSSHKeyRequest struct {
	Title string `json:"title"`
	Key   string `json:"key"`
}

func (s *Server) handleListSSHKeys(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	keys, err := s.store.SSHKeys().ListByUser(r.Context(), user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) handleCreateSSHKey(w http.ResponseWriter, r *http.Request) {
	var req createSSHKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Key == "" {
		s.writeError(w, r, errBadRequest("a public key is required"))
		return
	}

	parsed, err := auth.ParsePublicKey(req.Key)
	if err != nil {
		s.writeError(w, r, errBadRequest(err.Error()))
		return
	}

	user := userFrom(r.Context())
	title := req.Title
	if title == "" {
		title = parsed.Comment
	}

	key := &models.SSHKey{
		UserID:      user.ID,
		Title:       title,
		Fingerprint: parsed.Fingerprint,
		PublicKey:   parsed.PEM,
	}
	if err := s.store.SSHKeys().Create(r.Context(), key); err != nil {
		if store.IsUniqueViolation(err) {
			s.writeError(w, r, errConflict("this key is already registered"))
			return
		}
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, map[string]any{"key": key})
}

func (s *Server) handleDeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	keyID, err := uuid.Parse(pathParam(r, "keyID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("invalid key id"))
		return
	}
	user := userFrom(r.Context())
	if err := s.store.SSHKeys().Delete(r.Context(), keyID, user.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusNoContent, nil)
}

type createTokenRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"` // RFC 3339, optional
}

// handleCreateToken issues a personal access token. The plaintext is returned
// exactly once: only its hash is stored.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Name == "" {
		s.writeError(w, r, errBadRequest("a token name is required"))
		return
	}

	scopes := auth.ValidScopes(req.Scopes)
	if len(scopes) == 0 {
		scopes = []string{models.ScopeReadUser, models.ScopeReadRepo, models.ScopeReadAPI}
	}

	token := &models.PersonalAccessToken{
		UserID:    userFrom(r.Context()).ID,
		Name:      req.Name,
		Scopes:    scopes,
		TokenHash: nil,
	}
	if req.ExpiresAt != "" {
		expiry, err := parseTime(req.ExpiresAt)
		if err != nil {
			s.writeError(w, r, errBadRequest("expires_at must be an RFC 3339 timestamp"))
			return
		}
		token.ExpiresAt = &expiry
	}

	plaintext, hash, err := auth.GenerateToken()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	token.TokenHash = hash

	if err := s.store.Tokens().Create(r.Context(), token); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"token": token,
		"value": plaintext,
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.Tokens().ListByUser(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"tokens": tokens})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "tokenID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("invalid token id"))
		return
	}
	if err := s.store.Tokens().Revoke(r.Context(), id, userFrom(r.Context()).ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusNoContent, nil)
}
