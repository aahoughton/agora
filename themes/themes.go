// Package themes embeds the viewer's default theme. See README.md for the
// files a theme provides and the data its templates receive.
package themes

import "embed"

// Default holds the default theme under "default/".
//
//go:embed default
var Default embed.FS
