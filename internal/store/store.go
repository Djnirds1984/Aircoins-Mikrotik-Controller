// Package store is the persistence layer. All SQL for the panel lives here.
package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Sentinel errors returned by the store.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict means a uniqueness constraint was violated.
	ErrConflict = errors.New("store: conflict")
)

// timeLayout is the storage format for timestamps. Values are always written in
// UTC and parsed explicitly, so behaviour does not depend on driver defaults.
const timeLayout = "2006-01-02 15:04:05"

// nowStamp returns the current UTC time in storage format.
func nowStamp() string { return time.Now().UTC().Format(timeLayout) }

// parseTimeParsed parses a stored timestamp, returning the zero time when the
// value is empty or unparseable.
func parseTimeParsed(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{timeLayout, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// nullTimePtr parses a nullable stored timestamp, returning nil when NULL.
func nullTimePtr(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	return parseTimePtr(v.String)
}

// parseTimePtr parses a stored timestamp, returning nil when absent.
func parseTimePtr(s string) *time.Time {
	t := parseTimeParsed(s)
	if t.IsZero() {
		return nil
	}
	return &t
}

// stamp renders a time for storage.
func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

// isUniqueViolation reports whether an error is a SQLite uniqueness failure.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
