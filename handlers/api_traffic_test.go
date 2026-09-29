package handlers

import (
	"context"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// apiTestServer wires a Handler to a temporary database and the parsed
// templates, the way main.go does. It returns the base URL and a client that
// already holds a panel session, because the REST API sits behind the auth
// guard like every other operator endpoint.
func apiTestServer(t *testing.T) (string, *http.Client) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{
		Path:          filepath.Join(t.TempDir(), "api.db"),
		SecretKeyPath: filepath.Join(t.TempDir(), "secret.key"),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.AdminUsers().EnsureAdminUser(ctx, testAdminUser, testAdminPass); err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	tpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	h := New(db, tpl, Config{APITimeout: 5 * time.Second})
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	return srv.URL, authedClientFor(t, srv.URL, db)
}

// apiTrafficPayload mirrors what templates/dashboard.html parses.
type apiTrafficPayload struct {
	InterfaceName string `json:"interface_name"`
	Interface     struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"interface"`
	Points []struct {
		Timestamp time.Time `json:"timestamp"`
		RxBytes   int64     `json:"rx_bytes"`
		TxBytes   int64     `json:"tx_bytes"`
		RxRate    int64     `json:"rx_rate"`
		TxRate    int64     `json:"tx_rate"`
	} `json:"points"`
}

// TestAPIRouterInterfaceTraffic drives the endpoint behind the dashboard
// graph: register a router pointing at the REST stub, then poll twice. The
// response must carry the {interface_name, points[]} contract the template
// maps, and the second poll must append to the first sample.
func TestAPIRouterInterfaceTraffic(t *testing.T) {
	stub := newRestStub(t)
	base, client := apiTestServer(t)
	host, port := splitStubURL(t, stub.server.URL)

	body := `{"name":"stub","host":"` + host + `","port":` + strconv.Itoa(port) +
		`,"username":"aircoins","password":"s3cret","transport":"` + database.TransportREST +
		`","rest_port":` + strconv.Itoa(port) + `}`
	resp, err := client.Post(base+"/api/v1/routers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create router status %d: %s", resp.StatusCode, raw)
	}
	var created apiRouter
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created router: %v", err)
	}

	poll := func(want int) apiTrafficPayload {
		t.Helper()
		url := base + "/api/v1/routers/" + strconv.FormatInt(created.ID, 10) +
			"/interfaces/*1/traffic"
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("traffic poll: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("traffic status %d, want %d: %s", resp.StatusCode, want, raw)
		}
		var payload apiTrafficPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode traffic %s: %v", raw, err)
		}
		return payload
	}

	first := poll(http.StatusOK)
	if first.InterfaceName != "ether1" {
		t.Errorf("interface_name = %q, want ether1", first.InterfaceName)
	}
	if first.Interface.Name != "ether1" {
		t.Errorf("interface.name = %q, want ether1", first.Interface.Name)
	}
	if len(first.Points) != 1 {
		t.Fatalf("first poll returned %d points, want 1", len(first.Points))
	}
	p := first.Points[0]
	if p.RxBytes != 1024 || p.TxBytes != 2048 || p.RxRate != 300 || p.TxRate != 400 {
		t.Errorf("point = %+v, want bytes 1024/2048 and rates 300/400", p)
	}
	if p.Timestamp.IsZero() {
		t.Error("point timestamp is missing")
	}

	second := poll(http.StatusOK)
	if len(second.Points) != 2 {
		t.Errorf("second poll returned %d points, want 2: the window must accumulate",
			len(second.Points))
	}

	// An unknown router keeps the standard error contract.
	resp, err = client.Get(base + "/api/v1/routers/9999/interfaces/*1/traffic")
	if err != nil {
		t.Fatalf("unknown router poll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown router status %d, want 404", resp.StatusCode)
	}
}

// registerRESTRouter adds a router that points at a REST stub and returns its
// database id, so a test can drive the API against a fake device.
func registerRESTRouter(t *testing.T, client *http.Client, base, host string, port int) int64 {
	t.Helper()
	body := `{"name":"stub","host":"` + host + `","port":` + strconv.Itoa(port) +
		`,"username":"aircoins","password":"s3cret","transport":"` + database.TransportREST +
		`","rest_port":` + strconv.Itoa(port) + `}`
	resp, err := client.Post(base+"/api/v1/routers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create router status %d: %s", resp.StatusCode, raw)
	}
	var created apiRouter
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created router: %v", err)
	}
	return created.ID
}

// pollInterfaceTraffic reads one sample of the graph endpoint and asserts the
// status the API answered with.
func pollInterfaceTraffic(t *testing.T, client *http.Client, base string, routerID int64, iface string, want int) apiTrafficPayload {
	t.Helper()
	url := base + "/api/v1/routers/" + strconv.FormatInt(routerID, 10) +
		"/interfaces/" + iface + "/traffic"
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("traffic poll %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("traffic status %d, want %d: %s", resp.StatusCode, want, raw)
	}
	var payload apiTrafficPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode traffic %s: %v", raw, err)
	}
	return payload
}

// strictRESTStub is a RouterOS REST endpoint that serves the interface list but
// refuses the API-only ".id" attribute inside a REST print body, the way
// several builds do. A traffic request that filters by id fails there, which is
// what the dashboard reported as "Failed to load traffic data"; the controller
// has to resolve the interface from the list instead.
//
// With idless set the list also omits ".id", which leaves the dashboard with
// only the interface name to send — the second half of the same bug.
type strictRESTStub struct{ server *httptest.Server }

func newStrictRESTStub(t *testing.T, idless bool) *strictRESTStub {
	t.Helper()
	stub := &strictRESTStub{}
	row := `{"name":"ether1","type":"ether","mtu":"1500",` +
		`"mac-address":"AA:BB:CC:DD:EE:FF","rx-byte":"1024","tx-byte":"2048",` +
		`"rx-packet":"10","tx-packet":"20","rx-rate":"3000","tx-rate":"4000"}`
	if !idless {
		row = `{".id":"*1",` + row[1:]
	}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "aircoins" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":401,"message":"Unauthorized"}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/system/identity":
			_, _ = w.Write([]byte(`{"name":"Tolosa","version":"7.16.2"}`))
		case "/rest/interface/print":
			if strings.Contains(string(raw), `".id"`) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":400,"message":"Bad Request","detail":"unknown attribute .id"}`))
				return
			}
			_, _ = w.Write([]byte("[" + row + "]"))
		case "/rest/interface/monitor-traffic":
			_, _ = w.Write([]byte(`{"name":"ether1","rx-byte":"1024","tx-byte":"2048",` +
				`"rx-bits-per-second":"3000","tx-bits-per-second":"4000"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// TestAPIRouterInterfaceTrafficLookupFallback covers the dashboard graph on a
// device that refuses the ".id" print filter: the endpoint must still resolve
// the interface the dropdown reported, by id or by name, instead of answering
// with an error the graph cannot draw.
func TestAPIRouterInterfaceTrafficLookupFallback(t *testing.T) {
	stub := newStrictRESTStub(t, false)
	base, client := apiTestServer(t)
	host, port := splitStubURL(t, stub.server.URL)
	routerID := registerRESTRouter(t, client, base, host, port)

	// The list endpoint must hand the dashboard a usable id to select.
	resp, err := client.Get(base + "/api/v1/routers/" + strconv.FormatInt(routerID, 10) + "/interfaces")
	if err != nil {
		t.Fatalf("interface list: %v", err)
	}
	defer resp.Body.Close()
	var listed struct {
		Interfaces []apiInterface `json:"interfaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode interfaces: %v", err)
	}
	if len(listed.Interfaces) != 1 || listed.Interfaces[0].ID != "*1" {
		t.Fatalf("interfaces = %+v, want exactly one with id *1", listed.Interfaces)
	}

	// Poll by id, the way the dropdown does, twice: the window must accumulate.
	first := pollInterfaceTraffic(t, client, base, routerID, "*1", http.StatusOK)
	if first.InterfaceName != "ether1" || len(first.Points) != 1 {
		t.Fatalf("payload = %+v, want a single ether1 sample", first)
	}
	if got := first.Points[0].RxBytes; got != 1024 {
		t.Errorf("rx_bytes = %d, want 1024: the counters of the resolved row must be used", got)
	}
	second := pollInterfaceTraffic(t, client, base, routerID, "*1", http.StatusOK)
	if len(second.Points) != 2 {
		t.Errorf("second poll returned %d points, want 2: the window must accumulate",
			len(second.Points))
	}

	// A dropdown that carried the interface name instead resolves too; the
	// window is keyed per requested interface, so this starts its own series.
	byName := pollInterfaceTraffic(t, client, base, routerID, "ether1", http.StatusOK)
	if byName.InterfaceName != "ether1" || len(byName.Points) != 1 {
		t.Errorf("payload = %+v, want one ether1 sample for the name lookup", byName)
	}

	// An interface the device does not have stays a 404, and a placeholder
	// value is refused before it ever reaches the device.
	missing := pollInterfaceTraffic(t, client, base, routerID, "*9", http.StatusNotFound)
	if missing.InterfaceName != "" || len(missing.Points) != 0 {
		t.Errorf("missing interface payload = %+v, want nothing", missing)
	}
	pollInterfaceTraffic(t, client, base, routerID, "undefined", http.StatusBadRequest)
}

// TestAPIRouterInterfaceTrafficWithoutIDs covers the other half of the same
// bug: a device whose interface list carries no ".id" at all. The dashboard
// then sends the interface name, and the endpoint must still produce samples
// even though the device refuses every ".id" print filter.
func TestAPIRouterInterfaceTrafficWithoutIDs(t *testing.T) {
	stub := newStrictRESTStub(t, true)
	base, client := apiTestServer(t)
	host, port := splitStubURL(t, stub.server.URL)
	routerID := registerRESTRouter(t, client, base, host, port)

	resp, err := client.Get(base + "/api/v1/routers/" + strconv.FormatInt(routerID, 10) + "/interfaces")
	if err != nil {
		t.Fatalf("interface list: %v", err)
	}
	defer resp.Body.Close()
	var listed struct {
		Interfaces []apiInterface `json:"interfaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode interfaces: %v", err)
	}
	if len(listed.Interfaces) != 1 || listed.Interfaces[0].Name != "ether1" ||
		listed.Interfaces[0].ID != "" {
		t.Fatalf("interfaces = %+v, want a single id-less ether1", listed.Interfaces)
	}

	// The name is what the dropdown falls back to, so it has to work.
	payload := pollInterfaceTraffic(t, client, base, routerID, "ether1", http.StatusOK)
	if payload.InterfaceName != "ether1" || len(payload.Points) != 1 {
		t.Fatalf("payload = %+v, want one ether1 sample", payload)
	}
	if got := payload.Points[0].RxBytes; got != 1024 {
		t.Errorf("rx_bytes = %d, want the counters of the interface the list reported", got)
	}

	// An id the device never offered is not silently accepted either.
	pollInterfaceTraffic(t, client, base, routerID, "*1", http.StatusNotFound)
}
