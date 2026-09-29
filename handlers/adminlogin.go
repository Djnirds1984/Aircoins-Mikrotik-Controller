package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// adminLoginPage backs the panel sign-in screen.
type adminLoginPage struct {
	page
	Username  string
	FormError string
	// Next is where to go after a successful login. It is always a path on
	// this host, so it cannot be used as an open redirect.
	Next string
}

// AdminLogin renders the sign-in form.
func (h *Handler) AdminLogin(w http.ResponseWriter, r *http.Request) {
	// An operator who is already signed in has no business on this page.
	if _, ok := h.adminSession(r); ok {
		http.Redirect(w, r, h.cfg.AdminPath+"/", http.StatusSeeOther)
		return
	}
	next := sanitizeNext(r.URL.Query().Get("next"), h.cfg.AdminPath)
	h.render(w, r, http.StatusOK, "login.html", &adminLoginPage{
		page: page{Title: "Sign in", Nav: ""},
		Next: next,
	})
}

// AdminLoginSubmit verifies the credentials and opens a session.
func (h *Handler) AdminLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the login form", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.PostFormValue(loginFieldUser))
	password := r.PostFormValue(loginFieldPass)
	next := sanitizeNext(r.PostFormValue("next"), h.cfg.AdminPath)

	// The rate limit is checked before the password is touched, so a locked
	// account costs the attacker nothing.
	if !h.limitAdminLogin(w, r, username) {
		return
	}

	user, ok, err := h.db.AdminUsers().VerifyPassword(r.Context(), username, password)
	if err != nil {
		h.fail(w, r, "verify admin credentials", err)
		return
	}
	if !ok {
		delay := h.loginGuard.fail(username)
		h.log.Warn("admin login failed", "remote", clientIP(r), "account", normalizeLoginKey(username),
			"locked_for_seconds", int(delay.Seconds()))
		message := "Wrong operator name or password."
		if delay > 0 {
			message = "Too many failed attempts. This account is locked for " + humanSeconds(delay) + "."
		}
		h.renderLogin(w, r, http.StatusUnauthorized, message)
		return
	}

	h.loginGuard.succeed(username)
	token, expires, err := h.db.AdminUsers().CreateSession(r.Context(), user.ID, clientIP(r), h.cfg.AdminSessionTTL)
	if err != nil {
		h.fail(w, r, "create admin session", err)
		return
	}
	h.setAdminCookie(w, token, expires)
	h.log.Info("admin signed in", "remote", clientIP(r), "account", user.Username)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// AdminLogout revokes the session and returns to the login form.
func (h *Handler) AdminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		if err := h.db.AdminUsers().DeleteSession(r.Context(), cookie.Value); err != nil {
			// Losing the revocation still logs this browser out, so warn and
			// carry on rather than trapping the operator in the panel.
			h.log.Error("cannot revoke admin session", "error", err)
		}
	}
	h.clearAdminCookie(w)
	h.log.Info("admin signed out", "remote", clientIP(r))
	http.Redirect(w, r, h.cfg.AdminPath+"/login", http.StatusSeeOther)
}

// setAdminCookie stores the session token.
//
// SameSite=Lax (not Strict) because the operator may follow a link into the
// panel from elsewhere; Strict would silently drop the cookie on that first
// navigation and look like a broken login.
func (h *Handler) setAdminCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.SecureCookies,
	})
}

func (h *Handler) clearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.SecureCookies,
	})
}

// adminSession resolves the signed-in operator, if any.
func (h *Handler) adminSession(r *http.Request) (database.AdminUser, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return database.AdminUser{}, false
	}
	user, err := h.db.AdminUsers().SessionUser(r.Context(), cookie.Value)
	if err != nil {
		return database.AdminUser{}, false
	}
	return user, true
}

// renderLogin re-renders the sign-in form with an error, keeping the operator
// name so it does not have to be retyped.
func (h *Handler) renderLogin(w http.ResponseWriter, r *http.Request, status int, message string) {
	h.render(w, r, status, "login.html", &adminLoginPage{
		page:      page{Title: "Sign in", Nav: ""},
		Username:  strings.TrimSpace(r.PostFormValue(loginFieldUser)),
		FormError: message,
		Next:      sanitizeNext(r.PostFormValue("next"), h.cfg.AdminPath),
	})
}
