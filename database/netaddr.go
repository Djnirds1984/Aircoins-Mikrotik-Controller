package database

import (
	"net/netip"
	"strings"
)

// NormalizeMAC lowercases and strips separators from a MAC address so values
// coming from RouterOS ("AA:BB:CC:DD:EE:FF"), URL parameters and HTML forms can
// be compared reliably.
func NormalizeMAC(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(":", "", "-", "", ".", "", " ", "")
	cleaned := strings.ToLower(replacer.Replace(value))
	if len(cleaned) != 12 {
		return strings.ToLower(value)
	}
	for _, r := range cleaned {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return strings.ToLower(value)
		}
	}
	return cleaned
}

// FormatMAC renders a normalized MAC address in the RouterOS canonical
// colon-separated uppercase form.
func FormatMAC(value string) string {
	normalized := NormalizeMAC(value)
	if len(normalized) != 12 {
		return strings.ToUpper(strings.TrimSpace(value))
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, strings.ToUpper(normalized[i:i+2]))
	}
	return strings.Join(parts, ":")
}

// NormalizeIP canonicalises an IPv4/IPv6 address, returning the trimmed input
// when it cannot be parsed.
func NormalizeIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.String()
	}
	return value
}
