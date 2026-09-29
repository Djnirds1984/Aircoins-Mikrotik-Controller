package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	adminSessionCookie = "aircoins_admin"
	loginFieldUser     = "username"
	loginFieldPass     = "password"
)

// loginGuard throttles password guesses against the panel login.
//
// It layers two independent limits because either one alone is defeatable:
//
//   - a per-IP token bucket, which stops a single host from spraying
//     thousands of guesses a minute;
//   - a per-account lockout that grows with every failure, which stops a
//     distributed attack (many IPs, one account) that a per-IP limit never
//     notices.
//
// State is in memory: restarting the controller clears the counters. That is
// acceptable here because a restart is also an opportunity for the operator to
// change the password, and the alternative (a DB table per attempt) would let
// an attacker fill the disk.
type loginGuard struct {
	mu sync.Mutex
	// perIP is the shared token bucket, one bucket per client address.
	perIP *ipLimiter
	// failures counts consecutive failures per account name.
	failures map[string]*accountFailures
	// maxAttempts is how many failures an account tolerates before the
	// escalating delay kicks in.
	maxAttempts int
	// baseDelay is the delay after maxAttempts failures; it doubles per
	// further failure up to maxDelay.
	baseDelay time.Duration
	maxDelay  time.Duration
	lastGC    time.Time
}

type accountFailures struct {
	count int
	// last is when the most recent failure happened, used to age the counter
	// out so a user who fixes a typo is not punished forever.
	last time.Time
	// lockedUntil is the instant before which no password is even checked.
	lockedUntil time.Time
}

// defaultLoginGuard returns the production limiter: 10 guesses a minute per
// address, then an account lockout starting at 15 s that doubles to 15 min.
func defaultLoginGuard() *loginGuard {
	return &loginGuard{
		perIP:       newIPLimiter(10, time.Minute),
		failures:    make(map[string]*accountFailures),
		maxAttempts: 5,
		baseDelay:   15 * time.Second,
		maxDelay:    15 * time.Minute,
		lastGC:      time.Now(),
	}
}

// check reports whether an attempt from ip against username may proceed, and
// how long the caller must wait when it may not.
func (g *loginGuard) check(ip, username string) (bool, time.Duration) {
	if g == nil {
		return true, 0
	}
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.gcLocked(now)

	key := normalizeLoginKey(username)
	if f, ok := g.failures[key]; ok && now.Before(f.lockedUntil) {
		return false, time.Until(f.lockedUntil)
	}
	// The per-IP bucket is consulted outside the account check so a flood
	// from one host is cheap even while an account is locked.
	if ok, wait := g.perIP.allow(ip); !ok {
		return false, wait
	}
	return true, 0
}

// fail records a failed attempt against an account and returns how long the
// account is now locked for (zero while it is still under the threshold).
func (g *loginGuard) fail(username string) time.Duration {
	if g == nil {
		return 0
	}
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.gcLocked(now)

	key := normalizeLoginKey(username)
	f, ok := g.failures[key]
	if !ok {
		f = &accountFailures{}
		g.failures[key] = f
	}
	f.count++
	f.last = now
	if f.count < g.maxAttempts {
		return 0
	}
	// Exponential backoff: base, 2x, 4x ... capped at maxDelay.
	delay := g.baseDelay
	for i := g.maxAttempts; i < f.count && delay < g.maxDelay; i++ {
		delay *= 2
	}
	if delay > g.maxDelay {
		delay = g.maxDelay
	}
	f.lockedUntil = now.Add(delay)
	return delay
}

// succeed clears the failure history of an account after a good login, so a
// user who fat-fingers a password twice is not left with a stale lockout.
func (g *loginGuard) succeed(username string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, normalizeLoginKey(username))
}

// gcLocked forgets accounts that have been quiet for a long time. The map
// would otherwise grow once per distinct name an attacker tries.
func (g *loginGuard) gcLocked(now time.Time) {
	if now.Sub(g.lastGC) < time.Minute {
		return
	}
	g.lastGC = now
	cutoff := now.Add(-time.Hour)
	for key, f := range g.failures {
		if f.last.Before(cutoff) {
			delete(g.failures, key)
		}
	}
}

// normalizeLoginKey buckets the account name so "Admin", "admin" and "ADMIN"
// share one counter. An attacker must not get a fresh budget per capitalisation.
func normalizeLoginKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// errLoginLocked is returned by the guard when the account is in lockout.
var errLoginLocked = errors.New("too many failed attempts")

// limitAdminLogin applies the guard, writing 429 when the client must wait.
func (h *Handler) limitAdminLogin(w http.ResponseWriter, r *http.Request, username string) bool {
	ip := clientIP(r)
	ok, wait := h.loginGuard.check(ip, username)
	if ok {
		return true
	}
	if wait < time.Second {
		wait = time.Second
	}
	h.log.Warn("admin login throttled", "remote", ip, "account", normalizeLoginKey(username),
		"retry_after_seconds", int(wait.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
	h.renderLogin(w, r, http.StatusTooManyRequests,
		"Too many failed attempts. Try again in "+humanSeconds(wait)+".")
	return false
}

// humanSeconds renders a wait time for the login form.
func humanSeconds(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 60 {
		return strconv.Itoa(seconds) + "s"
	}
	minutes := (seconds + 59) / 60
	return strconv.Itoa(minutes) + " min"
}
