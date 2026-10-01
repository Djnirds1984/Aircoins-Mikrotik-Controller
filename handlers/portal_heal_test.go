package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// The self-heal is the difference between "the portal broke and the operator
// has to notice" and "the portal fixed itself before anyone phoned". These
// tests cover both halves: the rate-limit gate, and the decision to repair
// (or not) based on what the device is actually serving.

func TestPortalHealDueIsRateLimited(t *testing.T) {
	h := &Handler{}
	if !h.portalHealDue(1) {
		t.Fatal("the first check for a router must be allowed through")
	}
	if h.portalHealDue(1) {
		t.Fatal("a second check inside the window was allowed: every guest page view would re-dial the device")
	}
	if !h.portalHealDue(2) {
		t.Fatal("a different router must get its own window")
	}
}

// brokenFileRow is the scrambled stale page the heal must notice: present,
// non-empty, and full of Go template syntax the router can never render.
var brokenFileRow = map[string]string{
	".id": "*1", "name": "hotspot/login.html", "size": "900",
	"contents": `{{template "styles"}}`,
}

// goodFileRow is the handoff page, marker and all.
var goodFileRow = map[string]string{
	".id": "*1", "name": "hotspot/login.html", "size": "597",
	"contents": "<!-- " + portalHandoffMarker + " --><meta http-equiv=\"refresh\">",
}

func rowsJSON(rows ...map[string]string) string {
	if rows == nil {
		return "[]"
	}
	out, _ := json.Marshal(rows)
	return string(out)
}

// healStub stands for a device whose login page starts out broken and becomes
// the handoff page once a fetch lands. The DELETE is what flips it, so the
// test proves the repair really replaced the file rather than fetched over it.
func healStub(t *testing.T, commands *[]string, startBroken bool) *httptest.Server {
	t.Helper()
	broken := startBroken
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*commands = append(*commands, r.Method+" "+r.URL.Path+" "+string(raw))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"hotspot1","html-directory":"hotspot"}]`))
		case r.URL.Path == "/rest/ip/hotspot/profile" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"default"}]`))
		case strings.HasPrefix(r.URL.Path, "/rest/file") && r.Method == http.MethodDelete:
			// REST maps a remove to DELETE /rest/file/<id>; the device's file
			// table only becomes clean because THIS call happened.
			broken = false
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/rest/file") && r.Method == http.MethodGet:
			if broken {
				_, _ = w.Write([]byte(rowsJSON(brokenFileRow)))
			} else {
				_, _ = w.Write([]byte(rowsJSON(goodFileRow)))
			}
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
}

func healFixture(t *testing.T, server *httptest.Server) (*Handler, database.Router) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{
		Path:          filepath.Join(t.TempDir(), "heal.db"),
		SecretKeyPath: filepath.Join(t.TempDir(), "secret.key"),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	host, port := splitStubURL(t, server.URL)
	router, err := db.Routers().Create(ctx, database.Router{
		Name: "cafe", Host: host, Port: port,
		Username: "aircoins", Password: "s3cret",
		Transport: database.TransportREST, RestPort: port,
		DefaultPortal: true,
	})
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	// New applies the config defaults (the dial timeout matters), and the
	// repair path never renders a template, so the template set is not needed.
	return New(db, nil, Config{}), router
}

func TestRepairRouterHandoffReinstallsABrokenLoginPage(t *testing.T) {
	var commands []string
	server := healStub(t, &commands, true)
	defer server.Close()
	h, router := healFixture(t, server)

	h.repairRouterHandoff(router, "http://10.0.0.252/portal/router-login.html")

	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"DELETE /rest/file",
		"POST /rest/tool/fetch",
		`"url":"http://10.0.0.252/portal/router-login.html"`,
		`"dst-path":"hotspot/login.html"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the self-heal did not issue %q, guests keep seeing the broken page:\n%s", want, joined)
		}
	}
}

func TestRepairRouterHandoffLeavesAGoodPageAlone(t *testing.T) {
	var commands []string
	server := healStub(t, &commands, false)
	defer server.Close()
	h, router := healFixture(t, server)

	h.repairRouterHandoff(router, "http://10.0.0.252/portal/router-login.html")

	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, "/rest/tool/fetch") {
		t.Fatalf("a device already serving the handoff page was reinstalled anyway:\n%s", joined)
	}
}

func TestRepairRouterHandoffReinstallsAMissingPage(t *testing.T) {
	var commands []string
	server := healStubMissing(t, &commands)
	defer server.Close()
	h, router := healFixture(t, server)

	h.repairRouterHandoff(router, "http://10.0.0.252/portal/router-login.html")

	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "POST /rest/tool/fetch") {
		t.Fatalf("a router with no login file at all was not given the handoff page:\n%s", joined)
	}
}

// healStubMissing models the device with no login.html: /file never lists it,
// and the fetch itself deposits the good row.
func healStubMissing(t *testing.T, commands *[]string) *httptest.Server {
	t.Helper()
	fetched := false
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*commands = append(*commands, r.Method+" "+r.URL.Path+" "+string(raw))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"hotspot1","html-directory":"hotspot"}]`))
		case r.URL.Path == "/rest/file" && r.Method == http.MethodGet:
			if fetched {
				_, _ = w.Write([]byte(rowsJSON(goodFileRow)))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case r.URL.Path == "/rest/tool/fetch":
			fetched = true
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
}
