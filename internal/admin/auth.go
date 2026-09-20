package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/auth"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/httpx"
)

// sessionTTL is how long a panel login lasts.
const sessionTTL = 12 * time.Hour

// msgAuthFailed is deliberately identical for an unknown user and a wrong
// password, so the login form does not reveal which usernames exist.
const msgAuthFailed = "Incorrect username or password."

// handleLoginPage renders the login form.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	count, err := s.admins.Count(r.Context())
	if err == nil && count == 0 {
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}
	if _, err := s.currentAdmin(r); err == nil {
		http.Redirect(w, r, "/admin/routers", http.StatusSeeOther)
		return
	}

	s.renderPage(w, r, http.StatusOK, "login", s.newPage(w, r, "Sign in", "", nil))
}

// handleLoginSubmit authenticates an administrator.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")

	admin, err := s.admins.GetByUsername(r.Context(), username)
	if err != nil || admin.Disabled || auth.VerifyPassword(password, admin.PasswordHash) != nil {
		s.log.Warn("failed panel login", "username", username, "ip", httpx.ClientIP(r))
		s.renderPage(w, r, http.StatusUnauthorized, "login",
			s.newPage(w, r, "Sign in", "", map[string]any{
				"Username": username,
				"Error":    msgAuthFailed,
			}))
		return
	}

	if err := s.startSession(w, r, admin); err != nil {
		s.log.Error("start session", "error", err)
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}
	if err := s.admins.MarkLogin(r.Context(), admin.ID, time.Now()); err != nil {
		s.log.Warn("record login", "error", err)
	}

	http.Redirect(w, r, "/admin/routers", http.StatusSeeOther)
}

// startSession creates a session row and sets the session cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, admin *domain.Admin) error {
	token, err := auth.RandomToken(32)
	if err != nil {
		return err
	}

	expires := time.Now().UTC().Add(sessionTTL)
	if err := s.admins.CreateSession(r.Context(), admin.ID, token, expires,
		httpx.ClientIP(r), r.UserAgent()); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
	})
	return nil
}

// handleLogout destroys the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !checkCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		if err := s.admins.DeleteSession(r.Context(), cookie.Value); err != nil {
			s.log.Warn("delete session", "error", err)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
