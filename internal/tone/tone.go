// Package tone maps a participant name to one of a fixed set of colors, so
// the same name has the same color in the HTML viewer and the TUI.
package tone

import "hash/fnv"

// Count is how many tones a theme defines (tone 0 to Count-1).
const Count = 8

// Of returns the tone for name, in [0, Count).
func Of(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32() % Count)
}
