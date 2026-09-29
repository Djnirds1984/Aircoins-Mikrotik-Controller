package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"testing"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// editorConfig is the panel configuration the PORTAL tests run under. The admin
// prefix matters: the editor lives at /admin/portal-editor, so these tests also
// prove the routes survive the StripPrefix mount.
func editorConfig() Config {
	return Config{AdminPath: "/admin", PortalName: "Aircoins Hotspot", Version: "test"}
}

// TestPortalEditorPageRenders proves the new PORTAL page is reachable from the
// navigation and offers all five themes.
func TestPortalEditorPageRenders(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)

	body := getBody(t, client, base+"/admin/portal-editor")
	if !strings.Contains(body, "<h1>Portal</h1>") {
		t.Error("the PORTAL page did not render its heading")
	}
	// Every theme needs a radio input, otherwise the operator cannot select it.
	for _, theme := range database.PortalThemes {
		if !strings.Contains(body, `name="theme" value="`+theme+`"`) {
			t.Errorf("theme %q is missing from the editor", theme)
		}
	}
	// The navigation must link the page, or it can only be reached by typing
	// the URL.
	if !strings.Contains(body, `href="/admin/portal-editor"`) {
		t.Error("the navigation does not link the PORTAL page")
	}
}

// TestPortalEditorRequiresSession pins the guard: the appearance of the customer
// facing portal is an admin secret, and an anonymous browser must be sent to
// the login form rather than shown the editor.
func TestPortalEditorRequiresSession(t *testing.T) {
	base, _ := newUnauthE2E(t, editorConfig())

	// noRedirect is essential here: a default client would follow the 303 to
	// the login form and report its 200, which is exactly the confusion this
	// test exists to rule out.
	resp, err := noRedirect().Get(base + "/admin/portal-editor")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 to the login form", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Location"), "/admin/login") {
		t.Errorf("redirected to %q, want the login form", resp.Header.Get("Location"))
	}
}

// TestPortalEditorSavesAppearance walks the real form: change the theme, rename
// the header, add custom HTML, then confirm a guest sees all three.
func TestPortalEditorSavesAppearance(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)
	editor := base + "/admin/portal-editor"

	page := getBody(t, client, editor)
	resp, err := client.PostForm(editor+"/save", url.Values{
		"csrf_token":  {csrfOf(t, page)},
		"theme":       {database.PortalThemeForest},
		"header_name": {"Tolosa Coffee Wi-Fi"},
		"custom_html": {`<p class="hint">Open daily 08:00 - 22:00</p>`},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save ended on %d, want the editor again", resp.StatusCode)
	}
	if !strings.HasSuffix(resp.Request.URL.Path, "/portal-editor") {
		t.Fatalf("save landed on %q, want the editor", resp.Request.URL.Path)
	}

	// The sign-in page is public, so an anonymous client proves what a guest
	// actually receives.
	guest := &http.Client{}
	body := getBody(t, guest, base+"/portal/login")
	if !strings.Contains(body, "Tolosa Coffee Wi-Fi") {
		t.Error("the saved header name is not shown on the sign-in page")
	}
	if !strings.Contains(body, "Open daily 08:00 - 22:00") {
		t.Error("the saved custom HTML is not rendered on the sign-in page")
	}
	// The Forest theme's accent has to reach the page, otherwise the radio
	// button saved something the browser cannot apply.
	if !strings.Contains(body, "#84cc16") {
		t.Error("the Forest theme CSS was not applied to the sign-in page")
	}
	// The selected radio has to come back checked, or the form would silently
	// show Midnight on the next visit.
	editor = getBody(t, client, base+"/admin/portal-editor")
	if !strings.Contains(editor, `value="forest" checked`) {
		t.Error("the saved theme is not marked as selected in the editor")
	}
}

// TestPortalEditorRejectsScriptAndLongHeader covers the two validations an
// operator is most likely to hit.
func TestPortalEditorRejectsScriptAndLongHeader(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)
	editor := base + "/admin/portal-editor"

	page := getBody(t, client, editor)
	resp, err := client.PostForm(editor+"/save", url.Values{
		"csrf_token":  {csrfOf(t, page)},
		"theme":       {database.PortalThemeOcean},
		"header_name": {strings.Repeat("x", database.MaxPortalHeaderName+1)},
		"custom_html": {"<script>fetch('http://evil.example')</script>"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for rejected input", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.Contains(body, "Scripts are not allowed") {
		t.Error("the script block was not reported inline")
	}
	if !strings.Contains(body, "Keep the header under") {
		t.Error("the over-long header was not reported inline")
	}

	// Nothing may have been stored: a rejected save must not half-apply.
	stored, err := db.PortalSettings().Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Theme != database.DefaultPortalTheme || stored.CustomHTML != "" {
		t.Errorf("a rejected save was partially applied: %+v", stored)
	}
}

// uploadBackground posts a file part to the editor's upload route, walking the
// real form including the CSRF token read from a fresh page.
//
// contentType is sent in the part header on purpose: the handler must sniff the
// bytes instead of trusting a value the browser chose.
func uploadBackground(t *testing.T, client *http.Client, editorURL, filename, contentType string, data []byte, csrf string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if csrf != "" {
		if err := writer.WriteField("csrf_token", csrf); err != nil {
			t.Fatalf("write csrf field: %v", err)
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="background"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, editorURL+"/background", &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return resp
}

// TestPortalBackgroundUploadAndServe is the image path end to end: a real JPEG
// is uploaded through the multipart form, stored, and then fetched by an
// unauthenticated guest exactly as the captive pages fetch it.
//
// The part is announced as image/png while the bytes are a JPEG, which proves
// the handler sniffs the content instead of believing the upload headers.
func TestPortalBackgroundUploadAndServe(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)
	editor := base + "/admin/portal-editor"

	payload := testJPEG(t)
	page := getBody(t, client, editor)
	resp := uploadBackground(t, client, editor, "shopfront.jpg", "image/png", payload, csrfOf(t, page))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload ended on %d: %s", resp.StatusCode, raw)
	}

	// A guest fetching the image must get the exact bytes back, with the type
	// sniffed from the content rather than from the lied-about header.
	guest := &http.Client{}
	imgResp, err := guest.Get(base + "/portal/background")
	if err != nil {
		t.Fatalf("GET background: %v", err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusOK {
		t.Fatalf("GET background -> %d, want 200", imgResp.StatusCode)
	}
	if got := imgResp.Header.Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg sniffed from the bytes", got)
	}
	served, _ := io.ReadAll(imgResp.Body)
	if !bytes.Equal(served, payload) {
		t.Errorf("served %d bytes, want the %d uploaded", len(served), len(payload))
	}

	// And the sign-in page must now reference it.
	if !strings.Contains(getBody(t, guest, base+"/portal/login"), "url('/portal/background')") {
		t.Error("the sign-in page does not use the uploaded background")
	}
}

// TestPortalBackgroundRejectsNonImage proves a GIF is refused, so a guest can
// never be served a non-image document under an image Content-Type.
func TestPortalBackgroundRejectsNonImage(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)
	editor := base + "/admin/portal-editor"

	page := getBody(t, client, editor)
	resp := uploadBackground(t, client, editor, "payload.gif", "image/png",
		[]byte("GIF89a"+strings.Repeat("\x00", 64)), csrfOf(t, page))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 for a GIF", resp.StatusCode)
	}

	if _, _, _, err := db.PortalSettings().BackgroundImage(context.Background()); err == nil {
		t.Error("a rejected image was stored anyway")
	}
	// With nothing stored the public path must 404 rather than serve an empty
	// 200, so the page can tell "no background" from "broken background".
	guest := &http.Client{}
	imgResp, err := guest.Get(base + "/portal/background")
	if err != nil {
		t.Fatalf("GET background: %v", err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET background with no image -> %d, want 404", imgResp.StatusCode)
	}
}

// TestPortalBackgroundDeleteClearsImage checks the remove button frees the
// stored bytes and drops the image from the guest page.
func TestPortalBackgroundDeleteClearsImage(t *testing.T) {
	ctx := context.Background()
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)
	editor := base + "/admin/portal-editor"

	if err := db.PortalSettings().SetBackground(ctx, testPNG(t), "image/png", "shopfront.png"); err != nil {
		t.Fatalf("SetBackground: %v", err)
	}
	if !strings.Contains(getBody(t, client, base+"/portal/login"), "/portal/background") {
		t.Fatal("the background is not in use, cannot test removing it")
	}

	page := getBody(t, client, editor)
	resp, err := client.PostForm(editor+"/background/delete", url.Values{
		"csrf_token": {csrfOf(t, page)},
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete ended on %d, want the editor", resp.StatusCode)
	}
	if _, _, _, err := db.PortalSettings().BackgroundImage(ctx); err == nil {
		t.Error("the image is still stored after the delete")
	}
	if strings.Contains(getBody(t, client, base+"/portal/login"), "url('/portal/background')") {
		t.Error("the sign-in page still references the removed background")
	}
}

// TestPortalEditorUploadRequiresCSRF proves the multipart route is not a way
// around the double submit cookie. ParseForm does not populate PostForm for a
// multipart body, so a guard that only understood urlencoded forms would either
// reject every upload or skip the check; this pins the rejection.
func TestPortalEditorUploadRequiresCSRF(t *testing.T) {
	base, db := newCaptiveE2E(t, editorConfig())
	client := authedClientFor(t, base, db)

	resp := uploadBackground(t, client, base+"/admin/portal-editor", "shopfront.png", "image/png",
		testPNG(t), "not-the-right-token")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a bad CSRF token", resp.StatusCode)
	}
	if _, _, _, err := db.PortalSettings().BackgroundImage(context.Background()); err == nil {
		t.Error("an upload without a valid CSRF token was stored")
	}
}

// testPNG builds a small real PNG. The upload path sniffs the content type from
// the bytes, so a hand-written magic number would be weaker evidence than an
// actually encoded image.
func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, testImage(8)); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// testJPEG builds a small real JPEG, so the JPEG branch of the upload path is
// covered by an actual encoder rather than a magic number.
func testJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(16), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// testImage is a small deterministic gradient, enough for a real encoder to
// produce a valid file of either format.
func testImage(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 128, A: 255})
		}
	}
	return img
}
