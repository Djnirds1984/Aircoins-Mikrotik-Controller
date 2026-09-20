package routeros

import (
	"testing"
	"time"
)

func TestParseUptime(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"empty", "", 0},
		{"seconds", "45s", 45},
		{"minutes", "12m30s", 750},
		{"token form", "3w1d4h22m10s", 3*7*86400 + 86400 + 4*3600 + 22*60 + 10},
		{"clock form", "1w2d03:04:05", 7*86400 + 2*86400 + 3*3600 + 4*60 + 5},
		{"hours only", "5h", 18000},
		{"garbage", "not-a-duration", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseUptime(tc.in); got != tc.want {
				t.Fatalf("ParseUptime(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"1024", 1024},
		{"16.0 MiB", 16 * 1024 * 1024},
		{"128 kB", 128 * 1024},
		{"1.5 GiB", int64(1.5 * 1024 * 1024 * 1024)},
	}
	for _, tc := range tests {
		if got := ParseSize(tc.in); got != tc.want {
			t.Fatalf("ParseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseOffsetSeconds(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"+01:00", 3600},
		{"-05:30", -(5*3600 + 30*60)},
		{"00:00", 0},
		{"+02:00", 7200},
	}
	for _, tc := range tests {
		if got := ParseOffsetSeconds(tc.in); got != tc.want {
			t.Fatalf("ParseOffsetSeconds(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSupportsPAPLogin(t *testing.T) {
	tests := []struct {
		loginBy string
		pap     bool
		chap    bool
	}{
		{"http-pap,http-chap,cookie,trial", true, true},
		{"http-chap,cookie", false, true},
		{"http-pap", true, false},
		{"https,cookie", false, false},
		{"", false, false},
		{" http-pap , cookie ", true, false},
	}
	for _, tc := range tests {
		if got := SupportsPAPLogin(tc.loginBy); got != tc.pap {
			t.Fatalf("SupportsPAPLogin(%q) = %v, want %v", tc.loginBy, got, tc.pap)
		}
		if got := SupportsHTTPChap(tc.loginBy); got != tc.chap {
			t.Fatalf("SupportsHTTPChap(%q) = %v, want %v", tc.loginBy, got, tc.chap)
		}
	}
}

// TestLoginRequestArgsIsWellFormed guards the exact wire format of
// /ip/hotspot/active/login, because an empty attribute makes RouterOS fail with
// "unknown host IP 0.0.0.0".
func TestLoginRequestArgsIsWellFormed(t *testing.T) {
	args := LoginRequest{
		User:     "VOUCHER-1",
		Password: "pw",
		IP:       "10.5.50.23",
		MAC:      "AA:BB:CC:DD:EE:01",
	}.Args()

	want := []string{
		"=user=VOUCHER-1",
		"=password=pw",
		"=ip=10.5.50.23",
		"=mac-address=AA:BB:CC:DD:EE:01",
	}
	if len(args) != len(want) {
		t.Fatalf("got %d args (%v), want %d", len(args), args, len(want))
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, args[i], want[i])
		}
	}

	// Empty values must be omitted entirely.
	minimal := LoginRequest{User: "u", Password: "p", IP: "1.2.3.4"}.Args()
	for _, a := range minimal {
		if a == "=mac-address=" || a == "=domain=" {
			t.Fatalf("empty attribute was emitted: %q", a)
		}
	}
}

func TestDeviceUTC(t *testing.T) {
	clock := Clock{Date: "2026-01-02", Time: "10:00:00", GMTOffsetSeconds: 2 * 3600}
	got, err := deviceUTC(clock)
	if err != nil {
		t.Fatalf("deviceUTC: %v", err)
	}
	want := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("deviceUTC = %s, want %s", got, want)
	}

	if _, err := deviceUTC(Clock{Date: "nonsense"}); err == nil {
		t.Fatal("expected an error for an unparseable date")
	}
}
