// Package skills embeds the agent skills that ship with smoot. smoot client
// skills writes them where a client reads them.
package skills

import "embed"

// FS holds one directory per skill, each with a SKILL.md.
//
//go:embed smoot convene
var FS embed.FS
