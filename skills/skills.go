// Package skills embeds the agent skills that ship with agora. agora skills
// install writes them where a client reads them.
package skills

import "embed"

// FS holds one directory per skill, each with a SKILL.md.
//
//go:embed agora convene
var FS embed.FS
