package handlers

import (
	"context"
	"html/template"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// hotspotServerPageStub answers the reads and writes the full hotspot server
// form flow performs: the identity probe, the interface list the page renders
// and the handler validates against, and the /ip/hotspot write - which rejects
// a body carrying "comment" exactly like the device behind
// remote.oxapsph.com:10775 does, so a successful create also exercises the
// strip-and-retry path.
func hotspotServerPageStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var puts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "aircoins" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/system/identity":
			_, _ = w.Write([]byte(`{"name":"Tolosa"}`))
		case r.URL.Path == "/rest/interface/print":
			_, _ = w.Write([]byte(`[{"name":"ether1","type":"ether"},{"name":"bridge-lan","type":"bridge"}]`))
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			answerHotspotWrite(w, string(raw))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, &puts
}

// TestHotspotServerCreateValidatesInterfaceOverTheWeb drives the editor the
// way an operator does and pins the fix for the journal of Sep 24 09:17: an
// interface name the device does not have must come back as a field error on
// a reopened form with no write attempted, while a name it does have reaches
// the device - spaces and all, normalised on the way in.
func TestHotspotServerCreateValidatesInterfaceOverTheWeb(t *testing.T) {
	stub, puts := hotspotServerPageStub(t)
	host, port := splitStubURL(t, stub.URL)

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
	web := httptest.NewServer(h.Routes())
	defer web.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	browser := &http.Client{Jar: jar}

	if _, err := db.Routers().Create(ctx, database.Router{
		Name: "stub", Host: host, Port: port,
		Username: "aircoins", Password: "s3cret",
		Transport: database.TransportREST, RestPort: port,
	}); err != nil {
		t.Fatalf("create router: %v", err)
	}

	page := getBody(t, browser, web.URL+"/network/1?tab=servers")
	token := csrfOf(t, page)

	// A name the device does not have: field error, editor reopened, values
	// preserved, and nothing sent to the device.
	bad := url.Values{
		"csrf_token": {token},
		"name":       {"hs1"},
		"interface":  {"ghost0"},
	}
	resp, err := browser.PostForm(web.URL+"/network/1/servers", bad)
	if err != nil {
		t.Fatalf("POST unknown interface: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown interface status = %d, want 422; body: %s", resp.StatusCode, raw)
	}
	body := string(raw)
	for _, want := range []string{
		"no interface named ghost0",
		"Available: ether1, bridge-lan",
		`value="ghost0"`,
		`<details class="editor" open>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("422 page is missing %q", want)
		}
	}
	if len(*puts) != 0 {
		t.Fatalf("device received %d writes for a refused form, want 0: %v", len(*puts), *puts)
	}

	// A name the device has - posted with the spaces an operator would type -
	// normalises, passes the check and reaches the device. The blank comment
	// is rejected and retried without it, as this RouterOS build demands.
	good := url.Values{
		"csrf_token": {token},
		"name":       {"hs1"},
		"interface":  {" ether1 "},
	}
	resp, err = browser.PostForm(web.URL+"/network/1/servers", good)
	if err != nil {
		t.Fatalf("POST known interface: %v", err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("known interface status = %d, want 200 after the redirect; body: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "created") {
		t.Errorf("page after create does not carry the success flash: %.200s", raw)
	}
	if len(*puts) != 2 {
		t.Fatalf("PUT count = %d, want 2 (comment rejected, then retried): %v", len(*puts), *puts)
	}
	if !strings.Contains((*puts)[0], `"comment"`) {
		t.Errorf("first PUT should carry the comment the device rejects: %s", (*puts)[0])
	}
	if !strings.Contains((*puts)[1], `"interface":"ether1"`) {
		t.Errorf("retry should carry the normalised interface: %s", (*puts)[1])
	}
	if strings.Contains((*puts)[1], `"comment"`) {
		t.Errorf("retry must drop the comment: %s", (*puts)[1])
	}
}
