package routeros

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// dialStep performs the raw TCP check and reports its timing.
func (p *Prober) dialStep(ctx context.Context, run *prober, opts ProbeOptions) error {
	address := opts.Credentials.Endpoint()

	dialTimeout := 5 * time.Second
	if opts.Timeout > 0 && opts.Timeout < dialTimeout {
		dialTimeout = opts.Timeout
	}

	var (
		failure error
		elapsed time.Duration
	)

	run.check(ctx, "dial", "TCP reachability", func(ctx context.Context) (domain.CheckStatus, string, string) {
		begin := time.Now()
		err := p.DialTCP(ctx, address, dialTimeout)
		elapsed = time.Since(begin)
		if err == nil {
			return domain.StatusPass,
				fmt.Sprintf("Connected to %s in %s", address, elapsed.Round(time.Millisecond)),
				""
		}
		failure = err
		return domain.StatusFail, err.Error(), dialFix(err)
	})

	run.rep.LatencyMS += elapsed.Milliseconds()
	return failure
}

// authStep connects and authenticates, then reports which account was used.
func (p *Prober) authStep(ctx context.Context, run *prober, opts ProbeOptions) (Transport, domain.CheckStatus) {
	var (
		transport Transport
		status    = domain.StatusFail
	)

	run.check(ctx, "api", "API authentication", func(ctx context.Context) (domain.CheckStatus, string, string) {
		t, err := p.Dial(ctx, Options{
			Host:     opts.Credentials.Host,
			Port:     opts.Credentials.Port,
			TLS:      opts.Credentials.TLS,
			Username: opts.Credentials.User,
			Password: opts.Credentials.Password,
			Timeout:  8 * time.Second,
		})
		if err != nil {
			status = domain.StatusFail
			return status, err.Error(), authFix(err)
		}
		transport = t
		status = domain.StatusPass
		scheme := "api"
		if opts.Credentials.TLS {
			scheme = "api-ssl"
		}
		return status, fmt.Sprintf("Authenticated over %s as %q", scheme, opts.Credentials.User), ""
	})

	return transport, status
}

// DeriveResult reduces a list of checks to an overall result: FAIL dominates,
// then WARN, then PASS. SKIP does not affect the outcome.
func DeriveResult(checks []domain.ProbeCheck) domain.CheckStatus {
	result := domain.StatusPass
	for _, c := range checks {
		switch c.Status {
		case domain.StatusFail:
			return domain.StatusFail
		case domain.StatusWarn:
			result = domain.StatusWarn
		}
	}
	return result
}

// finalise reduces the check list to an overall result. A FAIL dominates, then
// WARN, then SKIP, then PASS.
func finalise(rep *domain.ProbeReport) {
	rep.Result = DeriveResult(rep.Checks)
}

// dialFix suggests what to check when the raw connection fails.
func dialFix(err error) string {
	switch {
	case errors.Is(err, ErrTimeout):
		return "The device did not answer. Check that the router is powered and reachable, that /ip service api (or api-ssl) is enabled, and that no firewall rule drops TCP 8728/8729 from this panel."
	case errors.Is(err, ErrUnreachable):
		return "Check the IP address and confirm the device is routed from this panel."
	default:
		return "Verify the address, then confirm the API service is enabled on the router."
	}
}

// authFix suggests what to check when authentication fails.
func authFix(err error) string {
	switch {
	case errors.Is(err, ErrAuth):
		return "Check the API username and password."
	case errors.Is(err, ErrPermission):
		return "The account is valid but restricted. Use an account in the full group, or add the api policy to the group."
	case errors.Is(err, ErrTimeout):
		return "The socket opened but the login timed out. Check router CPU load and the login timeout."
	default:
		return "Confirm /ip service has api (or api-ssl) enabled and reachable from this panel."
	}
}
