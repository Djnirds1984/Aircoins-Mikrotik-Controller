package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/auth"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/store"
)

// handleRouterCreate stores a router that has passed (or been excused from) the
// connection test.
func (s *Server) handleRouterCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRFOr403(w, r) {
		return
	}

	form, err := parseRouterForm(r)
	if err != nil {
		s.renderRouterFormError(w, r, nil, "Could not read the form: "+err.Error(), true)
		return
	}
	if err := form.validate(); err != nil {
		s.renderRouterFormError(w, r, form, err.Error(), true)
		return
	}
	if !form.SkipVerification {
		if err := s.verifyProbeToken(form.ProbeToken, form.Credentials()); err != nil {
			s.renderRouterFormError(w, r, form, err.Error(), true)
			return
		}
	}

	router := form.toRouter()
	router.Verified = !form.SkipVerification
	router.VerifyOverride = form.SkipVerification
	if router.Verified {
		router.ProbeState = domain.ProbePass
	}

	token, err := auth.RandomToken(16)
	if err != nil {
		s.serverError(w, r, "mint portal token", err)
		return
	}
	router.PortalToken = token

	id, err := s.routers.Create(r.Context(), router)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.renderRouterFormError(w, r, form,
				"Another router already uses that name or address.", true)
			return
		}
		s.serverError(w, r, "create router", err)
		return
	}

	s.log.Info("router created", "id", id, "name", form.Name, "host", form.Host, "admin", adminName(r))

	// Persist what was just verified, so the detail page shows the report the
	// save decision was based on, together with the cached device facts. A
	// staged router is not probed, because it may legitimately be unreachable.
	router.ID = id
	if router.Verified {
		s.probeAndStore(r.Context(), router)
	}

	setFlash(w, "ok", "Router added. Next: install the portal login stub and allow the panel in the walled garden.", s.cfg.CookieSecure)
	http.Redirect(w, r, "/admin/routers/"+itoa(id), http.StatusSeeOther)
}

// probeAndStore runs a connection test with the stored credentials and records
// the result. Errors are logged rather than returned: the router is already
// saved, so a failed re-probe only means stale facts.
func (s *Server) probeAndStore(ctx context.Context, router *domain.Router) {
	report, err := s.prober().Probe(ctx, routeros.ProbeOptions{
		Credentials: domain.RouterCredentials{
			Host:     router.Host,
			Port:     router.APIPort,
			TLS:      router.APITLS,
			User:     router.APIUser,
			Password: router.APISecret,
		},
		Timeout:    s.cfg.ProbeTimeout,
		AllowWrite: false,
		RouterID:   router.ID,
		RouterName: router.Name,
	})
	if err != nil {
		s.log.Warn("probe after save", "router", router.ID, "error", err)
		return
	}
	if err := s.routers.SaveProbe(ctx, router.ID, report); err != nil {
		s.log.Warn("save probe after create", "router", router.ID, "error", err)
		return
	}
	s.log.Info("probe after save", "router", router.ID, "result", string(report.Result))
}

// handleRouterUpdate saves an edited router.
func (s *Server) handleRouterUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRFOr403(w, r) {
		return
	}
	if _, ok := s.routerOr404(w, r); !ok {
		return
	}

	form, err := parseRouterForm(r)
	if err != nil {
		s.renderRouterFormError(w, r, nil, "Could not read the form: "+err.Error(), false)
		return
	}
	if err := form.validate(); err != nil {
		s.renderRouterFormError(w, r, form, err.Error(), false)
		return
	}
	if !form.SkipVerification {
		if err := s.verifyProbeToken(form.ProbeToken, form.Credentials()); err != nil {
			s.renderRouterFormError(w, r, form, err.Error(), false)
			return
		}
	}

	router := form.toRouter()
	router.Verified = !form.SkipVerification
	if router.Verified {
		router.ProbeState = domain.ProbePass
	}

	if err := s.routers.Update(r.Context(), router); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.renderRouterFormError(w, r, form,
				"Another router already uses that name or address.", false)
			return
		}
		s.serverError(w, r, "update router", err)
		return
	}

	s.log.Info("router updated", "id", router.ID, "name", router.Name, "admin", adminName(r))
	setFlash(w, "ok", "Router updated.", s.cfg.CookieSecure)
	http.Redirect(w, r, "/admin/routers/"+itoa(router.ID), http.StatusSeeOther)
}

// handleRouterToggle enables or disables management of a router.
func (s *Server) handleRouterToggle(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRFOr403(w, r) {
		return
	}
	router, ok := s.routerOr404(w, r)
	if !ok {
		return
	}

	enabled := !router.Enabled
	if err := s.routers.SetEnabled(r.Context(), router.ID, enabled); err != nil {
		s.serverError(w, r, "toggle router", err)
		return
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.log.Info("router toggled", "id", router.ID, "enabled", enabled, "admin", adminName(r))
	setFlash(w, "ok", router.DisplayName()+" "+state+".", s.cfg.CookieSecure)
	http.Redirect(w, r, "/admin/routers", http.StatusSeeOther)
}

// handleRouterDelete removes a router and everything attached to it.
func (s *Server) handleRouterDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRFOr403(w, r) {
		return
	}
	router, ok := s.routerOr404(w, r)
	if !ok {
		return
	}

	if err := s.routers.Delete(r.Context(), router.ID); err != nil {
		s.serverError(w, r, "delete router", err)
		return
	}

	s.log.Info("router deleted", "id", router.ID, "name", router.Name, "admin", adminName(r))
	setFlash(w, "ok", "Router "+router.DisplayName()+" removed.", s.cfg.CookieSecure)
	http.Redirect(w, r, "/admin/routers", http.StatusSeeOther)
}

// renderRouterFormError re-renders the create or edit form with a message.
func (s *Server) renderRouterFormError(w http.ResponseWriter, r *http.Request, form *routerForm, message string, isNew bool) {
	router := &domain.Router{APIPort: defaultAPIPort, FTPPort: defaultFTPPort, Enabled: true}
	if form != nil {
		router = form.toRouter()
		router.Enabled = form.Enable
	}

	if !isNew && form != nil && form.ID != 0 {
		if stored, err := s.routers.Get(r.Context(), form.ID); err == nil {
			router.PortalToken = stored.PortalToken
			router.Verified = stored.Verified
			router.ProbeState = stored.ProbeState
			router.CreatedAt = stored.CreatedAt
			router.LastProbeAt = stored.LastProbeAt
			router.APISecret = ""
			router.FTPSecret = ""
		}
	}

	title := "Add router"
	if !isNew {
		title = "Edit " + router.DisplayName()
	}

	view := &routerFormView{
		IsNew:      isNew,
		Router:     router,
		Error:      message,
		PanelURL:   s.cfg.PublicBaseURL,
		AllowWrite: form == nil || form.AllowWrite,
	}
	s.renderPage(w, r, http.StatusBadRequest, "router_form", s.newPage(w, r, title, "routers", view))
}

// adminName returns the acting administrator for audit logs.
func adminName(r *http.Request) string {
	if admin := adminFrom(r.Context()); admin != nil {
		return admin.Username
	}
	return "unknown"
}
