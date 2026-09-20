package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/auth"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/httpx"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/store"
)

// minPasswordLength is the shortest password accepted for a panel account.
const minPasswordLength = 10

// handleSetupPage renders the first-run wizard.
func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	count, err := s.admins.Count(r.Context())
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, http.StatusOK, "setup", s.newPage(w, r, "First run setup", "", nil))
}

// handleSetupSubmit creates the first administrator.
func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	count, err := s.admins.Count(r.Context())
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		http.Error(w, "setup has already been completed", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	confirm := r.PostFormValue("confirm")

	fail := func(message string) {
		s.renderPage(w, r, http.StatusBadRequest, "setup",
			s.newPage(w, r, "First run setup", "", map[string]any{
				"Username": username,
				"Error":    message,
			}))
	}

	switch {
	case username == "":
		fail("Choose a username.")
		return
	case len(password) < minPasswordLength:
		fail("Use a password of at least 10 characters.")
		return
	case password != confirm:
		fail("The two passwords do not match.")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		fail("Could not hash the password.")
		return
	}

	admin := &domain.Admin{
		Username:     username,
		PasswordHash: hash,
		Role:         domain.RoleSuperAdmin,
	}
	id, err := s.admins.Create(r.Context(), admin)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail("That username is already taken.")
			return
		}
		s.log.Error("create administrator", "error", err)
		fail("Could not create the administrator.")
		return
	}
	admin.ID = id

	if err := s.startSession(w, r, admin); err != nil {
		s.log.Error("start session after setup", "error", err)
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	s.log.Info("created first administrator", "username", username, "ip", httpx.ClientIP(r))
	setFlash(w, "ok", "Welcome. Add your first router to get started.", s.cfg.CookieSecure)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}
