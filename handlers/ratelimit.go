package handlers

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ipLimiter is a tiny per-IP token bucket used to keep the captive portal from
// being hammered with voucher guesses. It is intentionally in memory: a
// controller restart clearing the counters is acceptable for this use case.
type ipLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	burst  int
	window time.Duration
	lastGC time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func newIPLimiter(burst int, window time.Duration) *ipLimiter {
	if burst <= 0 {
		burst = 12
	}
	if window <= 0 {
		window = time.Minute
	}
	return &ipLimiter{
		buckets: make(map[string]*bucket),
		burst:   burst,
		window:  window,
		lastGC:  time.Now(),
	}
}

// allow consumes one token for ip and reports whether the request may proceed.
// The second return value is how long the caller should wait before retrying.
func (l *ipLimiter) allow(ip string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	now := time.Now()
	rate := float64(l.burst) / l.window.Seconds()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.gcLocked(now)

	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: float64(l.burst), lastSeen: now}
		l.buckets[ip] = b
	}
	// Refill according to the elapsed time.
	b.tokens += now.Sub(b.lastSeen).Seconds() * rate
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.lastSeen = now
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / rate * float64(time.Second))
		return false, wait
	}
	b.tokens--
	return true, 0
}

// gcLocked drops buckets that have been idle for two windows.
func (l *ipLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < l.window {
		return
	}
	l.lastGC = now
	cutoff := now.Add(-2 * l.window)
	for ip, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, ip)
		}
	}
}

// limitPortal applies the limiter to a portal request, writing a 429 when the
// client has to slow down.
func (h *Handler) limitPortal(w http.ResponseWriter, r *http.Request) bool {
	ok, wait := h.limiter.allow(clientIP(r))
	if ok {
		return true
	}
	if wait < time.Second {
		wait = time.Second
	}
	h.log.Warn("portal login throttled", "remote", clientIP(r), "retry_after", wait)
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
	http.Error(w, "too many login attempts, please wait a moment and try again", http.StatusTooManyRequests)
	return false
}
