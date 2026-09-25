package handlers

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

func TestHotspotInstallerFormValidation(t *testing.T) {
	valid := newHotspotInstallerForm()
	valid.Interface = "bridge1-HS"
	valid.AdminPassword = "secret"
	if !valid.validate() {
		t.Fatalf("valid installer rejected: %v", valid.Errors)
	}
	valid.AddressPool = "10.5.50.300-10.5.50.254"
	if valid.validate() || valid.Errors["address_pool"] == "" {
		t.Fatalf("invalid range accepted: %v", valid.Errors)
	}
	valid = newHotspotInstallerForm()
	valid.Interface = "bridge1-HS"
	valid.AdminPassword = ""
	if valid.validate() || valid.Errors["admin_password"] == "" {
		t.Fatalf("empty administrator password accepted: %v", valid.Errors)
	}
}

func TestHotspotInstallerFollowsRouterOSSetupOrder(t *testing.T) {
	var commands []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "aircoins" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		commands = append(commands, r.Method+" "+r.URL.Path+" "+string(raw))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if r.URL.Path == "/rest/ip/dns" {
				_, _ = w.Write([]byte(`{".id":"*1","servers":"1.1.1.1,8.8.8.8"}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{".id":"*1"}`))
	}))
	defer server.Close()

	host, port := splitStubURL(t, server.URL)
	client := &MikrotikClient{
		router:   database.Router{Host: host, Port: port, Username: "aircoins", Password: "s3cret"},
		endpoint: net.JoinHostPort(host, strconv.Itoa(port)), timeout: 5 * time.Second,
		log: slog.New(slog.DiscardHandler),
	}
	candidate := transportCandidate{mode: database.TransportREST, host: host, port: port}
	client.tp = newRestTransport(client, candidate)

	steps, err := client.InstallHotspot(context.Background(), HotspotInstallSpec{
		Interface: "bridge1-HS", Address: "10.5.50.1/24", AddressPool: "10.5.50.2-10.5.50.254",
		Masquerade: true, DNSServers: "1.1.1.1,8.8.8.8", DNSName: "hotspot.local",
		SMTPServer: "0.0.0.0", ServerName: "hotspot1", ProfileName: "hsprof1",
		PoolName: "hs-pool", AdminUser: "admin", AdminPassword: "secret",
		WalledGardenHost: "routerlogin",
	})
	if err != nil {
		t.Fatalf("InstallHotspot: %v", err)
	}
	if len(steps) != 9 {
		t.Fatalf("completed steps = %d, want 9: %v", len(steps), steps)
	}

	order := []string{
		"PUT /rest/ip/address ", "PUT /rest/ip/pool ", "PUT /rest/ip/dhcp-server ",
		"PUT /rest/ip/firewall/nat ", "PATCH /rest/ip/dns/", "PUT /rest/ip/hotspot/profile ",
		"PUT /rest/ip/hotspot ", "PUT /rest/ip/hotspot/user ", "PUT /rest/ip/hotspot/walled-garden ",
	}
	var writes []string
	for _, command := range commands {
		if strings.HasPrefix(command, "PUT ") || strings.HasPrefix(command, "PATCH /rest/ip/dns/") {
			writes = append(writes, command)
		}
	}
	if len(writes) != len(order) {
		t.Fatalf("write count = %d, want %d: %v", len(writes), len(order), writes)
	}
	for i, want := range order {
		if !strings.HasPrefix(writes[i], want) {
			t.Errorf("write %d = %q, want prefix %q", i, writes[i], want)
		}
	}
	for _, want := range []string{`"address":"10.5.50.1/24"`, `"interface":"bridge1-HS"`, `"src-address":"10.5.50.0/24"`, `"dns-name":"hotspot.local"`, `"profile":"default"`} {
		if !strings.Contains(strings.Join(writes, "\n"), want) {
			t.Errorf("installer writes do not contain %s: %v", want, writes)
		}
	}
}

// installStub serves the reads a second installer run makes: every object the
// run looks for already exists, so only the DNS setting is written again.
func installStub(t *testing.T, commands *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*commands = append(*commands, r.Method+" "+r.URL.Path+" "+string(raw))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if r.URL.Path == "/rest/ip/dns" {
				_, _ = w.Write([]byte(`[{".id":"*1","servers":"1.1.1.1"}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"name":"hotspot1"}]`))
			return
		}
		_, _ = w.Write([]byte(`{".id":"*1"}`))
	}))
}

func installerClient(t *testing.T, server *httptest.Server) *MikrotikClient {
	t.Helper()
	host, port := splitStubURL(t, server.URL)
	client := &MikrotikClient{
		router:   database.Router{Host: host, Port: port, Username: "aircoins", Password: "s3cret"},
		endpoint: net.JoinHostPort(host, strconv.Itoa(port)), timeout: 5 * time.Second,
		log: slog.New(slog.DiscardHandler),
	}
	client.tp = newRestTransport(client, transportCandidate{mode: database.TransportREST, host: host, port: port})
	return client
}

func completeSpec() HotspotInstallSpec {
	return HotspotInstallSpec{
		Interface: "bridge1-HS", Address: "10.5.50.1/24", AddressPool: "10.5.50.2-10.5.50.254",
		Masquerade: true, DNSServers: "1.1.1.1", DNSName: "hotspot.local", SMTPServer: "0.0.0.0",
		ServerName: "hotspot1", ProfileName: "hsprof1", PoolName: "hs-pool",
		AdminUser: "admin", AdminPassword: "secret", WalledGardenHost: "routerlogin",
	}
}

func TestHotspotInstallerReusesExistingObjects(t *testing.T) {
	var commands []string
	server := installStub(t, &commands)
	defer server.Close()

	steps, err := installerClient(t, server).InstallHotspot(context.Background(), completeSpec())
	if err != nil {
		t.Fatalf("InstallHotspot over existing objects: %v", err)
	}
	// The DNS setting has no "exists" state: it is re-applied every run. Every
	// other object must be reused rather than duplicated.
	var writes []string
	for _, command := range commands {
		if strings.HasPrefix(command, "PUT ") || strings.HasPrefix(command, "PATCH ") {
			writes = append(writes, command)
		}
	}
	if len(writes) != 1 || !strings.HasPrefix(writes[0], "PATCH /rest/ip/dns/") {
		t.Fatalf("second run wrote more than the DNS setting: %v", writes)
	}
	if len(steps) != 9 {
		t.Fatalf("steps = %d, want 9: %v", len(steps), steps)
	}
	for _, step := range steps {
		if strings.Contains(step, "DNS servers") {
			continue
		}
		if !strings.Contains(step, "already exists") {
			t.Errorf("step does not report reuse: %q", step)
		}
	}
}

func TestHotspotInstallerKeepsCompletedStepsOnFailure(t *testing.T) {
	var commands []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		commands = append(commands, r.Method+" "+r.URL.Path+" "+string(raw))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if r.URL.Path == "/rest/ip/dns" {
				_, _ = w.Write([]byte(`[{".id":"*1","servers":"1.1.1.1"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Path == "/rest/ip/hotspot/profile" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":400,"message":"failure: not enough permissions"}`))
			return
		}
		_, _ = w.Write([]byte(`{".id":"*1"}`))
	}))
	defer server.Close()

	steps, err := installerClient(t, server).InstallHotspot(context.Background(), completeSpec())
	if err == nil {
		t.Fatal("expected the rejected server profile to fail the install")
	}
	// The hint translates the device prose, so assert on the meaning the
	// operator is shown rather than on RouterOS's raw wording.
	if !strings.Contains(err.Error(), "permission") {
		t.Errorf("error does not explain the refusal: %v", err)
	}
	if len(steps) != 5 {
		t.Fatalf("completed steps = %d, want 5: %v", len(steps), steps)
	}
	last := steps[len(steps)-1]
	if !strings.Contains(last, "DNS servers") {
		t.Errorf("last completed step is not the DNS write: %q", last)
	}
	// The profile write itself is expected and failed. Nothing after it may run.
	for _, command := range commands {
		for _, later := range []string{
			"PUT /rest/ip/hotspot ", "PUT /rest/ip/hotspot/user", "PUT /rest/ip/hotspot/walled-garden",
		} {
			if strings.HasPrefix(command, later) {
				t.Errorf("wrote past the failing step: %q", command)
			}
		}
	}
}
