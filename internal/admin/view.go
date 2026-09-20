package admin

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/auth"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
)

const (
	sessionCookie = "ac_session"
	csrfCookie    = "ac_csrf"
	flashCookie   = "ac_flash"
)

// flashMessage is a one-shot notification rendered on the next page load.
type flashMessage struct {
	Kind    string // "ok" or "error"
	Message string
}

// page is the data every admin template receives through the shared layout.
type page struct {
	Title   string
	Nav     string
	Admin   *domain.Admin
	CSRF    string
	Flash   *flashMessage
	Version string
	Data    any
}

// setFlash stores a message for the next request.
func setFlash(w http.ResponseWriter, kind, message string, secure bool) {
	payload := kind + "|" + message
	cookie := &http.Cookie{
		Name:     flashCookie,
		Value:    payload,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
	http.SetCookie(w, cookie)
}

// readFlash consumes and clears the pending flash message.
func readFlash(w http.ResponseWriter, r *http.Request, secure bool) *flashMessage {
	cookie, err := r.Cookie(flashCookie)
	if err != nil || cookie.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})

	kind, message, found := strings.Cut(cookie.Value, "|")
	if !found {
		return nil
	}
	return &flashMessage{Kind: kind, Message: message}
}

// csrfToken returns the current CSRF token, minting one when absent.
func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookie); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	token, err := auth.RandomToken(24)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int((12 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
	})
	return token
}

// checkCSRF validates a submitted token against the cookie.
func checkCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	submitted := r.PostFormValue("_csrf")
	if submitted == "" {
		submitted = r.Header.Get("X-CSRF-Token")
	}
	return submitted != "" && submitted == cookie.Value
}

// templateFuncs are the helpers available inside templates.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"bytes":        routeros.FormatBytes,
		"dash":         dash,
		"yesNo":        yesNo,
		"listOrDash":   listOrDash,
		"formatTime":   formatTime,
		"formatTimeOr": formatTimeOr,
		"timeAgo":      timeAgo,
		"statusClass":  statusClass,
		"stateClass":   stateClass,
		"lower":        strings.ToLower,
		"upper":        strings.ToUpper,
		"join":         strings.Join,
		"seq":          seq,
		"json":         toJSON,
		"probeView":    probeView,
		"ptrTime":      ptrTime,
	}
}

// probeView wraps a stored report for the shared probe_report fragment.
func probeView(report *domain.ProbeReport) *probeReportView {
	return &probeReportView{Report: report}
}

// ptrTime turns a time into a pointer so timeAgo accepts it.
func ptrTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func listOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ", ")
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func formatTimeOr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// timeAgo renders a coarse relative time, which is what operators want when
// scanning a health column.
func timeAgo(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// statusClass maps a probe check outcome onto a CSS class.
func statusClass(status domain.CheckStatus) string {
	switch status {
	case domain.StatusPass:
		return "ok"
	case domain.StatusWarn:
		return "warn"
	case domain.StatusFail:
		return "fail"
	default:
		return "skip"
	}
}

// stateClass maps a probe state onto a CSS class.
func stateClass(state string) string {
	switch state {
	case domain.ProbePass:
		return "ok"
	case domain.ProbeWarn:
		return "warn"
	case domain.ProbeFail:
		return "fail"
	default:
		return "skip"
	}
}

func seq(n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

// toJSON renders a value as JSON for embedding in a data attribute.
func toJSON(v any) string {
	buf, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(buf)
}

// parseID reads a numeric path value.
func parseID(r *http.Request, key string) (int64, error) {
	raw := r.PathValue(key)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q", raw)
	}
	return id, nil
}
