package faketos

import (
	"fmt"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
)

// rows builds a !re reply from the given sentences.
func rows(items ...map[string]string) *routeros.Reply {
	return &routeros.Reply{Re: items, Done: map[string]string{}}
}

// empty builds a reply with no !re sentences, which is what a successful
// mutating command returns.
func empty() *routeros.Reply { return rows() }

// filterRows applies RouterOS "?key=value" query words.
func filterRows(items []map[string]string, args []string) []map[string]string {
	filters := map[string]string{}
	for _, a := range args {
		if !strings.HasPrefix(a, "?") {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimPrefix(a, "?"), "=")
		filters[key] = value
	}
	if len(filters) == 0 {
		return items
	}

	var out []map[string]string
	for _, item := range items {
		match := true
		for k, v := range filters {
			if item[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, item)
		}
	}
	return out
}

// argValue reads a "=key=value" API word.
func argValue(args []string, key string) string {
	prefix := "=" + key + "="
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}

func (d *Device) resourceRow() map[string]string {
	return map[string]string{
		".id":               "*1",
		"uptime":            "3w1d4h22m10s",
		"version":           d.opts.ROSVersion,
		"build-time":        "2025-01-15 12:00:00",
		"free-memory":       "86425600",
		"total-memory":      "134217728",
		"cpu-count":         "4",
		"cpu":               "ARMv7",
		"free-hdd-space":    fmt.Sprintf("%d", d.opts.FreeHDD),
		"total-hdd-space":   "16777216",
		"board-name":        d.opts.BoardName,
		"architecture-name": d.opts.Arch,
		"platform":          "MikroTik",
	}
}

// clockRow reports a local clock reading. The GMT offset is fixed at zero and
// the wall clock is shifted, so the probe observes the requested skew.
func (d *Device) clockRow() map[string]string {
	now := time.Now().UTC().Add(time.Duration(d.opts.ClockOffsetHours) * time.Hour)
	return map[string]string{
		".id":            "*1",
		"date":           now.Format("2006-01-02"),
		"time":           now.Format("15:04:05"),
		"time-zone-name": "UTC",
		"gmt-offset":     "00:00",
	}
}
