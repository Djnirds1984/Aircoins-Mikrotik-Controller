package routeros

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Sentinel errors that callers can test with errors.Is. They let the HTTP layer
// turn device problems into actionable messages instead of raw !trap text.
var (
	// ErrAuth means the API username/password was rejected.
	ErrAuth = errors.New("routeros: authentication failed")
	// ErrPermission means the account authenticated but may not run the command.
	ErrPermission = errors.New("routeros: account lacks permission")
	// ErrHostUnknown is returned by /ip/hotspot/active/login when the client IP
	// is not present in the hotspot host table yet.
	ErrHostUnknown = errors.New("routeros: unknown hotspot host")
	// ErrNoMenu means the command does not exist on this RouterOS version,
	// most often because the hotspot package is disabled.
	ErrNoMenu = errors.New("routeros: command unavailable on this device")
	// ErrTimeout means the device did not answer in time.
	ErrTimeout = errors.New("routeros: device timed out")
	// ErrUnreachable means the TCP connection could not be established.
	ErrUnreachable = errors.New("routeros: device unreachable")
)

// DeviceError is a !trap or !fatal sentence returned by RouterOS.
type DeviceError struct {
	// Message is the RouterOS error text, e.g. "unknown host IP 0.0.0.0".
	Message string
	// Category is the RouterOS error category ("0".."7") when supplied.
	Category string
	// Sentinel is the classified sentinel this error matches, if any.
	Sentinel error

	wrapped error
}

func (e *DeviceError) Error() string {
	if e.Category != "" {
		return fmt.Sprintf("routeros device error (category %s): %s", e.Category, e.Message)
	}
	return "routeros device error: " + e.Message
}

func (e *DeviceError) Unwrap() error { return e.wrapped }

// Is allows errors.Is(err, ErrAuth) style checks.
func (e *DeviceError) Is(target error) bool {
	if target == nil {
		return false
	}
	if e.Sentinel != nil && target == e.Sentinel {
		return true
	}
	return errors.Is(e.wrapped, target)
}

// classifyDevice maps RouterOS error text onto a sentinel error.
//
// RouterOS supplies a machine readable category but it is coarse, so the text is
// also inspected. Categories: 0 missing item/command, 1 argument value failure,
// 2 interrupted, 3 scripting failure, 4 general failure, 5 API failure.
func classifyDevice(msg, category string) error {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "cannot log in"),
		strings.Contains(lower, "invalid user name or password"),
		strings.Contains(lower, "invalid username or password"):
		return ErrAuth
	case strings.Contains(lower, "not enough permissions"),
		strings.Contains(lower, "not allowed"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "prohibited"):
		return ErrPermission
	case strings.Contains(lower, "unknown host"):
		return ErrHostUnknown
	case strings.Contains(lower, "no such command"),
		strings.Contains(lower, "bad command name"),
		strings.Contains(lower, "unknown command"),
		strings.Contains(lower, "syntax error"):
		return ErrNoMenu
	}
	if category == "3" {
		return ErrPermission
	}
	return nil
}

// wrapDialError turns a connection failure into a sentinel error with detail.
func wrapDialError(address string, err error) error {
	if err == nil {
		return nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: no response from %s within the timeout", ErrTimeout, address)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s", ErrTimeout, address)
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "cannot log in"),
		strings.Contains(lower, "invalid user name or password"):
		return fmt.Errorf("%w: %v", ErrAuth, err)
	case strings.Contains(lower, "connection refused"):
		return fmt.Errorf("%w: %s refused the connection (is the API service enabled?)", ErrUnreachable, address)
	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "no address associated"):
		return fmt.Errorf("%w: %s could not be resolved", ErrUnreachable, address)
	case strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "deadline exceeded"):
		return fmt.Errorf("%w: %s", ErrTimeout, address)
	}
	return fmt.Errorf("%w: %s: %v", ErrUnreachable, address, err)
}
