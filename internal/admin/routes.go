package admin

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/httpx"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/store"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/version"
)

// routes registers every admin route.
func (s *Server) routes() {
	// Static assets and health probes.
	s.mux.Handle("GET /static/", http.StripPrefix("/static/",
		http.FileServerFS(mustSub(s.assets, "static"))))
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	})

	// Authentication.
	s.mux.HandleFunc("GET /admin/login", s.handleLoginPage)
	s.mux.HandleFunc("POST /admin/login", s.handleLoginSubmit)
	s.mux.HandleFunc("POST /admin/logout", s.requireAuth(s.handleLogout))
	s.mux.HandleFunc("GET /admin/setup", s.handleSetupPage)
	s.mux.HandleFunc("POST /admin/setup", s.handleSetupSubmit)

	// Router registry.
	s.mux.Handle("GET /admin/", s.requireAuth(s.handleDashboard))
	s.mux.Handle("GET /admin/routers", s.requireAuth(s.handleRouterList))
	s.mux.HandleFunc("GET /admin/routers/new", s.requireAuth(s.handleRouterNew))
	s.mux.HandleFunc("POST /admin/routers", s.requireAuth(s.handleRouterCreate))
	s.mux.HandleFunc("POST /admin/routers/test", s.requireAuth(s.handleRouterTest))
	s.mux.HandleFunc("GET /admin/routers/{id}", s.requireAuth(s.handleRouterDetail))
	s.mux.HandleFunc("GET /admin/routers/{id}/edit", s.requireAuth(s.handleRouterEdit))
	s.mux.HandleFunc("POST /admin/routers/{id}", s.requireAuth(s.handleRouterUpdate))
	s.mux.HandleFunc("POST /admin/routers/{id}/probe", s.requireAuth(s.handleRouterProbe))
	s.mux.HandleFunc("POST /admin/routers/{id}/toggle", s.requireAuth(s.handleRouterToggle))
	s.mux.HandleFunc("POST /admin/routers/{id}/delete", s.requireAuth(s.handleRouterDelete))
}

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(fmt.Sprintf("admin: assets: %v", err))
	}
	return sub
}

// requireAuth wraps a handler so it only runs for authenticated admins.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if count, err := s.admins.Count(r.Context()); err == nil && count == 0 {
			http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
			return
		}

		admin, err := s.currentAdmin(r)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), adminContextKey{}, admin)
		next(w, r.WithContext(ctx))
	}
}

// currentAdmin resolves the session cookie to an administrator.
func (s *Server) currentAdmin(r *http.Request) (*domain.Admin, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, store.ErrNotFound
	}
	return s.admins.SessionAdmin(r.Context(), cookie.Value)
}

// adminFrom returns the authenticated administrator from the context.
func adminFrom(ctx context.Context) *domain.Admin {
	admin, _ := ctx.Value(adminContextKey{}).(*domain.Admin)
	return admin
}

// newPage prepares the common page payload.
func (s *Server) newPage(w http.ResponseWriter, r *http.Request, title, nav string, data any) *page {
	return &page{
		Title:   title,
		Nav:     nav,
		Admin:   adminFrom(r.Context()),
		CSRF:    s.csrfToken(w, r),
		Flash:   readFlash(w, r, s.cfg.CookieSecure),
		Version: version.String(),
		Data:    data,
	}
}

// renderPage renders a full page, logging template failures.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, data *page) {
	w.WriteHeader(status)
	if err := s.render.RenderPage(w, name, data); err != nil {
		s.log.Error("render page", "page", name, "error", err)
	}
}

// renderPartial renders a fragment, used by in-page updates.
func (s *Server) renderPartial(w http.ResponseWriter, status int, name string, data any) {
	if err := s.render.RenderPartialStatus(w, status, name, data); err != nil {
		s.log.Error("render partial", "partial", name, "error", err)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.String(),
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}
