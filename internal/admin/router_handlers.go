package admin

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/store"
)

// handleRouterList shows the router registry.
func (s *Server) handleRouterList(w http.ResponseWriter, r *http.Request) {
	routers, err := s.routers.List(r.Context())
	if err != nil {
		s.serverError(w, r, "list routers", err)
		return
	}

	view := &routerListView{Routers: routers}
	for _, rt := range routers {
		switch {
		case !rt.Verified:
			view.Unverified++
		case rt.ProbeState == domain.ProbeFail:
			view.Offline++
		default:
			view.Healthy++
		}
	}

	s.renderPage(w, r, http.StatusOK, "routers_list",
		s.newPage(w, r, "Routers", "routers", view))
}

// handleRouterNew renders an empty create form.
func (s *Server) handleRouterNew(w http.ResponseWriter, r *http.Request) {
	view := &routerFormView{
		IsNew: true,
		Router: &domain.Router{
			APIPort:     defaultAPIPort,
			FTPPort:     defaultFTPPort,
			Enabled:     true,
			ProbeState:  domain.ProbeUnknown,
			PortalToken: "",
		},
		AllowWrite: true,
		PanelURL:   s.cfg.PublicBaseURL,
	}
	s.renderPage(w, r, http.StatusOK, "router_form",
		s.newPage(w, r, "Add router", "routers", view))
}

// handleRouterDetail shows a router, its last probe and its cached inventory.
func (s *Server) handleRouterDetail(w http.ResponseWriter, r *http.Request) {
	router, ok := s.routerOr404(w, r)
	if !ok {
		return
	}

	view := &routerDetailView{Router: router, PortalURL: s.portalURLFor(router)}

	if report, err := s.routers.LatestProbe(r.Context(), router.ID); err == nil {
		view.Report = report
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("load latest probe", "router", router.ID, "error", err)
	}

	servers, err := s.routers.ListHotspotServers(r.Context(), router.ID)
	if err != nil {
		s.log.Warn("load hotspot inventory", "router", router.ID, "error", err)
	} else {
		view.Servers = servers
	}

	if router.StubHash == "" {
		view.StubHint = "The portal login stub has not been installed on this router yet."
	}

	s.renderPage(w, r, http.StatusOK, "router_detail",
		s.newPage(w, r, router.DisplayName(), "routers", view))
}

// handleRouterEdit renders the edit form. Secrets are never sent back to the
// browser, so the fields are left blank and mean "keep the stored value".
func (s *Server) handleRouterEdit(w http.ResponseWriter, r *http.Request) {
	router, ok := s.routerOr404(w, r)
	if !ok {
		return
	}
	router.APISecret = ""
	router.FTPSecret = ""

	view := &routerFormView{
		IsNew:      false,
		Router:     router,
		PanelURL:   s.cfg.PublicBaseURL,
		AllowWrite: true,
	}
	s.renderPage(w, r, http.StatusOK, "router_form",
		s.newPage(w, r, "Edit "+router.DisplayName(), "routers", view))
}

// routerOr404 loads the router named by the {id} path value.
func (s *Server) routerOr404(w http.ResponseWriter, r *http.Request) (*domain.Router, bool) {
	id, err := parseID(r, "id")
	if err != nil {
		http.Error(w, "invalid router id", http.StatusBadRequest)
		return nil, false
	}

	router, err := s.routers.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "router not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, "load router", err)
		return nil, false
	}
	return router, true
}

// portalURLFor builds the captive portal URL for a router.
func (s *Server) portalURLFor(router *domain.Router) string {
	if router.PortalToken == "" {
		return ""
	}
	base := s.cfg.PublicBaseURL
	if base == "" {
		return "/p/" + router.PortalToken
	}
	return fmt.Sprintf("%s/p/%s", base, router.PortalToken)
}

// serverError logs and reports an internal failure.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, action string, err error) {
	s.log.Error(action, "error", err, "path", r.URL.Path)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// requireCSRFOr403 validates the CSRF token on a mutating request.
func (s *Server) requireCSRFOr403(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	if !checkCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}
