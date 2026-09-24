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
// templates, the way main.go does, and returns its base URL.
func apiTestServer(t *testing.T) string {
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
	tpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	h := New(db, tpl, Config{APITimeout: 5 * time.Second})
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	return srv.URL
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
	base := apiTestServer(t)
	host, port := splitStubURL(t, stub.server.URL)

	body := `{"name":"stub","host":"` + host + `","port":` + strconv.Itoa(port) +
		`,"username":"aircoins","password":"s3cret","transport":"` + database.TransportREST +
		`","rest_port":` + strconv.Itoa(port) + `}`
	resp, err := http.Post(base+"/api/v1/routers", "application/json", strings.NewReader(body))
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
		resp, err := http.Get(url)
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
	resp, err = http.Get(base + "/api/v1/routers/9999/interfaces/*1/traffic")
	if err != nil {
		t.Fatalf("unknown router poll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown router status %d, want 404", resp.StatusCode)
	}
}
