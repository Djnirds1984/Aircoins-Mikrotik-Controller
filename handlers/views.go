package handlers

import (
	"fmt"
	"html/template"
	"math"
	"net/url"
	"strings"
	"time"
)

// TemplatePattern is the glob main.go uses to parse the embedded templates.
const TemplatePattern = "templates/*.html"

// TemplateFuncs returns the helper functions available inside the templates.
func TemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"humanBytes":    humanBytes,
		"humanCount":    humanCount,
		"minutesLabel":  minutesLabel,
		"mbLabel":       mbLabel,
		"durationLabel": durationLabel,
		"timeLabel":     timeLabel,
		"sinceLabel":    sinceLabel,
		"money":         money,
		"statusClass":   routerStatusClass,
		"pct":           percent,
		"portalQuery":   portalQuery,
		"add":           func(a, b int) int { return a + b },
		"sub":           func(a, b int) int { return a - b },
		"seq":           seq,
		"upper":         strings.ToUpper,
		"lower":         strings.ToLower,
		"default":       defaultValue,
	}
}

const noValue = "—"

// humanBytes renders a byte count with binary units.
func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d %s", n, units[0])
	}
	return fmt.Sprintf("%.1f %s", value, units[idx])
}

// humanCount groups thousands so large session counts stay readable.
//
// Templates reach it with an int ({{len .Clients}}) and with an int64 (a SQL
// aggregate); the template language cannot convert between the two, so the
// value arrives as any and is normalised here.
func humanCount(value any) string {
	n, ok := asInt64(value)
	if !ok {
		return noValue
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := fmt.Sprintf("%d", n)
	var out []string
	for len(digits) > 3 {
		out = append([]string{digits[len(digits)-3:]}, out...)
		digits = digits[:len(digits)-3]
	}
	out = append([]string{digits}, out...)
	joined := strings.Join(out, ",")
	if neg {
		return "-" + joined
	}
	return joined
}

// asInt64 normalises the integer kinds a template can hand a helper: int for
// len() and slice sizes, int64 for the counters the stores aggregate in SQL.
func asInt64(value any) (int64, bool) {
	switch n := value.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}

// minutesLabel turns a minute allowance into "1h 30m".
func minutesLabel(minutes int) string {
	if minutes <= 0 {
		return "unlimited"
	}
	hours := minutes / 60
	mins := minutes % 60
	if hours == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// mbLabel renders a data allowance.
func mbLabel(mb int) string {
	if mb <= 0 {
		return "unlimited"
	}
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

// durationLabel renders a Go duration compactly.
func durationLabel(d time.Duration) string {
	if d <= 0 {
		return noValue
	}
	d = d.Round(time.Second)
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// timeLabel renders an optional timestamp in local time.
//
// Templates hold two shapes of timestamp: a *time.Time for nullable columns
// (routers.last_seen_at) and a time.Time for stored values (session rows), so
// accept either instead of forcing a conversion the markup cannot express.
func timeLabel(value any) string {
	t, ok := asTime(value)
	if !ok || t.IsZero() {
		return noValue
	}
	return t.Local().Format("02 Jan 2006 15:04")
}

// sinceLabel renders "3 min ago" style relative times.
func sinceLabel(value any) string {
	t, ok := asTime(value)
	if !ok || t.IsZero() {
		return "never"
	}
	delta := time.Since(t)
	if delta < 0 {
		delta = -delta
	}
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%d min ago", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(delta.Hours()))
	default:
		return fmt.Sprintf("%d d ago", int(delta.Hours()/24))
	}
}

// asTime normalises the timestamp shapes templates pass to timeLabel and
// sinceLabel: a time.Time value, or a *time.Time that may be nil.
func asTime(value any) (time.Time, bool) {
	switch t := value.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	default:
		return time.Time{}, false
	}
}

// money renders cents as a decimal amount.
func money(cents int64) string {
	return fmt.Sprintf("%.2f", float64(cents)/100)
}

// routerStatusClass maps a router status onto a badge class.
func routerStatusClass(status string) string {
	switch status {
	case "online":
		return "badge green"
	case "offline":
		return "badge red"
	default:
		return "badge slate"
	}
}

// percent renders part/total as a rounded percentage.
func percent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(part)/float64(total)*1000) / 10
}

// seq returns 0..n-1, handy for star ratings or print grids.
func seq(n int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// portalQuery rebuilds the MikroTik redirect parameters so the portal POST
// keeps mac, ip, link-login, link-orig and the server identity. Without it a
// voucher POST would lose the context needed to complete the login.
func portalQuery(p portalRequest) string {
	values := url.Values{}
	if p.MAC != "" {
		values.Set("mac", p.MAC)
	}
	if p.IP != "" {
		values.Set("ip", p.IP)
	}
	if p.Username != "" {
		values.Set("username", p.Username)
	}
	if p.LinkLogin != "" {
		values.Set("link-login", p.LinkLogin)
	}
	if p.LinkLoginOnly != "" {
		values.Set("link-login-only", p.LinkLoginOnly)
	}
	if p.LinkOrig != "" {
		values.Set("link-orig", p.LinkOrig)
	}
	if p.ServerName != "" {
		values.Set("server-name", p.ServerName)
	}
	if p.ChapID != "" {
		values.Set("chap-id", p.ChapID)
	}
	if p.ChapChallenge != "" {
		values.Set("chap-challenge", p.ChapChallenge)
	}
	return values.Encode()
}

func defaultValue(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
