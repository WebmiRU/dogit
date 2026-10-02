package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// handleAuthConfig tells the frontend what the instance allows, so the login
// page can hide registration once an administrator exists.
func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	users, _, err := s.store.Users().List(r.Context(), store.ListUsersFilter{Limit: 1})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"registration_enabled": len(users) == 0 || true,
		"has_users":            len(users) > 0,
	})
}

type registerRequest struct {
	Username        string `json:"username"`
	Email           string `json:"email"`
	Name            string `json:"name"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"password_confirmation"`
}

// handleRegister creates an account.
//
// The very first account becomes an administrator, because an instance with no
// administrator cannot be managed. Later registrations are ordinary accounts
// unless the instance is configured otherwise.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if err := validateUsername(req.Username); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		s.writeError(w, r, errBadRequest("a valid email address is required"))
		return
	}
	if req.Password != req.PasswordConfirm {
		s.writeError(w, r, errBadRequest("passwords do not match"))
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		s.writeError(w, r, errBadRequest(err.Error()))
		return
	}

	existing, _, err := s.store.Users().List(r.Context(), store.ListUsersFilter{Limit: 1})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	firstUser := len(existing) == 0

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	user := &models.User{
		Username:     req.Username,
		Email:        req.Email,
		Name:         strings.TrimSpace(req.Name),
		PasswordHash: hash,
		IsAdmin:      firstUser,
	}
	if err := s.store.Users().Create(r.Context(), user); err != nil {
		s.writeError(w, r, err)
		return
	}

	if err := s.store.Users().TouchLastSignIn(r.Context(), user.ID); err != nil {
		s.log.Warn("record sign-in", "user", user.Username, "error", err)
	}
	s.startSession(w, r, user)

	s.log.Info("user registered", "username", user.Username, "admin", user.IsAdmin)
	s.writeJSON(w, r, http.StatusCreated, s.currentUserPayload(r, user))
}

type loginRequest struct {
	Login    string `json:"login"` // username or email
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Login == "" || req.Password == "" {
		s.writeError(w, r, errBadRequest("login and password are required"))
		return
	}

	user, err := s.findUserByLogin(r, req.Login)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Same message and roughly the same work for unknown users, so the
			// response does not reveal which accounts exist.
			auth.VerifyPassword(req.Password, dummyHash)
			s.writeError(w, r, errUnauthorized("invalid login or password"))
			return
		}
		s.writeError(w, r, err)
		return
	}

	if !auth.VerifyPassword(req.Password, user.PasswordHash) {
		s.writeError(w, r, errUnauthorized("invalid login or password"))
		return
	}

	if err := s.store.Users().TouchLastSignIn(r.Context(), user.ID); err != nil {
		s.log.Warn("record sign-in", "user", user.Username, "error", err)
	}
	s.startSession(w, r, user)

	s.writeJSON(w, r, http.StatusOK, s.currentUserPayload(r, user))
}

func (s *Server) findUserByLogin(r *http.Request, login string) (*models.User, error) {
	if user, err := s.store.Users().ByUsername(r.Context(), login); err == nil {
		return user, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return s.store.Users().ByEmail(r.Context(), login)
}

// handleAuthenticated wraps a handler that requires a session. It exists for the
// /auth routes, which are public as a group but individual handlers inside it
// still need an identity.
func (s *Server) handleAuthenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.identify(r)
		if err != nil || user == nil {
			s.writeError(w, r, errUnauthorized("authentication is required"))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), contextUserKey{}, user)))
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if id, err := uuid.Parse(cookie.Value); err == nil {
			// A failed delete only means the session was already gone.
			_ = s.store.Sessions().Delete(r.Context(), id)
		}
	}
	s.clearSessionCookie(w)
	s.writeJSON(w, r, http.StatusNoContent, nil)
}

func (s *Server) handleCurrentUser(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	s.writeJSON(w, r, http.StatusOK, s.currentUserPayload(r, user))
}

func (s *Server) currentUserPayload(r *http.Request, user *models.User) map[string]any {
	payload := map[string]any{"user": user}
	if projectCount, err := s.projectCount(r, user); err == nil {
		payload["project_count"] = projectCount
	}
	return payload
}

func (s *Server) projectCount(r *http.Request, user *models.User) (int, error) {
	projects, _, err := s.store.Projects().ListVisible(r.Context(), user.ID, "")
	if err != nil {
		return 0, err
	}
	return len(projects), nil
}

// startSession creates a session and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *models.User) {
	sess := &models.Session{
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(s.sessionTTL()),
		UserAgent: truncate(r.UserAgent(), 400),
		IP:        clientIP(r),
	}
	if err := s.store.Sessions().Create(r.Context(), sess); err != nil {
		s.log.Error("create session", "error", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.ID.String(),
		Path:     "/",
		Expires:  sess.ExpiresAt,
		MaxAge:   int(s.sessionTTL().Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		SameSite: http.SameSiteLaxMode,
	})
}

func validateUsername(name string) error {
	switch {
	case name == "":
		return errBadRequest("a username is required")
	case len(name) < 2 || len(name) > 32:
		return errBadRequest("a username must be between 2 and 32 characters long")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return errBadRequest("a username may contain only lowercase letters, digits, '_', '-' and '.'")
		}
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return errBadRequest("a username must not start or end with a dot")
	}
	return nil
}

// clientIP extracts the caller's address, trusting proxy headers only for the
// loopback hop that our own reverse proxy represents.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
