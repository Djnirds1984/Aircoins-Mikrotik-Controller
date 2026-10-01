package handlers

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// portalBranding is the operator supplied look of the guest facing pages,
// resolved once per request and handed to the captive templates.
//
// It is a single value rather than a set of loose fields so the two guest pages
// cannot disagree: they render the same brand, theme and background.
type portalBranding struct {
	// HeaderName is the brand line shown to guests. It falls back to
	// Config.PortalName so an install that never opened the editor looks
	// exactly as it did before.
	HeaderName string
	// ThemeCSS is the :root override block for the selected theme.
	ThemeCSS template.CSS
	// Theme is the raw key, used to mark the active card in the editor.
	Theme string
	// BackgroundURL is the path the captive pages fetch the photo from. Empty
	// when no image is stored.
	BackgroundURL string
	// CustomHTML is the operator's extra markup, injected into the portal card.
	CustomHTML template.HTML
}

// portalBackgroundPath serves the stored background image to the guest pages.
//
// It lives under /portal/ so it stays reachable without a panel session (see
// isPublicPath).
const portalBackgroundPath = "/portal/background"

// The admin routes of the PORTAL page. They are deliberately not under
// /portal/, so they never inherit the guest-facing exemptions in
// isPublicPath and csrfGuard.
const (
	portalEditorPath           = "/portal-editor"
	portalEditorSavePath       = "/portal-editor/save"
	portalEditorBackgroundPath = "/portal-editor/background"
	portalEditorBackgroundDrop = "/portal-editor/background/delete"
	// portalEditorInstallPath runs the router-side fetch that points each
	// hotspot at this panel's login page, so the operator never retypes
	// /tool fetch after a deploy.
	portalEditorInstallPath = "/portal-editor/install-router-page"
)

// portalEditorDefaults are the values the editor needs to describe its own
// limits, kept in Go so the help text cannot drift from the validation.
type portalEditorDefaults struct {
	MaxHeaderRunes int
	MaxHTMLBytes   int
	MaxImageBytes  int
	MaxFullBytes   int
}

// portalTheme is one selectable theme with the CSS that implements it.
// portalThemes are the five built-in looks of the captive portal.
//
// Each one only redefines the custom properties partials.html already declares,
// so no markup has to change to support a new theme. They are ordered from the
// built-in default to the most distinctive, which is the order the editor
// shows them in.
var portalThemes = []portalTheme{
	{
		Key:         database.PortalThemeMidnight,
		Label:       "Midnight",
		Description: "The built-in dark blue. Low light, easy on the eyes.",
		Swatch:      "linear-gradient(135deg,#0b1120,#16223c)",
		// The default values are restated so the card is a faithful preview
		// and switching away and back is a no-op.
		CSS: template.CSS(`:root{
  --bg:#0b1120; --panel:#111c33; --panel-2:#16223c; --line:#22304d; --text:#e6ecf7;
  --muted:#8fa3c4; --accent:#38bdf8; --accent-2:#22d3ee;
}`),
	},
	{
		Key:         database.PortalThemeOcean,
		Label:       "Ocean",
		Description: "Deep teal and aqua. Calm, high contrast.",
		Swatch:      "linear-gradient(135deg,#04252e,#0f4f5c)",
		CSS: template.CSS(`:root{
  --bg:#04252e; --panel:#0b3d47; --panel-2:#0f4f5c; --line:#16697a; --text:#e6fbff;
  --muted:#8fc7d4; --accent:#2dd4bf; --accent-2:#22d3ee;
}`),
	},
	{
		Key:         database.PortalThemeSunset,
		Label:       "Sunset",
		Description: "Warm amber on a plum background. Welcoming at dusk.",
		Swatch:      "linear-gradient(135deg,#3b1220,#7c2d43)",
		CSS: template.CSS(`:root{
  --bg:#3b1220; --panel:#5b1a2c; --panel-2:#7c2d43; --line:#a34660; --text:#fff1ec;
  --muted:#e0a894; --accent:#fb923c; --accent-2:#fbbf24;
}`),
	},
	{
		Key:         database.PortalThemeForest,
		Label:       "Forest",
		Description: "Deep green with a lime accent. Quiet and outdoorsy.",
		Swatch:      "linear-gradient(135deg,#0a1f14,#14532d)",
		CSS: template.CSS(`:root{
  --bg:#0a1f14; --panel:#12331f; --panel-2:#14532d; --line:#2f6b45; --text:#e9fbee;
  --muted:#93c9a8; --accent:#84cc16; --accent-2:#22c55e;
}`),
	},
	{
		Key:         database.PortalThemeLight,
		Label:       "Daylight",
		Description: "A light page for bright rooms and daylight readability.",
		Swatch:      "linear-gradient(135deg,#f8fafc,#e2e8f0)",
		CSS: template.CSS(`:root{
  --bg:#f1f5f9; --panel:#ffffff; --panel-2:#f8fafc; --line:#cbd5e1; --text:#0f172a;
  --muted:#64748b; --accent:#0284c7; --accent-2:#0891b2;
  --shadow:0 10px 30px rgba(15,23,42,.12);
}`),
	},
}

// portalThemeByKey looks a theme up, falling back to the default so an unknown
// stored value still renders.
func portalThemeByKey(key string) portalTheme {
	for _, theme := range portalThemes {
		if theme.Key == key {
			return theme
		}
	}
	return portalThemes[0]
}

// portalEditorForm carries the editor values, including the ones that failed
// validation so a long custom HTML block is not lost on a typo elsewhere.
type portalEditorForm struct {
	HeaderName string
	Theme      string
	CustomHTML string
	// PageMode is "standard" for the built-in layout or "full" for the
	// operator's own document.
	PageMode string
	// FullHTML is the operator's complete portal page, used in full mode.
	FullHTML string
	Errors   map[string]string
}

func newPortalEditorForm() portalEditorForm {
	return portalEditorForm{
		Theme:    database.DefaultPortalTheme,
		PageMode: database.PortalPageStandard,
		Errors:   map[string]string{},
	}
}

// portalEditorFormFromRequest reads the submitted editor form.
func portalEditorFormFromRequest(r *http.Request) portalEditorForm {
	form := newPortalEditorForm()
	form.HeaderName = strings.TrimSpace(r.PostFormValue("header_name"))
	form.Theme = database.NormalizePortalTheme(r.PostFormValue("theme"))
	form.CustomHTML = r.PostFormValue("custom_html")
	// An unchecked radio simply does not appear in the body, so a missing value
	// means the operator chose the default rather than that something was lost.
	form.PageMode = database.NormalizePortalPageMode(r.PostFormValue("page_mode"))
	form.FullHTML = r.PostFormValue("full_html")
	return form
}

// firstLine trims a multi-line compiler error down to its first line, which is
// the part that names the problem. A template error carries the offending source
// on later lines, which would be dumped into the middle of the editor form.
func firstLine(message string) string {
	message = strings.TrimSpace(message)
	if idx := strings.IndexByte(message, '\n'); idx >= 0 {
		return strings.TrimSpace(message[:idx])
	}
	return message
}

// portalEditorPage backs the PORTAL page of the operator panel.
type portalEditorPage struct {
	page
	// Settings is the stored appearance, used to describe what is live now.
	Settings database.PortalSettings
	// Form carries the submitted values back on a validation error.
	Form portalEditorForm
	// Themes is the selectable list, in display order.
	Themes []portalTheme
	// HasBackground reports whether a photo is stored, so the editor can show
	// the preview and the remove button.
	HasBackground bool
	// UpdatedAt is when the text fields were last saved.
	UpdatedAt time.Time
	// BackgroundUpdatedAt is when the photo was last replaced.
	BackgroundUpdatedAt *time.Time
	// Defaults exposes the limits to the template.
	Defaults portalEditorDefaults
	// RouterLoginURL is the absolute address of the login page to install on
	// the MikroTik, so the operator can paste it straight into /tool fetch.
	RouterLoginURL string
	// PortalHost is the address of this panel, for the walled-garden rule that
	// makes it reachable before a guest has signed in.
	PortalHost string
	// Starter is the sample data the starter page is rendered with, so the
	// operator sees a filled-in example rather than a wall of {{.Placeholders}}.
	Starter portalFullData
}

type portalTheme struct {
	// Key is the persisted value.
	Key string
	// Label is what the operator reads on the card.
	Label string
	// Description is the one line hint under the label.
	Description string
	// Swatch is the gradient the card is drawn with, so the preview is
	// generated from the same colours the page uses.
	Swatch string
	// CSS is the :root override block applied to the guest pages.
	CSS template.CSS
}

// newPortalEditorView assembles the page from the stored settings and the form
// values, so a rejected save renders exactly like a fresh load except for the
// inline errors and the retained text.
func (h *Handler) newPortalEditorView(settings database.PortalSettings, form portalEditorForm) *portalEditorPage {
	return &portalEditorPage{
		page: page{
			Title: "Portal",
			Nav:   "portal",
			Back:  h.cfg.AdminPath + portalEditorPath,
		},
		Settings:            settings,
		Form:                form,
		Themes:              portalThemes,
		HasBackground:       settings.HasBackground(),
		UpdatedAt:           settings.UpdatedAt,
		BackgroundUpdatedAt: settings.BackgroundAt,
		Defaults: portalEditorDefaults{
			MaxHeaderRunes: database.MaxPortalHeaderName,
			MaxHTMLBytes:   database.MaxPortalCustomHTML,
			MaxImageBytes:  database.MaxPortalBackgroundBytes,
			MaxFullBytes:   database.MaxPortalFullHTML,
		},
		Starter: samplePortalData(h.cfg.PortalName),
	}
}

// samplePortalData fills the starter page with plausible values.
//
// The starter is inserted into the textarea as text, so it is rendered with
// example data rather than blanks: an operator reading it should see what each
// placeholder produces, not a page full of empty spots. .LoginAction stays a
// plain path so the example cannot be copy-pasted into a live page and break a
// login by pointing at a URL with no hotspot parameters on it.
func samplePortalData(portalName string) portalFullData {
	return portalFullData{
		PortalName:  portalName,
		MAC:         "D6:A8:AA:7A:70:E5",
		IP:          "10.1.0.135",
		RouterName:  "Ground floor",
		RouterKnown: true,
		LoginAction: template.URL(portalLoginPath),
		LoggedIn:    false,
		FormError:   "",
		Notice:      "",
		ExpiresAt:   "2026-12-31T23:59:00Z",
		BannerURL:   portalBackgroundPath,
		AdminPath:   "/admin",
		LoginURL:    portalLoginPath,
		Year:        2026,
		Version:     "1.0.0",
	}
}

// PortalEditor renders the PORTAL page: the theme, the header name, the extra
// HTML and the background image of the captive portal.
func (h *Handler) PortalEditor(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.PortalSettings().Get(r.Context())
	if err != nil {
		h.fail(w, r, "load portal settings", err)
		return
	}
	form := newPortalEditorForm()
	form.HeaderName = settings.HeaderName
	form.Theme = settings.Theme
	form.CustomHTML = settings.CustomHTML
	form.PageMode = settings.PageMode
	form.FullHTML = settings.FullHTML

	view := h.newPortalEditorView(settings, form)
	// The install instructions are generated from the address the operator is
	// actually using, so the commands can be pasted into RouterOS unchanged.
	if base := portalBaseURL(r); base != "" {
		view.RouterLoginURL = base + portalRouterLoginPath
		view.PortalHost = portalRequestHost(r.Host)
	}

	h.render(w, r, http.StatusOK, "portal_editor.html", view)
}

// PortalEditorSave stores the theme, the header name and the extra HTML.
//
// The three fields are saved together rather than as separate forms so the
// operator can rename the portal and change its look in one submit.
func (h *Handler) PortalEditorSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the form", http.StatusBadRequest)
		return
	}

	settings, err := h.db.PortalSettings().Get(ctx)
	if err != nil {
		h.fail(w, r, "load portal settings", err)
		return
	}

	form := portalEditorFormFromRequest(r)
	if runes := len([]rune(form.HeaderName)); runes > database.MaxPortalHeaderName {
		form.Errors["header_name"] = fmt.Sprintf("Keep the header under %d characters (this one has %d).",
			database.MaxPortalHeaderName, runes)
	}
	if len(form.CustomHTML) > database.MaxPortalCustomHTML {
		form.Errors["custom_html"] = fmt.Sprintf("The extra HTML is %d KB; the limit is %d KB. Shorten it, or move large images to the background upload.",
			len(form.CustomHTML)/1024, database.MaxPortalCustomHTML/1024)
	}
	if len(form.FullHTML) > database.MaxPortalFullHTML {
		form.Errors["full_html"] = fmt.Sprintf("The page is %d KB; the limit is %d KB.",
			len(form.FullHTML)/1024, database.MaxPortalFullHTML/1024)
	}

	// A full page that cannot compile would silently fall back to the built-in
	// layout, which is safe but confusing: the operator saves, sees no change,
	// and has no idea why. Compile it here so the error names the line.
	if strings.TrimSpace(form.FullHTML) != "" {
		if _, err := template.New("check").Parse(form.FullHTML); err != nil {
			form.Errors["full_html"] = "This page cannot be compiled: " + firstLine(err.Error())
		}
	}

	// The custom block goes inside the built-in card, so it is held to the
	// narrower rules: no scripts and no frames. The full page is a different
	// contract - it is the whole document and may contain a countdown <script> -
	// and is only bounded by the Content-Security-Policy.
	lowered := strings.ToLower(form.CustomHTML)
	for _, rule := range []struct{ needle, message string }{
		{"<script", "Scripts are not allowed in the extra HTML block. Use the full-page mode for a page with JavaScript."},
		{"</script", "Scripts are not allowed in the extra HTML block. Use the full-page mode for a page with JavaScript."},
		{"javascript:", "javascript: links are not allowed."},
		{"<iframe", "Iframes are not allowed: a walled-garden guest has no route to load them."},
	} {
		if strings.Contains(lowered, rule.needle) {
			form.Errors["custom_html"] = rule.message
			break
		}
	}
	if len(form.Errors) > 0 {
		h.render(w, r, http.StatusBadRequest, "portal_editor.html", h.newPortalEditorView(settings, form))
		return
	}

	saved := database.PortalSettings{
		Theme:      form.Theme,
		HeaderName: form.HeaderName,
		CustomHTML: form.CustomHTML,
		PageMode:   form.PageMode,
		FullHTML:   form.FullHTML,
	}
	if err := h.db.PortalSettings().Save(ctx, saved); err != nil {
		if errors.Is(err, database.ErrPortalTheme) {
			form.Errors["theme"] = "Pick one of the listed themes."
			h.render(w, r, http.StatusBadRequest, "portal_editor.html", h.newPortalEditorView(settings, form))
			return
		}
		h.fail(w, r, "save portal settings", err)
		return
	}

	h.log.Info("portal appearance saved", "remote", clientIP(r),
		"theme", saved.Theme, "header_name", saved.HeaderName, "custom_html_bytes", len(saved.CustomHTML))

	setFlash(w, "Portal appearance saved. Guests see it on their next page load.", "ok")
	http.Redirect(w, r, h.cfg.AdminPath+portalEditorPath, http.StatusSeeOther)
}

// PortalRouterInstall points every registered hotspot at this panel by making
// each router fetch the redirect login page over its management API. It is the
// one-click version of the /tool fetch an operator would otherwise retype on
// every site: the panel generates that page live and it carries no Go template
// tags, so once installed it survives controller updates and never needs
// refreshing. A stale copy of the built-in template is exactly what shows
// guests raw {{ }} text, and this replaces it with the working redirect.
func (h *Handler) PortalRouterInstall(w http.ResponseWriter, r *http.Request) {
	back := h.cfg.AdminPath + portalEditorPath
	ctx := r.Context()

	// The router reaches the panel over the address this request came in on.
	// Without it the fetch URL would be empty and every router would fail.
	base := portalBaseURL(r)
	if base == "" {
		h.flashAndRedirect(w, r, back, "err",
			"The address of this panel could not be determined from the request, so the routers cannot be pointed at it. Open the panel through its real address and try again.")
		return
	}
	fetchURL := base + portalRouterLoginPath

	routers, err := h.db.Routers().List(ctx)
	if err != nil {
		h.fail(w, r, "load router inventory", err)
		return
	}
	if len(routers) == 0 {
		h.flashAndRedirect(w, r, back, "err",
			"No routers are registered yet, so there is nowhere to install the portal page. Add the hotspot under Routers first.")
		return
	}

	var installed, problems []string
	for _, router := range routers {
		client, err := h.dialRouter(ctx, router)
		if err != nil {
			problems = append(problems, router.Name+": unreachable ("+routerErrorHint(err)+")")
			continue
		}
		// The fetch is a round trip the router makes to the panel, so give it a
		// little more room than a plain API call needs.
		callCtx, cancel := context.WithTimeout(ctx, h.cfg.APITimeout+5*time.Second)
		result, installErr := client.InstallHotspotPortal(callCtx, fetchURL)
		cancel()
		client.Close()
		switch {
		case installErr != nil:
			problems = append(problems, router.Name+": "+routerErrorHint(installErr))
		case !result.Verified:
			problems = append(problems, router.Name+": the router reported no usable file at "+result.DestPath)
		default:
			installed = append(installed, router.Name)
		}
	}

	switch {
	case len(problems) == 0:
		h.flashAndRedirect(w, r, back, "ok",
			"Portal login page installed and verified on "+strconv.Itoa(len(installed))+
				" router(s). Guests now reach this panel, and future controller updates do not need this repeated.")
	case len(installed) > 0:
		h.flashAndRedirect(w, r, back, "warn",
			"Installed on "+strconv.Itoa(len(installed))+" router(s); problems: "+strings.Join(problems, "; "))
	default:
		h.flashAndRedirect(w, r, back, "err", "Could not install the portal page: "+strings.Join(problems, "; "))
	}
}

// PortalEditorBackground accepts a JPEG or PNG upload and stores it as the
// portal background.
func (h *Handler) PortalEditorBackground(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		h.portalEditorFormError(w, r, "The upload was too large or the form was malformed. Try a smaller JPEG or PNG.", http.StatusBadRequest)
		return
	}
	defer func() {
		// ParseMultipartForm spills anything over 4 MB into a temp file, which
		// has to be removed or the controller slowly fills /tmp.
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("background")
	if err != nil {
		h.portalEditorFormError(w, r, "Choose a JPEG or PNG file to upload.", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read one byte past the limit so an oversized file is caught here with a
	// helpful message instead of being truncated into a corrupt image.
	data, err := io.ReadAll(io.LimitReader(file, database.MaxPortalBackgroundBytes+1))
	if err != nil {
		h.portalEditorFormError(w, r, "The upload could not be read.", http.StatusBadRequest)
		return
	}
	switch {
	case len(data) == 0:
		h.portalEditorFormError(w, r, "That file is empty.", http.StatusBadRequest)
		return
	case len(data) > database.MaxPortalBackgroundBytes:
		h.portalEditorFormError(w, r, fmt.Sprintf("That image is larger than %d MB. Resize it before uploading.",
			database.MaxPortalBackgroundBytes/(1<<20)), http.StatusRequestEntityTooLarge)
		return
	}

	// The type is sniffed from the bytes, never taken from the browser: both
	// the .jpg extension and the multipart Content-Type are attacker
	// controlled, and this row is served back to every guest.
	contentType := http.DetectContentType(data)
	if !database.ValidPortalImageType(contentType) {
		h.portalEditorFormError(w, r,
			"Only JPEG (.jpg, .jpeg) and PNG (.png) images are accepted; that file is not one of them.",
			http.StatusUnsupportedMediaType)
		return
	}

	if err := h.db.PortalSettings().SetBackground(ctx, data, contentType, header.Filename); err != nil {
		h.fail(w, r, "save portal background", err)
		return
	}
	h.log.Info("portal background uploaded", "remote", clientIP(r),
		"bytes", len(data), "content_type", contentType)

	setFlash(w, "Background image updated.", "ok")
	http.Redirect(w, r, h.cfg.AdminPath+portalEditorPath, http.StatusSeeOther)
}

// PortalEditorBackgroundDelete removes the stored background image.
func (h *Handler) PortalEditorBackgroundDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "cannot read the form", http.StatusBadRequest)
		return
	}
	if err := h.db.PortalSettings().ClearBackground(r.Context()); err != nil {
		h.fail(w, r, "clear portal background", err)
		return
	}
	h.log.Info("portal background removed", "remote", clientIP(r))

	setFlash(w, "Background image removed.", "ok")
	http.Redirect(w, r, h.cfg.AdminPath+portalEditorPath, http.StatusSeeOther)
}

// portalEditorFormError re-renders the editor with a message in place of the
// uploaded file, which a file input cannot re-populate after a failed submit.
func (h *Handler) portalEditorFormError(w http.ResponseWriter, r *http.Request, message string, status int) {
	settings, err := h.db.PortalSettings().Get(r.Context())
	if err != nil {
		h.fail(w, r, "load portal settings", err)
		return
	}
	form := newPortalEditorForm()
	form.HeaderName = settings.HeaderName
	form.Theme = settings.Theme
	form.CustomHTML = settings.CustomHTML
	form.PageMode = settings.PageMode
	form.FullHTML = settings.FullHTML
	form.Errors["background"] = message

	h.render(w, r, status, "portal_editor.html", h.newPortalEditorView(settings, form))
}

// PortalBackground serves the stored background image to the captive pages.
//
// It is a GET on a public path. A guest that cannot fetch the photo still gets
// a working sign-in form, because the template treats a failed load as "no
// background" and falls back to the plain theme.
func (h *Handler) PortalBackground(w http.ResponseWriter, r *http.Request) {
	data, contentType, uploadedAt, err := h.db.PortalSettings().BackgroundImage(r.Context())
	if errors.Is(err, database.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.log.Error("cannot read portal background", "error", err)
		http.Error(w, "no background image", http.StatusInternalServerError)
		return
	}

	head := w.Header()
	head.Set("Content-Type", contentType)
	head.Set("Content-Length", strconv.Itoa(len(data)))
	// The image only changes when the operator uploads a new one, so let the
	// phone cache it rather than re-download megabytes on every portal load.
	head.Set("Cache-Control", "public, max-age=3600")
	// uploadedAt is stored to the second, which is a usable validator.
	head.Set("Last-Modified", uploadedAt.UTC().Format(http.TimeFormat))
	head.Set("ETag", fmt.Sprintf(`"%d-%d"`, uploadedAt.UTC().Unix(), len(data)))
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, head.Get("ETag")) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// portalBrandingFor loads the stored appearance and resolves it into the values
// the guest facing templates need.
//
// A database error degrades to the defaults instead of failing the page: a
// guest can do nothing about a read error on the branding row, and showing them
// a plain sign-in form is far better than an error page.
func (h *Handler) portalBrandingFor(ctx context.Context) portalBranding {
	settings, err := h.db.PortalSettings().Get(ctx)
	if err != nil {
		h.log.Warn("portal branding unavailable, using defaults", "error", err)
		settings = database.DefaultPortalSettings()
	}
	theme := portalThemeByKey(settings.Theme)
	branding := portalBranding{
		HeaderName: defaultValue(settings.HeaderName, h.cfg.PortalName),
		ThemeCSS:   theme.CSS,
		Theme:      theme.Key,
		CustomHTML: template.HTML(settings.CustomHTML),
	}
	if settings.HasBackground() {
		branding.BackgroundURL = portalBackgroundPath
	}
	return branding
}
