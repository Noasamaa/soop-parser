package web

import "embed"

// Static holds the frontend assets.
//
//go:embed all:static
var Static embed.FS
