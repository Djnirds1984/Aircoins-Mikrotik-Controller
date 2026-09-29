package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// portalSettingsDDL creates the single row table backing the PORTAL editor.
//
// The image lives in the database next to the rest of the controller state
// instead of on disk: the captive portal is served by a single process, the
// file is bounded to a few megabytes, and this way a backup of the SQLite file
// is still a complete backup of the portal.
const portalSettingsDDL = `
CREATE TABLE IF NOT EXISTS portal_settings (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    theme           TEXT NOT NULL DEFAULT 'midnight',
    header_name     TEXT NOT NULL DEFAULT '',
    custom_html     TEXT NOT NULL DEFAULT '',
    background      BLOB,
    background_type TEXT NOT NULL DEFAULT '',
    background_name TEXT NOT NULL DEFAULT '',
    background_at   TEXT,
    updated_at      TEXT NOT NULL
)`

// Portal theme identifiers. The keys are persisted, so they must stay stable
// once shipped; the CSS that implements them lives in the handlers package.
const (
	PortalThemeMidnight = "midnight"
	PortalThemeOcean    = "ocean"
	PortalThemeSunset   = "sunset"
	PortalThemeForest   = "forest"
	PortalThemeLight    = "light"
)

// DefaultPortalTheme is used when nothing is stored yet.
const DefaultPortalTheme = PortalThemeMidnight

// PortalThemes lists every selectable theme in the order the editor shows them.
var PortalThemes = []string{
	PortalThemeMidnight,
	PortalThemeOcean,
	PortalThemeSunset,
	PortalThemeForest,
	PortalThemeLight,
}

// Limits for the operator supplied portal content.

// MaxPortalHeaderName is the longest header the portal will render. The name
// is shown in the brand line of a phone screen, so anything longer is a typo.
const MaxPortalHeaderName = 60

// MaxPortalCustomHTML bounds the extra markup. It is plenty for a terms box or
// a QR code and keeps a pasted base64 blob out of the database.
const MaxPortalCustomHTML = 64 << 10

// MaxPortalBackgroundBytes caps an uploaded background. A modern phone photo is
// 2-4 MB, so 6 MB accepts a full resolution JPEG without inviting a database
// full of posters.
const MaxPortalBackgroundBytes = 6 << 20

// ErrPortalTheme is returned when a theme key is not one of PortalThemes.
var ErrPortalTheme = errors.New("database: unknown portal theme")

// ValidPortalTheme reports whether key names a selectable theme.
func ValidPortalTheme(key string) bool {
	for _, theme := range PortalThemes {
		if theme == key {
			return true
		}
	}
	return false
}

// NormalizePortalTheme maps a stored or submitted value onto a known theme, so
// a renamed or hand-edited row can never break the portal stylesheet.
func NormalizePortalTheme(key string) string {
	if ValidPortalTheme(key) {
		return key
	}
	return DefaultPortalTheme
}

// ValidPortalImageType reports whether a sniffed content type may be stored as
// a portal background. Only JPEG and PNG are accepted: both are understood by
// every browser that reaches a hotspot, and both can be re-encoded to shrink a
// photo before upload.
func ValidPortalImageType(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/png":
		return true
	default:
		return false
	}
}

// PortalSettings is the operator's branding of the captive portal.
type PortalSettings struct {
	// Theme is one of PortalThemes.
	Theme string
	// HeaderName replaces the portal name in the guest facing brand line. An
	// empty value keeps the PORTAL_NAME from the environment.
	HeaderName string
	// CustomHTML is an extra block of markup rendered inside the portal card.
	// It is authored by an authenticated operator and rendered to
	// unauthenticated guests, so it is trusted input.
	CustomHTML string
	// Background is the raw image bytes, empty when none is stored.
	Background []byte
	// BackgroundType is the sniffed content type of Background.
	BackgroundType string
	// BackgroundName is the original file name, shown in the editor only.
	BackgroundName string
	// BackgroundAt is when the image was last replaced.
	BackgroundAt *time.Time
	// UpdatedAt covers the text fields.
	UpdatedAt time.Time
}

// HasBackground reports whether a background image is stored.
func (s PortalSettings) HasBackground() bool { return len(s.Background) > 0 }

// PortalSettingsStore persists the single portal appearance row.
type PortalSettingsStore struct{ db *DB }

// PortalSettings returns the captive portal appearance store.
func (db *DB) PortalSettings() *PortalSettingsStore { return &PortalSettingsStore{db: db} }

const portalSettingsColumns = `theme, header_name, custom_html, background,
    background_type, background_name, background_at, updated_at`

// DefaultPortalSettings is what a controller that was never configured serves:
// the built-in dark theme, the environment portal name and no extra content.
func DefaultPortalSettings() PortalSettings {
	return PortalSettings{Theme: DefaultPortalTheme}
}

// Get loads the portal appearance, falling back to the defaults when the row
// does not exist yet. A missing row is the normal state of a fresh install, so
// it is not an error.
func (s *PortalSettingsStore) Get(ctx context.Context) (PortalSettings, error) {
	settings := DefaultPortalSettings()
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT `+portalSettingsColumns+` FROM portal_settings WHERE id = 1`)
	var (
		theme         string
		headerName    string
		customHTML    string
		background    []byte
		backgroundTyp sql.NullString
		backgroundNam sql.NullString
		backgroundAt  sql.NullString
		updated       sql.NullString
	)
	err := row.Scan(&theme, &headerName, &customHTML, &background,
		&backgroundTyp, &backgroundNam, &backgroundAt, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return PortalSettings{}, wrapDBError("read portal settings", err)
	}
	settings.Theme = NormalizePortalTheme(theme)
	settings.HeaderName = headerName
	settings.CustomHTML = customHTML
	settings.Background = background
	settings.BackgroundType = backgroundTyp.String
	settings.BackgroundName = backgroundNam.String
	settings.BackgroundAt = parseStampPtr(backgroundAt)
	settings.UpdatedAt = parseStamp(updated)
	return settings, nil
}

// Save stores the text fields. The background image is deliberately left
// alone: it is replaced through SetBackground/ClearBackground so retyping a
// header name cannot silently drop the operator's photo.
//
// The upsert keeps the store usable on a database that has never had a portal
// row, which is every install created before this feature existed.
func (s *PortalSettingsStore) Save(ctx context.Context, settings PortalSettings) error {
	if !ValidPortalTheme(settings.Theme) {
		return fmt.Errorf("%w: %q", ErrPortalTheme, settings.Theme)
	}
	settings.HeaderName = strings.TrimSpace(settings.HeaderName)
	if len([]rune(settings.HeaderName)) > MaxPortalHeaderName {
		return fmt.Errorf("database: portal header name is longer than %d characters", MaxPortalHeaderName)
	}
	if len(settings.CustomHTML) > MaxPortalCustomHTML {
		return fmt.Errorf("database: portal custom HTML is larger than %d bytes", MaxPortalCustomHTML)
	}
	if _, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO portal_settings (id, theme, header_name, custom_html, updated_at)
        VALUES (1, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            theme       = excluded.theme,
            header_name = excluded.header_name,
            custom_html = excluded.custom_html,
            updated_at  = excluded.updated_at`,
		settings.Theme, settings.HeaderName, settings.CustomHTML,
		stamp(now())); err != nil {
		return wrapDBError("save portal settings", err)
	}
	return nil
}

// SetBackground replaces the background image.
//
// The bytes are validated here as well as in the handler: the store is the last
// line of defence, and a row carrying an executable content type would be
// served straight to every guest.
func (s *PortalSettingsStore) SetBackground(ctx context.Context, data []byte, contentType, name string) error {
	if len(data) == 0 {
		return errors.New("database: portal background is empty")
	}
	if len(data) > MaxPortalBackgroundBytes {
		return fmt.Errorf("database: portal background is larger than %d bytes", MaxPortalBackgroundBytes)
	}
	if !ValidPortalImageType(contentType) {
		return fmt.Errorf("database: unsupported portal background type %q (use JPEG or PNG)", contentType)
	}
	at := now()
	// The image columns are written in the same statement as the insert so a
	// first upload produces a complete row instead of a partial one.
	if _, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO portal_settings (id, theme, background, background_type, background_name, background_at, updated_at)
        VALUES (1, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            background      = excluded.background,
            background_type = excluded.background_type,
            background_name = excluded.background_name,
            background_at   = excluded.background_at,
            updated_at      = excluded.updated_at`,
		DefaultPortalTheme, data, contentType, strings.TrimSpace(name), stamp(at), stamp(at)); err != nil {
		return wrapDBError("save portal background", err)
	}
	return nil
}

// ClearBackground removes the stored image, leaving the text fields intact.
func (s *PortalSettingsStore) ClearBackground(ctx context.Context) error {
	if _, err := s.db.sql.ExecContext(ctx, `
        UPDATE portal_settings
           SET background = NULL, background_type = '', background_name = '',
               background_at = NULL, updated_at = ?
         WHERE id = 1`, stamp(now())); err != nil {
		return wrapDBError("clear portal background", err)
	}
	return nil
}

// BackgroundImage returns the stored image together with its content type and
// upload time. ErrNotFound is returned when no usable image is configured, so
// the handler can answer 404 instead of an empty 200.
func (s *PortalSettingsStore) BackgroundImage(ctx context.Context) ([]byte, string, time.Time, error) {
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT background, background_type, background_at FROM portal_settings WHERE id = 1`)
	var (
		raw      []byte
		typ      sql.NullString
		uploaded sql.NullString
	)
	err := row.Scan(&raw, &typ, &uploaded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, "", time.Time{}, wrapDBError("read portal background", err)
	}
	if len(raw) == 0 {
		return nil, "", time.Time{}, ErrNotFound
	}
	// A row written by an older build, or by hand, may carry a type the upload
	// path would never have accepted. Refuse it rather than echo it back with a
	// Content-Type the browser was never meant to see.
	if !ValidPortalImageType(typ.String) {
		return nil, "", time.Time{}, ErrNotFound
	}
	return raw, typ.String, parseStamp(uploaded), nil
}
