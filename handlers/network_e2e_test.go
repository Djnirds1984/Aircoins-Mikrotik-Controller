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
	base, browser := newNetworkE2E(t, stub.URL)

	page := getBody(t, browser, base+"/network/1?tab=servers")
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
	resp, err := browser.PostForm(base+"/network/1/servers", accepted)
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
	resp, err = browser.PostForm(base+"/network/1/servers", refused)
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

// newNetworkE2E starts the controller against one stub device and returns the
// running front end plus a browser carrying its own cookies, so a test can drive
// a Network editor exactly the way an operator does.
func newNetworkE2E(t *testing.T, stubURL string) (string, *http.Client) {
	t.Helper()
	host, port := splitStubURL(t, stubURL)

	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{
		Path:          filepath.Join(t.TempDir(), "e2e.db"),
		SecretKeyPath: filepath.Join(t.TempDir(), "secret.key"),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tpl, err := template.New("").Funcs(TemplateFuncs()).ParseGlob("../" + TemplatePattern)
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	h := New(db, tpl, Config{APITimeout: 5 * time.Second})
	web := httptest.NewServer(h.Routes())
	t.Cleanup(web.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	if _, err := db.Routers().Create(ctx, database.Router{
		Name: "stub", Host: host, Port: port,
		Username: "aircoins", Password: "s3cret",
		Transport: database.TransportREST, RestPort: port,
	}); err != nil {
		t.Fatalf("create router: %v", err)
	}
	return web.URL, &http.Client{Jar: jar}
}

// interfaceVLANPageStub answers the reads the VLAN section performs and records
// its writes. The device it stands for reports physical ports and a bridge, so
// the editor can be driven with a parent that is not a bridge.
func interfaceVLANPageStub(t *testing.T) (*httptest.Server, *[]string) {
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
			// A print carrying .proplist and .sum travels in the command form.
			_, _ = w.Write([]byte(`[{"name":"ether1","type":"ether"},{"name":"ether5","type":"ether"},{"name":"bridge1","type":"bridge"}]`))
		case r.URL.Path == "/rest/interface/vlan" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/rest/interface/vlan" && r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			puts = append(puts, string(raw))
			var body map[string]string
			_ = json.Unmarshal(raw, &body)
			_, _ = w.Write([]byte(`{".id":"*9","name":"` + body["name"] + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, &puts
}

// TestVLANCreateFollowsThePickedParentInterface pins the refactor: the editor
// creates an /interface/vlan interface, so the parent is any interface the
// device reports - a physical port here, a bridge when the operator picks one -
// and one submission always carries exactly one VLAN ID.
func TestVLANCreateFollowsThePickedParentInterface(t *testing.T) {
	stub, puts := interfaceVLANPageStub(t)
	base, browser := newNetworkE2E(t, stub.URL)

	page := getBody(t, browser, base+"/network/1?tab=vlans")
	token := csrfOf(t, page)
	for _, want := range []string{
		"Add a VLAN interface", `name="vlan_id"`, `list="iface-options"`,
		"ether5", "bridge1", "No VLAN interface on this device yet.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("vlans page is missing %q", want)
		}
	}
	if strings.Contains(page, "vlan-ids") {
		t.Error("the page still offers the old range editor")
	}

	resp, err := browser.PostForm(base+"/network/1/vlans", url.Values{
		"csrf_token": {token},
		"name":       {"vlan100"},
		"interface":  {"ether5"},
		"vlan_id":    {"100"},
		"mtu":        {"1500"},
		"comment":    {"guest uplink"},
	})
	if err != nil {
		t.Fatalf("POST vlan: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want 200 after the redirect; body: %s", resp.StatusCode, raw)
	}
	if len(*puts) != 1 {
		t.Fatalf("PUT count = %d, want 1: %v", len(*puts), *puts)
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte((*puts)[0]), &fields); err != nil {
		t.Fatalf("PUT body %q is not an object: %v", (*puts)[0], err)
	}
	for key, want := range map[string]string{
		"name": "vlan100", "interface": "ether5", "vlan-id": "100",
		"mtu": "1500", "comment": "guest uplink",
	} {
		if fields[key] != want {
			t.Errorf("PUT %s = %q, want %q (%v)", key, fields[key], want, fields)
		}
	}
	if _, bridge := fields["bridge"]; bridge {
		t.Errorf("the write still targets a bridge VLAN entry: %v", fields)
	}
	if strings.Contains(string(raw), "vlan-ids") {
		t.Error("the page after the create still speaks of vlan-ids")
	}

	// A range never reaches the device: the editor creates one VLAN per
	// submission and says so, keeping what the operator typed.
	*puts = nil
	resp, err = browser.PostForm(base+"/network/1/vlans", url.Values{
		"csrf_token": {token},
		"name":       {"vlan100"},
		"interface":  {"ether5"},
		"vlan_id":    {"100-120"},
	})
	if err != nil {
		t.Fatalf("POST vlan range: %v", err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("range status = %d, want 422; body: %s", resp.StatusCode, raw)
	}
	if len(*puts) != 0 {
		t.Errorf("a range must be refused before the device is written: %v", *puts)
	}
	body := string(raw)
	for _, want := range []string{
		"Ranges such as 100-120 are not accepted",
		`value="100-120"`,
		`<details class="editor" open>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("422 page is missing %q", want)
		}
	}
}
