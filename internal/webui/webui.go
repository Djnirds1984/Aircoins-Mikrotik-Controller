// Package webui embeds the panel's templates and static assets.
//
// The assets live beside this file so a single `go build` produces a binary with
// no runtime file dependencies, which matters for the SBC and mini PC targets.
package webui

import "embed"

// FS holds the admin panel templates and static files.
//
//go:embed admin/templates admin/static
var FS embed.FS
