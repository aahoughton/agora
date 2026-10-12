package tone

import "testing"

func TestOf(t *testing.T) {
	// The mapping is part of what users see: a participant keeps its color
	// across releases and between the HTML viewer and the TUI.
	for name, want := range map[string]int{"claude-implementer": 4, "codex-reviewer": 0, "alice": 7} {
		if got := Of(name); got != want {
			t.Errorf("Of(%q) = %d, want %d", name, got, want)
		}
	}
	seen := map[int]bool{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		seen[Of(n)] = true
	}
	if len(seen) < 4 {
		t.Fatalf("tones barely vary: %v", seen)
	}
}
