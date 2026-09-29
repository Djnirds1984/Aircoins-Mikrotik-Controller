package main

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

// TestLoadConfigDefaultsToPort80 pins the shipped default: the panel answers
// on http://<board-ip>/ without a port suffix.
func TestLoadConfigDefaultsToPort80(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("API_TIMEOUT", "")

	_, addr, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if addr != ":80" {
		t.Errorf("default listen address = %q, want %q", addr, ":80")
	}
}

// TestLoadConfigHonoursADDR keeps ADDR as the escape hatch to any other port.
func TestLoadConfigHonoursADDR(t *testing.T) {
	t.Setenv("ADDR", "127.0.0.1:8080")

	_, addr, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if addr != "127.0.0.1:8080" {
		t.Errorf("ADDR override = %q, want %q", addr, "127.0.0.1:8080")
	}
}

// TestLoadConfigRejectsInvalidADDR keeps a typo from reaching the listener.
func TestLoadConfigRejectsInvalidADDR(t *testing.T) {
	t.Setenv("ADDR", "not-an-address")

	if _, _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() accepted an invalid ADDR")
	}
}

// TestSplitListenAddr covers the forms install.sh and the docs use.
func TestSplitListenAddr(t *testing.T) {
	tests := []struct {
		addr string
		host string
		port int
	}{
		{addr: ":80", host: "", port: 80},
		{addr: "80", host: "", port: 80},
		{addr: "0.0.0.0:8080", host: "0.0.0.0", port: 8080},
		{addr: "127.0.0.1:9000", host: "127.0.0.1", port: 9000},
	}
	for _, tc := range tests {
		host, port, err := splitListenAddr(tc.addr)
		if err != nil {
			t.Errorf("splitListenAddr(%q) error = %v", tc.addr, err)
			continue
		}
		if host != tc.host || port != tc.port {
			t.Errorf("splitListenAddr(%q) = (%q, %d), want (%q, %d)",
				tc.addr, host, port, tc.host, tc.port)
		}
	}
}

// TestListenErrorExplainsPrivilegedPorts makes the port 80 failure actionable
// instead of a bare "permission denied".
func TestListenErrorExplainsPrivilegedPorts(t *testing.T) {
	denied := listenError(":80", syscall.EACCES)
	if !errors.Is(denied, syscall.EACCES) {
		t.Errorf("listenError dropped the original error: %v", denied)
	}
	if !strings.Contains(denied.Error(), "CAP_NET_BIND_SERVICE") {
		t.Errorf("listenError hint missing: %v", denied)
	}

	other := errors.New("boom")
	if got := listenError(":80", other); got != other {
		t.Errorf("listenError changed an unrelated failure: %v", got)
	}
}
