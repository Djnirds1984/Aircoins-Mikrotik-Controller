package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// settingsPage backs /admin/settings: the panel's own credentials and the
// security posture of the login form.
type settingsPage struct {
	page
	User database.AdminUser
	// Errors are keyed by form field so the template can show them inline.
	Errors map[string]string
	// MinLength is the shortest password the panel accepts.
	MinLength int
	// SessionCount is how many live logins the credentials currently have.
	SessionCount int
}

// Settings renders the account page. The current password must be supplied to
// change anything, so a stolen session cannot quietly take the panel over.
func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.adminSession(r)
	if !ok {
		http.Redirect(w, r, h.cfg.AdminPath+"/login", http.StatusSeeOther)
		return
	}
	sessions, err := h.db.AdminUsers().CountUserSessions(r.Context(), user.ID)
	if err != nil {
		h.log.Warn("cannot count admin sessions", "error", err)
	}
	h.render(w, r, http.StatusOK, "settings.html", &settingsPage{
		page:         page{Title: "Settings", Nav: "settings", Back: h.cfg.AdminPath + "/settings"},
		User:         user,
		Errors:       map[string]string{},
		MinLength:    database.MinAdminPasswordLength,
		SessionCount: int(sessions),
	})
}

// SettingsCredentials changes the operator name and/or password.
func (h *Handler) SettingsCredentials(w http.ResponseWriter, r *http.Request) {
	user, ok := h.adminSession(r)
	if !ok {
		http.Redirect(w, r, h.cfg.AdminPath+"/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the form", http.StatusBadRequest)
		return
	}

	view := &settingsPage{
		page:      page{Title: "Settings", Nav: "settings", Back: h.cfg.AdminPath + "/settings"},
		User:      user,
		Errors:    map[string]string{},
		MinLength: database.MinAdminPasswordLength,
	}
	if n, err := h.db.AdminUsers().CountUserSessions(r.Context(), user.ID); err == nil {
		view.SessionCount = int(n)
	}

	current := r.PostFormValue("current_password")
	newName := database.NormalizeAdminUsername(r.PostFormValue("username"))
	newPassword := r.PostFormValue("new_password")
	confirm := r.PostFormValue("confirm_password")

	// Re-verify the current password. Without this, anyone who walks up to an
	// unlocked browser could take over the panel permanently.
	if _, valid, err := h.db.AdminUsers().VerifyPassword(r.Context(), user.Username, current); err != nil {
		h.fail(w, r, "verify current password", err)
		return
	} else if !valid {
		view.Errors["current_password"] = "That is not your current password."
		h.render(w, r, http.StatusUnauthorized, "settings.html", view)
		return
	}

	if newName == "" {
		view.Errors["username"] = "The operator name cannot be empty."
	}
	if newPassword == "" {
		view.Errors["new_password"] = "Enter a new password."
	} else if err := database.ValidateAdminPassword(newPassword); err != nil {
		view.Errors["new_password"] = strings.TrimPrefix(err.Error(), "database: ")
	} else if newPassword != confirm {
		view.Errors["confirm_password"] = "The two passwords do not match."
	} else if newPassword == current {
		view.Errors["new_password"] = "The new password is the same as the current one."
	}
	if len(view.Errors) > 0 {
		h.render(w, r, http.StatusBadRequest, "settings.html", view)
		return
	}

	if err := h.db.AdminUsers().SetCredentials(r.Context(), user.ID, newName, newPassword); err != nil {
		if errors.Is(err, database.ErrWeakPassword) {
			view.Errors["new_password"] = strings.TrimPrefix(err.Error(), "database: ")
		} else {
			view.Errors["form"] = err.Error()
		}
		h.render(w, r, http.StatusBadRequest, "settings.html", view)
		return
	}

	// Changing the credentials must kill every other session: a cookie stolen
	// before the change would otherwise survive it. This one is revoked too,
	// so the operator is returned to the login form and signs in again with
	// the new password - proof that the change took effect.
	if err := h.db.AdminUsers().DeleteUserSessions(r.Context(), user.ID); err != nil {
		h.log.Error("cannot revoke sessions after credential change", "error", err)
	}
	h.clearAdminCookie(w)
	h.log.Info("admin credentials changed", "remote", clientIP(r), "account", newName)

	setFlash(w, "Credentials updated. Sign in again with the new password.", "ok")
	http.Redirect(w, r, h.cfg.AdminPath+"/login", http.StatusSeeOther)
}
