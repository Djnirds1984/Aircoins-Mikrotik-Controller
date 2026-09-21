package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// now returns the current time truncated to the second in UTC, which is the
// resolution of the stored TEXT timestamps.
func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// stamp formats a time for storage.
func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

// stampValue formats a nullable time for storage.
func stampValue(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return stamp(t)
}

func stampPtrValue(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return stamp(*t)
}

// parseStamp converts a stored TEXT timestamp back to a UTC time. Invalid or
// empty values yield the zero time so rendering never panics.
func parseStamp(v sql.NullString) time.Time {
	if !v.Valid || v.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, v.String)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// parseStampPtr is parseStamp for nullable columns.
func parseStampPtr(v sql.NullString) *time.Time {
	t := parseStamp(v)
	if t.IsZero() {
		return nil
	}
	return &t
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/PRIMARY KEY clash.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "constraint failed: unique")
}

// wrapDBError adds operation context to a database error while keeping
// errors.Is/As working for callers.
func wrapDBError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("database: %s: %w", op, err)
}
