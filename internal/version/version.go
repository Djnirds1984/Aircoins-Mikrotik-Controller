// Package version exposes build metadata stamped in at link time.
package version

import "fmt"

// These values are overridden by the Makefile using -ldflags -X.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// String returns the short version, e.g. "1.2.3".
func String() string { return Version }

// Full returns a human readable version line for logs and the admin footer.
func Full() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, BuildDate)
}
