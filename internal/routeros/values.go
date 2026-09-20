package routeros

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	uptimeTokenRe = regexp.MustCompile(`(\d+)([wdhms])`)
	uptimeClockRe = regexp.MustCompile(`(\d+):(\d+):(\d+)`)
	sizeRe        = regexp.MustCompile(`^([0-9]*\.?[0-9]+)\s*([kKmMgGtT]?i?[bB])?$`)
)

// ParseUptime converts RouterOS uptime strings into seconds. Both the
// "1w2d03:04:05" and the "1w2d3h4m5s" renderings are accepted.
func ParseUptime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	var total int64
	if m := uptimeClockRe.FindStringSubmatch(s); m != nil {
		hours, _ := strconv.ParseInt(m[1], 10, 64)
		mins, _ := strconv.ParseInt(m[2], 10, 64)
		secs, _ := strconv.ParseInt(m[3], 10, 64)
		total += hours*3600 + mins*60 + secs
		s = uptimeClockRe.ReplaceAllString(s, " ")
	}

	for _, m := range uptimeTokenRe.FindAllStringSubmatch(s, -1) {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		switch m[2] {
		case "w":
			total += n * 7 * 86400
		case "d":
			total += n * 86400
		case "h":
			total += n * 3600
		case "m":
			total += n * 60
		case "s":
			total += n
		}
	}
	return total
}

// ParseSize converts RouterOS size strings into bytes. Plain byte counts
// ("1523712") and suffixed forms ("16.5 MiB", "128 kB") are both accepted,
// because different RouterOS versions and boards render these differently.
func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}

	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}

	mult := float64(1)
	switch strings.ToLower(m[2]) {
	case "kb", "kib":
		mult = 1024
	case "mb", "mib":
		mult = 1024 * 1024
	case "gb", "gib":
		mult = 1024 * 1024 * 1024
	case "tb", "tib":
		mult = 1024 * 1024 * 1024 * 1024
	}
	return int64(v * mult)
}

// ParseInt parses an integer, returning 0 when the value is absent or invalid.
func ParseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ParseBool parses RouterOS booleans ("true"/"false", "yes"/"no").
func ParseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

// ParseOffsetSeconds parses a RouterOS GMT offset such as "+01:00" or "-05:30"
// into seconds. RouterOS renders this as an hours:minutes pair, not as a
// duration, so the plain ":00" suffix would otherwise parse as zero.
func ParseOffsetSeconds(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")

	parts := strings.Split(s, ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0
	}

	multipliers := []int{3600, 60, 1}
	total := 0
	for i, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return 0
		}
		total += n * multipliers[i]
	}
	if neg {
		return -total
	}
	return total
}

// FormatBytes renders a byte count for reports.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + string("KMGTP"[exp]) + "iB"
}
