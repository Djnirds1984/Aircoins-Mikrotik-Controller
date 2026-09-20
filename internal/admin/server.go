// Package admin implements the server rendered administration panel.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/config"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/httpx"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/store"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/webui"
)

// adminContextKey carries the authenticated administrator in the request
// context.
type adminContextKey struct{}

// Deps are the dependencies of the admin server.
type Deps struct {
	Config *config.Config
	Logger *slog.Logger
	DB     *sql.DB
	Key    []byte

	// Dial and DialTCP override the RouterOS transport, which is how a simulated
	// device is injected for demos and tests.
	Dial    func(ctx context.Context, opts routeros.Options) (routeros.Transport, error)
	DialTCP func(ctx context.Context, address string, timeout time.Duration) error
}

// Server serves the admin panel.
type Server struct {
	cfg *config.Config
	log *slog.Logger
	db  *sql.DB
	key []byte

	routers *store.Routers
	admins  *store.Admins

	render *httpx.Renderer
	mux    *http.ServeMux

	dial    func(ctx context.Context, opts routeros.Options) (routeros.Transport, error)
	dialTCP func(ctx context.Context, address string, timeout time.Duration) error

	assets fs.FS
}

// NewServer builds the admin server.
func NewServer(deps Deps) (*Server, error) {
	if deps.Config == nil {
		return nil, errors.New("admin: config is required")
	}
	if deps.DB == nil {
		return nil, errors.New("admin: database is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	assets, err := fs.Sub(webui.FS, "admin")
	if err != nil {
		return nil, fmt.Errorf("admin: locate assets: %w", err)
	}

	renderer, err := httpx.NewRenderer(assets, templateFuncs())
	if err != nil {
		return nil, fmt.Errorf("admin: %w", err)
	}

	dial := deps.Dial
	if dial == nil {
		dial = routeros.Dial
	}
	dialTCP := deps.DialTCP
	if dialTCP == nil {
		dialTCP = routeros.DialTCP
	}

	s := &Server{
		cfg:     deps.Config,
		log:     logger,
		db:      deps.DB,
		key:     deps.Key,
		routers: store.NewRouters(deps.DB, deps.Key),
		admins:  store.NewAdmins(deps.DB),
		render:  renderer,
		mux:     http.NewServeMux(),
		dial:    dial,
		dialTCP: dialTCP,
		assets:  assets,
	}
	s.routes()
	return s, nil
}

// Handler returns the admin handler with its middleware applied.
func (s *Server) Handler() http.Handler {
	// The panel renders only local assets, so a strict policy is possible.
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self'; " +
		"script-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

	return httpx.Chain(s.mux,
		httpx.RequestLog(s.log),
		httpx.Recover(s.log),
		httpx.SecurityHeaders,
		httpx.ContentSecurityPolicy(csp),
		httpx.NoCache,
	)
}

// prober returns a Prober bound to this server's transport overrides.
func (s *Server) prober() *routeros.Prober {
	return &routeros.Prober{Dial: s.dial, DialTCP: s.dialTCP}
}
