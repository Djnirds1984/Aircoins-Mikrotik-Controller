package handlers

import (
	"context"
	"encoding/json"
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
// form flow performs. Its interface list deliberately omits "bridge1-HS" (the
// Sep 25 report) while the WRITE accepts it: the device knows more than the
// snapshot the page was built from, and that snapshot must never veto a write
// the device itself would take. A body carrying "comment" is refused first,
// exactly like remote.oxapsph.com:10775, and any other unknown interface is
// refused with the real RouterOS sentence the operator saw.
func hotspotServerPageStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var puts []string
	known := map[string]bool{"ether1": true, "bridge-lan": true, "bridge1-HS": true}
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
			// The snapshot the page's datalist is built from - without
			// bridge1-HS, which only the write itself will accept.
			_, _ = w.Write([]byte(`[{"name":"ether1","type":"ether"},{"name":"bridge-lan","type":"bridge"}]`))
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/rest/ip/hotspot" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			if strings.Contains(string(raw), `"comment"`) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":400,"message":"Bad Request","detail":"unknown parameter comment"}`))
				return
			}
			var body map[string]string
			_ = json.Unmarshal(raw, &body)
			if !known[body["interface"]] {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":400,"message":"Bad Request","detail":"input does not match any value of interface"}`))
				return
			}
			_, _ = w.Write([]byte(`{"name":"` + body["name"] + `","interface":"` + body["interface"] + `",".id":"*1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, &puts
}

// TestHotspotServerCreateOnlyReportsTheDevicesOwnRefusal drives the editor the
// way an operator does and pins the fix for the Sep 25 report: an interface the
// page's snapshot lacks but the DEVICE accepts must create the server, and a
// name the device truly refuses must come back as a field error quoting the
// device's own answer. The blank comment is stripped on retry, as this
// RouterOS build requires.
func TestHotspotServerCreateOnlyReportsTheDevicesOwnRefusal(t *testing.T) {
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
	if !strings.Contains(page, "ether1") {
		t.Fatal("the page did not render the live interface list")
	}
	if strings.Contains(page, "bridge1-HS") {
		t.Fatal("test setup: the page's snapshot must not contain bridge1-HS")
	}
	token := csrfOf(t, page)

	// The Sep 25 case: the snapshot lacks bridge1-HS, the device takes it - and
	// the spaces an operator types are normalised on the way in.
	accepted := url.Values{
		"csrf_token": {token},
		"name":       {"hs1"},
		"interface":  {" bridge1-HS "},
	}
	resp, err := browser.PostForm(web.URL+"/network/1/servers", accepted)
	if err != nil {
		t.Fatalf("POST accepted interface: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accepted interface status = %d, want 200 after the redirect; body: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "created") {
		t.Errorf("page after create does not carry the success flash: %.200s", raw)
	}
	if len(*puts) != 2 {
		t.Fatalf("PUT count = %d, want 2 (comment refused, then retried): %v", len(*puts), *puts)
	}
	if !strings.Contains((*puts)[0], `"comment"`) {
		t.Errorf("first PUT should carry the comment the device refuses: %s", (*puts)[0])
	}
	if !strings.Contains((*puts)[1], `"interface":"bridge1-HS"`) {
		t.Errorf("retry should carry the trimmed interface: %s", (*puts)[1])
	}
	if strings.Contains((*puts)[1], `"comment"`) {
		t.Errorf("retry must drop the comment: %s", (*puts)[1])
	}

	// A name the DEVICE refuses: the write is attempted (twice, because the
	// blank comment is stripped first), the device says no, and its answer
	// becomes the field error. Values are kept and the editor reopens.
	*puts = nil
	refused := url.Values{
		"csrf_token": {token},
		"name":       {"hs2"},
		"interface":  {"ghost0"},
	}
	resp, err = browser.PostForm(web.URL+"/network/1/servers", refused)
	if err != nil {
		t.Fatalf("POST refused interface: %v", err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("refused interface status = %d, want 422; body: %s", resp.StatusCode, raw)
	}
	if len(*puts) != 2 {
		t.Fatalf("PUT count = %d, want 2 writes attempted before the refusal: %v", len(*puts), *puts)
	}
	body := string(raw)
	for _, want := range []string{
		"The device refused interface",
		"ghost0",
		"Interfaces this router reports: ether1, bridge-lan",
		"Not among the names the device reports: ghost0",
		`value="ghost0"`,
		`<details class="editor" open>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("422 page is missing %q", want)
		}
	}
}
