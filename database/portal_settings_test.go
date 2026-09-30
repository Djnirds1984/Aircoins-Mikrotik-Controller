package database

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// TestPortalSettingsDefaults pins the state of a controller that was never
// configured: the built-in theme, no header override, no image. A fresh install
// has no portal_settings row at all, so Get must not report that as an error or
// every guest page would degrade on every request.
func TestPortalSettingsDefaults(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	settings, err := db.PortalSettings().Get(ctx)
	if err != nil {
		t.Fatalf("Get on an empty database: %v", err)
	}
	if settings.Theme != DefaultPortalTheme {
		t.Errorf("Theme = %q, want %q", settings.Theme, DefaultPortalTheme)
	}
	if settings.HeaderName != "" || settings.CustomHTML != "" {
		t.Errorf("expected empty header and HTML, got %q / %q", settings.HeaderName, settings.CustomHTML)
	}
	if settings.HasBackground() {
		t.Error("HasBackground is true before anything was uploaded")
	}
	if _, _, _, err := db.PortalSettings().BackgroundImage(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("BackgroundImage err = %v, want ErrNotFound", err)
	}
}

// TestPortalSettingsRoundTrip covers the whole editor workflow: save the text
// fields, attach a photo, read it back byte for byte, then remove the photo
// without losing the text.
func TestPortalSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.PortalSettings()

	want := PortalSettings{Theme: PortalThemeSunset, HeaderName: "Tolosa Coffee", CustomHTML: "<p>Open 08:00</p>"}
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Theme != want.Theme || got.HeaderName != want.HeaderName || got.CustomHTML != want.CustomHTML {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// A JPEG header is enough: the store keeps bytes verbatim and never
	// decodes, so byte equality is the real contract.
	payload := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	if err := store.SetBackground(ctx, payload, "image/jpeg", "shopfront.jpg"); err != nil {
		t.Fatalf("SetBackground: %v", err)
	}

	data, contentType, uploadedAt, err := store.BackgroundImage(ctx)
	if err != nil {
		t.Fatalf("BackgroundImage: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Errorf("image bytes = %v, want %v", data, payload)
	}
	if contentType != "image/jpeg" {
		t.Errorf("content type = %q, want image/jpeg", contentType)
	}
	if uploadedAt.IsZero() {
		t.Error("upload time was not recorded")
	}

	// Saving the text again must not drop the photo: the two are edited from
	// separate forms, and retyping a header name is not a request to forget the
	// image.
	if err := store.Save(ctx, PortalSettings{Theme: PortalThemeForest, HeaderName: "Renamed"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if _, _, _, err := store.BackgroundImage(ctx); err != nil {
		t.Fatalf("photo lost after a text save: %v", err)
	}

	if err := store.ClearBackground(ctx); err != nil {
		t.Fatalf("ClearBackground: %v", err)
	}
	if _, _, _, err := store.BackgroundImage(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("after clear err = %v, want ErrNotFound", err)
	}
	after, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if after.Theme != PortalThemeForest || after.HeaderName != "Renamed" {
		t.Errorf("clearing the image changed the text fields: %+v", after)
	}
}

// TestPortalSettingsRejectsBadInput covers the guards between the upload form
// and a row that is served to every guest.
func TestPortalSettingsRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.PortalSettings()

	if err := store.Save(ctx, PortalSettings{Theme: "neon-hacker"}); !errors.Is(err, ErrPortalTheme) {
		t.Errorf("unknown theme err = %v, want ErrPortalTheme", err)
	}

	tooLong := strings.Repeat("x", MaxPortalHeaderName+1)
	if err := store.Save(ctx, PortalSettings{Theme: DefaultPortalTheme, HeaderName: tooLong}); err == nil {
		t.Error("an over-long header name was accepted")
	}

	oversized := make([]byte, MaxPortalBackgroundBytes+1)
	if err := store.SetBackground(ctx, oversized, "image/jpeg", "big.jpg"); err == nil {
		t.Error("an oversized image was accepted")
	}
	if err := store.SetBackground(ctx, []byte("GIF89a"), "image/gif", "anim.gif"); err == nil {
		t.Error("a GIF was accepted: only JPEG and PNG may be stored")
	}
	if err := store.SetBackground(ctx, nil, "image/jpeg", "empty.jpg"); err == nil {
		t.Error("an empty image was accepted")
	}
}

// TestPortalPageModeRoundTrip covers the full-page override at the store level:
// the mode and the document survive a save, and the guards around them hold.
func TestPortalPageModeRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := db.PortalSettings()

	// A fresh install is on the standard layout, not in full-page mode.
	fresh, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fresh.PageMode != PortalPageStandard || fresh.FullPageActive() {
		t.Errorf("a fresh install is not on the standard layout: %+v", fresh)
	}

	page := "<html><body><script>tick()</script>{{.MAC}}</body></html>"
	if err := store.Save(ctx, PortalSettings{
		Theme:    DefaultPortalTheme,
		PageMode: PortalPageFull,
		FullHTML: page,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PageMode != PortalPageFull {
		t.Errorf("PageMode = %q, want %q", got.PageMode, PortalPageFull)
	}
	if got.FullHTML != page {
		t.Error("the saved page did not survive the round trip")
	}
	if !got.FullPageActive() {
		t.Error("FullPageActive is false for a stored full page")
	}

	// Switching back to the standard layout keeps the document, so an operator
	// who changes their mind does not lose the work.
	if err := store.Save(ctx, PortalSettings{
		Theme: DefaultPortalTheme, PageMode: PortalPageStandard, FullHTML: page,
	}); err != nil {
		t.Fatalf("Save standard: %v", err)
	}
	back, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if back.FullPageActive() {
		t.Error("the operator page is still active in standard mode")
	}
	if back.FullHTML != page {
		t.Error("switching to the standard layout discarded the page")
	}

	// An empty document counts as inactive even in full mode: a half-filled form
	// must not be able to blank the portal for every guest.
	if err := store.Save(ctx, PortalSettings{
		Theme: DefaultPortalTheme, PageMode: PortalPageFull, FullHTML: "  ",
	}); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	empty, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if empty.FullPageActive() {
		t.Error("a blank page in full mode is treated as active")
	}

	// Guards.
	if err := store.Save(ctx, PortalSettings{Theme: DefaultPortalTheme, PageMode: "sidebar"}); err == nil {
		t.Error("an unknown page mode was accepted")
	}
	oversized := make([]byte, MaxPortalFullHTML+1)
	if err := store.Save(ctx, PortalSettings{
		Theme: DefaultPortalTheme, PageMode: PortalPageFull, FullHTML: string(oversized),
	}); err == nil {
		t.Error("an oversized full page was accepted")
	}
}

// TestNormalizePortalPageModeFallsBack proves an unknown stored mode degrades to
// the standard layout rather than leaving the portal with no page at all.
func TestNormalizePortalPageModeFallsBack(t *testing.T) {
	if got := NormalizePortalPageMode("sidebar"); got != PortalPageStandard {
		t.Errorf("NormalizePortalPageMode(sidebar) = %q, want %q", got, PortalPageStandard)
	}
	if got := NormalizePortalPageMode(""); got != PortalPageStandard {
		t.Errorf("NormalizePortalPageMode(empty) = %q, want %q", got, PortalPageStandard)
	}
}

// TestNormalizePortalThemeFallsBack proves a stored key that no longer exists
// (a renamed theme, or a hand-edited row) degrades to the default instead of
// producing a portal with no colours at all.
func TestNormalizePortalThemeFallsBack(t *testing.T) {
	if got := NormalizePortalTheme("chartreuse"); got != DefaultPortalTheme {
		t.Errorf("NormalizePortalTheme(chartreuse) = %q, want %q", got, DefaultPortalTheme)
	}
	if got := NormalizePortalTheme(""); got != DefaultPortalTheme {
		t.Errorf("NormalizePortalTheme(empty) = %q, want %q", got, DefaultPortalTheme)
	}
	for _, theme := range PortalThemes {
		if !ValidPortalTheme(theme) {
			t.Errorf("theme %q is listed but reported invalid", theme)
		}
	}
}
