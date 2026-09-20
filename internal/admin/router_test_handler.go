package admin

import (
	"net/http"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
)

// probeReportView is the payload for the probe_report fragment.
type probeReportView struct {
	Report *domain.ProbeReport
	Token  string
	Error  string
	IsNew  bool
	IsDemo bool
	// ShowVerdict renders the "saving enabled / blocked" line. It only makes
	// sense on the create and edit forms, not when displaying a stored report.
	ShowVerdict bool
}

// handleRouterTest runs the connection test for the values currently in the
// form and returns the report fragment. Nothing is persisted, so it is safe to
// press as often as you like.
func (s *Server) handleRouterTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRFOr403(w, r) {
		return
	}

	form, err := parseRouterForm(r)
	if err != nil {
		s.renderPartial(w, http.StatusBadRequest, "probe_report",
			&probeReportView{Error: err.Error(), IsNew: true})
		return
	}

	creds := form.Credentials()
	if creds.Host == "" || creds.User == "" {
		s.renderPartial(w, http.StatusBadRequest, "probe_report", &probeReportView{
			Error: "Enter the router address and API username before running the test.",
			IsNew: form.ID == 0,
		})
		return
	}

	report, err := s.prober().Probe(r.Context(), routeros.ProbeOptions{
		Credentials: creds,
		Timeout:     s.cfg.ProbeTimeout,
		AllowWrite:  form.AllowWrite,
		RouterID:    form.ID,
		RouterName:  form.Name,
	})
	if err != nil {
		s.log.Error("run probe", "error", err)
		s.renderPartial(w, http.StatusInternalServerError, "probe_report",
			&probeReportView{Error: "The connection test could not run: " + err.Error()})
		return
	}

	view := &probeReportView{
		Report:      report,
		IsNew:       form.ID == 0,
		IsDemo:      s.cfg.FakeRouter,
		ShowVerdict: true,
	}

	// A token is only issued when the probe actually reached and authenticated
	// against the device, which is what makes the save gate meaningful. A WARN
	// still counts, because warnings describe configuration advice, not a
	// failure to connect.
	if report.Result != domain.StatusFail {
		view.Token = s.issueProbeTokenFor(creds.Host, creds.Port, creds.TLS, creds.User)
	}

	s.log.Info("router connection test",
		"address", creds.Endpoint(),
		"result", string(report.Result),
		"admin", adminName(r),
	)

	s.renderPartial(w, http.StatusOK, "probe_report", view)
}

// handleRouterProbe re-runs the connection test using the stored credentials and
// records the result. It never mutates router configuration.
func (s *Server) handleRouterProbe(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRFOr403(w, r) {
		return
	}

	router, ok := s.routerOr404(w, r)
	if !ok {
		return
	}

	report, err := s.prober().Probe(r.Context(), routeros.ProbeOptions{
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
		s.serverError(w, r, "probe router", err)
		return
	}

	if err := s.routers.SaveProbe(r.Context(), router.ID, report); err != nil {
		s.serverError(w, r, "save probe result", err)
		return
	}

	if failed := report.Failed(); len(failed) > 0 {
		s.log.Warn("router probe failed", "router", router.ID, "reason", failed[0].Message)
		setFlash(w, "error", "Connection test failed: "+failed[0].Message, s.cfg.CookieSecure)
	} else {
		setFlash(w, "ok", "Connection test passed ("+string(report.Result)+").", s.cfg.CookieSecure)
	}

	http.Redirect(w, r, "/admin/routers/"+itoa(router.ID), http.StatusSeeOther)
}
