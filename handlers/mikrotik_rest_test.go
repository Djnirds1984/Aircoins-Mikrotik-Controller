package handlers

import (
	"context"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// restStub is a fake RouterOS REST endpoint. It answers the handful of shapes
// the translation layer produces, so the mapping is tested against real HTTP
// instead of a mock of our own function.
type restStub struct {
	server   *httptest.Server
	requests []*http.Request
	bodies   []string
}

func newRestStub(t *testing.T) *restStub {
	t.Helper()
	stub := &restStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		stub.requests = append(stub.requests, r)
		stub.bodies = append(stub.bodies, string(raw))

		user, pass, ok := r.BasicAuth()
		if !ok || user != "aircoins" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":401,"message":"Unauthorized"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/system/identity":
			_, _ = w.Write([]byte(`{"name":"Tolosa","version":"7.16.2"}`))
		case r.URL.Path == "/rest/system/resource":
			_, _ = w.Write([]byte(`{"version":"7.16.2","board-name":"CHR","cpu":"ARM","free-memory":134217728,"total-memory":268435456}`))
		case r.URL.Path == "/rest/ip/hotspot/active" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"user":"dave","address":"10.0.0.5","uptime":"02:30:00",` +
				`"bytes-in":1048576,"bytes-out":2097152,"login-by":"voucher"}]`))
		case r.URL.Path == "/rest/interface/print":
			_, _ = w.Write([]byte(`[{"name":"ether1","active":true,"rx-byte":1024,"tx-byte":2048,` +
				`"rx-packets-per-second":10,"tx-packets-per-second":20,` +
				`"rx-bits-per-second":300,"tx-bits-per-second":400}]`))
		case r.URL.Path == "/rest/interface/monitor-traffic" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"name":"ether1","rx-byte":1024,"tx-byte":2048,` +
				`"rx-packets-per-second":10,"tx-packets-per-second":20,` +
				`"rx-bits-per-second":300,"tx-bits-per-second":400}`))
		case r.URL.Path == "/rest/ip/hotspot/user/add" && r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"name":"voucher-7","profile":"vouchers"}`))
		case r.URL.Path == "/rest/nope":
			// Simulates a menu that refuses the CRUD verb, which must be
			// retried in the universal POST <menu>/<command> form.
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = w.Write([]byte(`{"error":406,"message":"Not Acceptable"}`))
		case r.URL.Path == "/rest/nope/print" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`[{"name":"fallback"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			// RouterOS answers an unknown path with a plain "not found", which is
			// what sentinelForStatus has to recognise.
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// restTestClient builds a client wired to the stub, bypassing the real dialer.
func restTestClient(t *testing.T, stub *restStub) *MikrotikClient {
	t.Helper()
	host, port := splitStubURL(t, stub.server.URL)
	client := &MikrotikClient{
		router:   database.Router{Host: host, Port: port, Username: "aircoins", Password: "s3cret"},
		endpoint: net.JoinHostPort(host, strconv.Itoa(port)),
		timeout:  5 * time.Second,
		log:      slog.New(slog.DiscardHandler),
	}
	candidate := transportCandidate{mode: database.TransportREST, host: host, port: port}
	client.tp = newRestTransport(client, candidate)
	client.transportName = candidate.mode
	return client
}

// splitStubURL turns an httptest URL into a host and a port.
func splitStubURL(t *testing.T, raw string) (string, int) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse stub url %q: %v", raw, err)
	}
	host, portStr, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split stub url %q: %v", raw, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

func TestRestTransportConnectAndIdentity(t *testing.T) {
	stub := newRestStub(t)
	client := restTestClient(t, stub)
	defer client.Close()

	ctx := context.Background()
	if err := client.tp.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Connect is idempotent: a second call must not re-probe the credentials.
	before := len(stub.requests)
	if err := client.tp.Connect(ctx); err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	if got := len(stub.requests); got != before {
		t.Errorf("verified session re-probed: %d extra requests", got-before)
	}

	reply, err := client.Run(ctx, "/system/identity/print")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	row := reply.First()
	if row == nil {
		t.Fatal("identity returned no rows")
	}
	if row["name"] != "Tolosa" || row["version"] != "7.16.2" {
		t.Errorf("identity row = %#v", row)
	}
	if _, err := client.DeviceInfo(ctx); err != nil {
		t.Fatalf("DeviceInfo over REST: %v", err)
	}
}

func TestRestTransportActiveClients(t *testing.T) {
	stub := newRestStub(t)
	client := restTestClient(t, stub)
	defer client.Close()

	// The controller reads active sessions through this method; it must work
	// unchanged over REST.
	clients, err := client.ActiveHotspotClients(context.Background())
	if err != nil {
		t.Fatalf("ActiveHotspotClients: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1", len(clients))
	}
	if clients[0].User != "dave" || clients[0].Address != "10.0.0.5" {
		t.Errorf("client = %#v", clients[0])
	}
	if clients[0].BytesIn != 1048576 || clients[0].BytesOut != 2097152 {
		t.Errorf("byte counters not decoded: %#v", clients[0])
	}
}

func TestRestTransportCommandTranslation(t *testing.T) {
	cases := []struct {
		name       string
		command    string
		args       []string
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{"print becomes a GET", "/ip/hotspot/user/print", nil,
			http.MethodGet, "/rest/ip/hotspot/user", ""},
		{"filters become query params", "/ip/hotspot/user/print", []string{"?name=dave"},
			http.MethodGet, "/rest/ip/hotspot/user", ""},
		{"add becomes a PUT", "/ip/hotspot/user/add", []string{"=name=voucher-7"},
			http.MethodPut, "/rest/ip/hotspot/user", `{"name":"voucher-7"}`},
		{"set becomes a PATCH on the id", "/ip/hotspot/user/set", []string{"=.id=*1", "=limit-uptime=1h"},
			http.MethodPatch, "/rest/ip/hotspot/user/*1", `{"limit-uptime":"1h"}`},
		{"remove becomes a DELETE", "/ip/hotspot/user/remove", []string{"=.id=*1"},
			http.MethodDelete, "/rest/ip/hotspot/user/*1", ""},
		{"a console command stays a POST", "/ip/hotspot/active/login",
			[]string{"=user=dave", "=password=x"}, http.MethodPost, "/rest/ip/hotspot/active/login",
			// The body is a Go map, so encoding/json sorts the keys.
			`{"password":"x","user":"dave"}`},
		{"an id filter becomes a command POST", "/ip/hotspot/user/print", []string{"=.id=*1"},
			http.MethodPost, "/rest/ip/hotspot/user/print", `{".id":"*1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newRestStub(t)
			client := restTestClient(t, stub)
			defer client.Close()

			// The stub only knows a few menus, so the assertion is on the request
			// that reached the device; an unmapped path still yields that request.
			// The last request is the command under test: the first one is always
			// the credential probe Connect issues before it.
			_, _ = client.Run(context.Background(), tc.command, tc.args...)
			if len(stub.requests) < 2 {
				t.Fatal("no command request reached the device")
			}
			got := stub.requests[len(stub.requests)-1]
			body := stub.bodies[len(stub.bodies)-1]
			if got.Method != tc.wantMethod {
				t.Errorf("method = %s, want %s", got.Method, tc.wantMethod)
			}
			if got.URL.Path != tc.wantPath {
				t.Errorf("path = %s, want %s", got.URL.Path, tc.wantPath)
			}
			switch {
			case tc.wantBody == "" && body != "":
				t.Errorf("body = %q, want empty", body)
			case tc.wantBody != "" && body != tc.wantBody:
				t.Errorf("body = %s, want %s", body, tc.wantBody)
			}
		})
	}
}

func TestRestTransportFallsBackToCommandForm(t *testing.T) {
	stub := newRestStub(t)
	client := restTestClient(t, stub)
	defer client.Close()

	// A menu that refuses the CRUD verb has to be retried as POST <menu>/<verb>.
	reply, err := client.Run(context.Background(), "/nope/print")
	if err != nil {
		t.Fatalf("print over the command fallback: %v", err)
	}
	if row := reply.First(); row == nil || row["name"] != "fallback" {
		t.Fatalf("fallback reply = %#v", reply)
	}
	// The first request is the credential probe, then the GET the stub refuses
	// and the POST that replaces it.
	if len(stub.requests) != 3 {
		t.Fatalf("expected a probe, a GET then a POST, got %d requests", len(stub.requests))
	}
	if stub.requests[1].Method != http.MethodGet || stub.requests[2].Method != http.MethodPost {
		t.Errorf("fallback methods = %s then %s",
			stub.requests[1].Method, stub.requests[2].Method)
	}
	if stub.requests[2].URL.Path != "/rest/nope/print" {
		t.Errorf("fallback path = %s, want /rest/nope/print", stub.requests[2].URL.Path)
	}
}

func TestRestTransportTrafficCounters(t *testing.T) {
	stub := newRestStub(t)
	client := restTestClient(t, stub)
	defer client.Close()

	// "=.sum" has no REST equivalent, so the rates are merged from
	// /interface/monitor-traffic under the property names the templates read.
	reply, err := client.Run(context.Background(),
		"/interface/print", "=.proplist=name", "=.sum rx-byte,tx-byte")
	if err != nil {
		t.Fatalf("interface print with counters: %v", err)
	}
	row := reply.First()
	if row == nil {
		t.Fatal("interface print returned no rows")
	}
	if row["rx-byte"] != "1024" || row["tx-byte"] != "2048" {
		t.Errorf("byte counters = %q/%q", row["rx-byte"], row["tx-byte"])
	}
	if row["rx-packet"] != "10" || row["tx-packet"] != "20" {
		t.Errorf("packet counters = %q/%q", row["rx-packet"], row["tx-packet"])
	}
	if row["rx-rate"] != "300" || row["tx-rate"] != "400" {
		t.Errorf("rate counters = %q/%q", row["rx-rate"], row["tx-rate"])
	}
}

func TestRestTransportErrorMapping(t *testing.T) {
	t.Run("bad credentials", func(t *testing.T) {
		stub := newRestStub(t)
		client := restTestClient(t, stub)
		defer client.Close()
		client.router.Password = "wrong"

		_, err := client.Run(context.Background(), "/system/identity/print")
		if !errors.Is(err, ErrRouterAuth) {
			t.Fatalf("error = %v, want ErrRouterAuth", err)
		}
		// The hint must mention credentials rather than the port.
		if !strings.Contains(routerErrorHint(err), "password") {
			t.Errorf("hint = %q, want it to mention the password", routerErrorHint(err))
		}
	})

	t.Run("unknown menu", func(t *testing.T) {
		stub := newRestStub(t)
		client := restTestClient(t, stub)
		defer client.Close()

		_, err := client.Run(context.Background(), "/does/not/exist/print")
		if !errors.Is(err, ErrRouterNotFound) {
			t.Fatalf("error = %v, want ErrRouterNotFound", err)
		}
	})

	t.Run("closed port", func(t *testing.T) {
		stub := newRestStub(t)
		client := restTestClient(t, stub)
		stub.server.Close()

		_, err := client.Run(context.Background(), "/system/identity/print")
		if !errors.Is(err, ErrRouterUnreachable) && !errors.Is(err, ErrRouterTimeout) {
			t.Fatalf("error = %v, want a connection failure sentinel", err)
		}
	})

	t.Run("plain http against a tls listener", func(t *testing.T) {
		// https to a plaintext server (or the reverse) must be reported, not hang.
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()
		host, port := splitStubURL(t, server.URL)
		client := &MikrotikClient{
			router:   database.Router{Host: host, Port: port, Username: "a", Password: "b"},
			endpoint: net.JoinHostPort(host, strconv.Itoa(port)),
			timeout:  3 * time.Second,
			log:      slog.New(slog.DiscardHandler),
		}
		// tls=false against the TLS listener is the plain-HTTP misconfiguration.
		client.tp = newRestTransport(client, transportCandidate{
			mode: database.TransportREST, host: host, port: port, tls: false,
		})
		defer client.Close()

		if err := client.tp.Connect(context.Background()); err == nil {
			t.Fatal("expected the plain http request against https to fail")
		}
	})
}

func TestRestTranslateUnit(t *testing.T) {
	cases := []struct {
		command  string
		args     []string
		method   string
		path     string
		queryKey string
	}{
		{"/interface/print", nil, http.MethodGet, "/interface", ""},
		{"/ip/pool/print", []string{"?name=dave"}, http.MethodGet, "/ip/pool", "name"},
		{"/ip/dns/print", []string{"=.proplist=name"}, http.MethodPost, "/ip/dns/print", ""},
		{"/ip/dns/set", []string{"=.id=*3", "=disabled=yes"}, http.MethodPatch, "/ip/dns/*3", ""},
		{"/ip/dns/remove", []string{"=.id=*3"}, http.MethodDelete, "/ip/dns/*3", ""},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got, err := translateCommand(tc.command, tc.args)
			if err != nil {
				t.Fatalf("translate: %v", err)
			}
			if got.method != tc.method || got.path != tc.path {
				t.Errorf("got %s %s, want %s %s", got.method, got.path, tc.method, tc.path)
			}
			if tc.queryKey != "" && got.query.Get(tc.queryKey) == "" {
				t.Errorf("query %q missing from %v", tc.queryKey, got.query)
			}
		})
	}

	// A set or remove without an id cannot be expressed in REST, and must fail
	// loudly instead of issuing a destructive request against the whole menu.
	if _, err := translateCommand("/ip/dns/set", []string{"=disabled=yes"}); err == nil {
		t.Error("set without .id should be rejected")
	}
	if _, err := translateCommand("/ip/dns/remove", nil); err == nil {
		t.Error("remove without .id should be rejected")
	}
}

func TestParseRESTReply(t *testing.T) {
	reply, err := parseRESTReply([]byte(`[{"name":"a","disabled":true,"limit":30,"mac":"AA:BB:CC:DD:EE:FF"}]`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	row := reply.First()
	if row["name"] != "a" {
		t.Errorf("name = %q", row["name"])
	}
	// RouterOS sends strings, but booleans and numbers do appear; everything has
	// to arrive as text because the rest of the package reads string rows.
	if row["disabled"] != "true" {
		t.Errorf("bool not stringified: %q", row["disabled"])
	}
	if row["limit"] != "30" {
		t.Errorf("number not stringified: %q", row["limit"])
	}

	if empty, err := parseRESTReply(nil); err != nil || len(empty.Re) != 0 {
		t.Errorf("empty body = %#v, err = %v", empty, err)
	}
	if _, err := parseRESTReply([]byte(`<html>not json</html>`)); err == nil {
		t.Error("a non JSON answer should be reported")
	}
}

func TestRestCandidateSelection(t *testing.T) {
	// Auto mode is secure-only and must never send credentials to HTTP port 80.
	router := database.Router{Host: "10.0.0.1", Port: 8728}
	got := transportCandidates(router)
	if len(got) != 3 {
		t.Fatalf("auto mode produced %d candidates, want 3", len(got))
	}
	want := []string{database.TransportRESTSsl, database.TransportAPISSL, database.TransportAPI}
	for index, mode := range want {
		if got[index].mode != mode {
			t.Errorf("auto candidate %d = %s, want %s", index, got[index].mode, mode)
		}
		if got[index].port == 80 {
			t.Errorf("auto mode probes plain HTTP on port 80 (%s)", got[index].mode)
		}
	}

	// A remembered HTTP REST success must not override the auto-mode safeguard.
	router.LastTransport = database.TransportREST
	got = transportCandidates(router)
	for _, candidate := range got {
		if candidate.mode == database.TransportREST {
			t.Fatal("auto mode reused the remembered plain HTTP REST transport")
		}
	}

	// A router that answered securely over the API is tried there first next time.
	router.LastTransport = database.TransportAPISSL
	got = transportCandidates(router)
	if got[0].mode != database.TransportAPISSL {
		t.Errorf("last secure transport not honoured: first candidate is %s", got[0].mode)
	}

	// An explicit mode yields exactly one candidate, with the right default port.
	router.Transport = database.TransportRESTSsl
	got = transportCandidates(router)
	if len(got) != 1 || got[0].mode != database.TransportRESTSsl || got[0].port != 443 {
		t.Errorf("explicit rest-ssl = %#v", got)
	}
	// HTTP remains available only with an explicit port; omitted means invalid.
	router.Transport = database.TransportREST
	got = transportCandidates(router)
	if len(got) != 1 || got[0].mode != database.TransportREST || got[0].port != 0 {
		t.Errorf("explicit plain REST without a port = %#v", got)
	}
	// A port the operator typed explicitly - including 80 - is passed through
	// and dialled; the controller must not refuse it outright.
	router.Transport = database.TransportREST
	router.RestPort = 80
	got = transportCandidates(router)
	if len(got) != 1 || got[0].mode != database.TransportREST || got[0].port != 80 {
		t.Fatalf("explicit plain REST on 80 = %#v", got)
	}
	_, port80Err := DialRouter(context.Background(), router, time.Second, slog.New(slog.DiscardHandler))
	if port80Err != nil && strings.Contains(port80Err.Error(), "never chosen automatically") {
		t.Fatalf("dialer refused an explicitly typed port 80: %v", port80Err)
	}
	router.Transport = database.TransportREST
	router.RestPort = 8080
	got = transportCandidates(router)
	if len(got) != 1 || got[0].port != 8080 || got[0].tls {
		t.Errorf("explicit rest on 8080 = %#v", got)
	}

	// The dialer itself must also reject an old database row that selected HTTP
	// REST before rest_port became mandatory. It must not make a network request.
	_, err := DialRouter(context.Background(), database.Router{
		Host: "remote.oxapsph.com", Port: 8728, Username: "aircoins", Password: "secret",
		Transport: database.TransportREST,
	}, time.Second, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("plain REST without a web port unexpectedly connected")
	}
	if strings.Contains(err.Error(), "remote.oxapsph.com:80") {
		t.Fatalf("dialer still contacted or named port 80: %v", err)
	}
	if !strings.Contains(err.Error(), "explicit web port") {
		t.Fatalf("error = %v, want an explicit-port explanation", err)
	}

}

// TestRestEndToEndOverTheWebForm drives the whole path an operator uses: add a
// router through POST /routers with transport=rest, then open its page and read
// live data. The device is the REST stub, so this proves the form field, the
// database column and the dialer are wired to each other.
func TestRestEndToEndOverTheWebForm(t *testing.T) {
	stub := newRestStub(t)
	host, port := splitStubURL(t, stub.server.URL)

	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{
		Path:          filepath.Join(t.TempDir(), "e2e.db"),
		SecretKeyPath: filepath.Join(t.TempDir(), "secret.key"),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	tpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	h := New(db, tpl, Config{APITimeout: 5 * time.Second})
	server := httptest.NewServer(h.Routes())
	defer server.Close()

	// One client with a cookie jar, exactly like a browser: the CSRF double
	// submit check needs the token cookie and the form field to match.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	browser := &http.Client{Jar: jar}

	// Register the router over REST only: the stub serves no API port at all, so
	// a controller that still spoke the legacy API could not answer this form.
	form := url.Values{
		"name": {"REST-only"}, "host": {host}, "port": {strconv.Itoa(port)},
		"username": {"aircoins"}, "password": {"s3cret"},
		"transport": {database.TransportREST}, "rest_port": {strconv.Itoa(port)},
		"default_portal": {"1"},
	}
	listPage := getBody(t, browser, server.URL+"/routers")
	form.Set("csrf_token", csrfOf(t, listPage))
	resp, err := browser.PostForm(server.URL+"/routers", form)
	if err != nil {
		t.Fatalf("POST /routers: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create router status %d", resp.StatusCode)
	}

	routers, err := db.Routers().List(ctx)
	if err != nil {
		t.Fatalf("list routers: %v", err)
	}
	if len(routers) != 1 {
		t.Fatalf("stored %d routers, want 1", len(routers))
	}
	stored := routers[0]
	if stored.Transport != database.TransportREST {
		t.Errorf("stored transport = %q, want %q", stored.Transport, database.TransportREST)
	}
	if stored.RestPort != port {
		t.Errorf("stored rest port = %d, want %d", stored.RestPort, port)
	}

	// The device page must render live data, not the cached fallback.
	detail := getBody(t, browser, server.URL+"/routers/1")
	for _, want := range []string{"REST-only", "dave", "10.0.0.5"} {
		if !strings.Contains(detail, want) {
			t.Errorf("router page is missing %q; the REST dial probably failed", want)
		}
	}
	if strings.Contains(detail, "Showing locally cached data") {
		t.Error("router page fell back to cached data although REST answered")
	}

	// The winning transport is remembered, so the next load skips the probe.
	reloaded, err := db.Routers().Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("reload router: %v", err)
	}
	if reloaded.LastTransport != database.TransportREST {
		t.Errorf("last transport = %q, want %q", reloaded.LastTransport, database.TransportREST)
	}

	// The JSON API exposes the same settings.
	body := getBody(t, browser, server.URL+"/api/v1/routers/1")
	if !strings.Contains(body, `"transport":"rest"`) {
		t.Errorf("API response does not carry the transport: %s", body)
	}
	if !strings.Contains(body, `"last_transport":"rest"`) {
		t.Errorf("API response does not carry the negotiated transport: %s", body)
	}
}

// getBody fetches a URL and fails the test unless it answers 200.
func getBody(t *testing.T, client *http.Client, rawURL string) string {
	t.Helper()
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s -> %d: %s", rawURL, resp.StatusCode, raw)
	}
	return string(raw)
}

// csrfOf pulls the CSRF token out of a rendered page.
func csrfOf(t *testing.T, page string) string {
	t.Helper()
	idx := strings.Index(page, `name="csrf_token" value="`)
	if idx < 0 {
		t.Fatal("no CSRF token in the page")
	}
	rest := page[idx+len(`name="csrf_token" value="`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("unterminated CSRF token")
	}
	return rest[:end]
}
