package routeros

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// ProbeOptions controls a connection test.
type ProbeOptions struct {
	// Credentials are the endpoint and login being tested. They are never
	// persisted by the probe.
	Credentials domain.RouterCredentials
	// Timeout bounds the entire probe.
	Timeout time.Duration
	// AllowWrite enables the write-permission check, which creates and then
	// deletes a scratch walled-garden entry.
	AllowWrite bool
	// RouterID and RouterName are echoed into the report.
	RouterID   int64
	RouterName string
	// Now allows tests to pin the clock.
	Now func() time.Time
}

// Prober runs connection tests. Both dependencies are injectable so the probe
// ladder can be exercised in tests without hardware.
type Prober struct {
	// Dial opens a RouterOS API connection.
	Dial func(ctx context.Context, opts Options) (Transport, error)
	// DialTCP performs the raw reachability check.
	DialTCP func(ctx context.Context, address string, timeout time.Duration) error
}

// NewProber returns a Prober wired to the real RouterOS API.
func NewProber() *Prober {
	return &Prober{Dial: Dial, DialTCP: DialTCP}
}

// DialTCP checks raw TCP reachability, which separates network problems from
// authentication problems in a probe report. It is exported so callers can
// build their own Prober.
func DialTCP(ctx context.Context, address string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return wrapDialError(address, err)
	}
	return conn.Close()
}

// prober accumulates checks and facts for one probe run.
type prober struct {
	t     Transport
	rep   *domain.ProbeReport
	allow bool
	now   func() time.Time
}

// check runs one step, recording its duration and outcome.
func (p *prober) check(ctx context.Context, id, title string, fn func(context.Context) (domain.CheckStatus, string, string)) {
	start := time.Now()
	status, message, fix := fn(ctx)
	p.rep.Checks = append(p.rep.Checks, domain.ProbeCheck{
		ID:         id,
		Title:      title,
		Status:     status,
		Message:    message,
		Fix:        fix,
		DurationMS: time.Since(start).Milliseconds(),
	})
}

// Probe runs the full connection test ladder and always returns a report: a
// failed step becomes a FAIL row rather than an error, so operators can see
// exactly how far the connection got.
func (p *Prober) Probe(ctx context.Context, opts ProbeOptions) (*domain.ProbeReport, error) {
	if p == nil || p.Dial == nil || p.DialTCP == nil {
		return nil, errors.New("routeros: probe is not configured")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rep := &domain.ProbeReport{
		RouterID:   opts.RouterID,
		RouterName: opts.RouterName,
		Address:    opts.Credentials.Endpoint(),
		ProbedAt:   now(),
	}
	run := &prober{rep: rep, allow: opts.AllowWrite, now: now}

	// Step 1: raw TCP reachability. Everything else depends on it.
	if err := p.dialStep(ctx, run, opts); err != nil {
		finalise(rep)
		return rep, nil
	}

	// Step 2: API authentication.
	transport, authStatus := p.authStep(ctx, run, opts)
	if transport == nil {
		finalise(rep)
		return rep, nil
	}
	defer transport.Close()

	run.t = transport

	// Step 3 onwards: device facts and capability checks.
	if authStatus != domain.StatusFail {
		run.checks(ctx)
	}

	finalise(rep)
	return rep, nil
}
