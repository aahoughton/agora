package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// mentionCycle is a completion in progress: tab again replaces the
// inserted name with the next match, as long as the composer still holds
// exactly what the last completion left there.
type mentionCycle struct {
	matches []string
	i       int
	start   int  // rune offset in the value where the name begins
	space   bool // a space follows the name
	value   string
	cursor  int // rune offset of the cursor after the completion
}

func nameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}

// mentionPrefix returns the name typed after an "@" that ends before, so
// "see @cod" gives "cod". The "@" must start the text or follow a character
// that cannot be in a name, so an email address is not a mention.
func mentionPrefix(before []rune) (string, bool) {
	i := len(before)
	for i > 0 && nameRune(before[i-1]) {
		i--
	}
	if i == 0 || before[i-1] != '@' || (i > 1 && nameRune(before[i-2])) {
		return "", false
	}
	return string(before[i:]), true
}

// mentionMatches returns the names starting with prefix, ignoring case, in
// the order given and without repeats.
func mentionMatches(prefix string, names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if !seen[n] && strings.HasPrefix(strings.ToLower(n), strings.ToLower(prefix)) {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// complete handles tab in the composer: it completes an @name from the
// conversation's participants, then its observers, or cycles to the next
// match. It reports false when there is nothing to complete, including a
// further tab on a single match.
func (m *Model) complete() bool {
	t := m.t
	value := t.composer.Value()
	cursor := cursorOffset(value, t.composer.Line(), t.composer.Column())
	if c := t.mention; c != nil && c.value == value && c.cursor == cursor {
		if len(c.matches) == 1 {
			t.mention = nil
			return false
		}
		c.i = (c.i + 1) % len(c.matches)
		m.fill(c, value, cursor)
		return true
	}
	t.mention = nil
	runes := []rune(value)
	prefix, ok := mentionPrefix(runes[:cursor])
	if !ok {
		return false
	}
	var names []string
	for _, p := range t.conv.Participants {
		names = append(names, p.Agent)
	}
	for _, o := range t.conv.Observers {
		names = append(names, o.Name)
	}
	matches := mentionMatches(prefix, names)
	if len(matches) == 0 {
		return false
	}
	next := cursor < len(runes) && (runes[cursor] == ' ' || runes[cursor] == '\n')
	c := &mentionCycle{matches: matches, start: cursor - len([]rune(prefix)), space: !next}
	m.fill(c, value, cursor)
	t.mention = c
	return true
}

// fill replaces the runes from c.start to cursor with the current match and
// leaves the cursor after it. Setting the whole value also drops any
// selection, so nothing outside that span is touched.
func (m *Model) fill(c *mentionCycle, value string, cursor int) {
	runes := []rune(value)
	name := c.matches[c.i]
	if c.space {
		name += " "
	}
	head := string(runes[:c.start]) + name
	tail := string(runes[cursor:])
	t := m.t
	t.composer.SetValue(head)
	t.composer.InsertString(tail)
	for range len(runes[cursor:]) {
		t.composer, _ = t.composer.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	c.value = head + tail
	c.cursor = len([]rune(head))
}

// cursorOffset converts the textarea's row and column to a rune offset in
// value.
func cursorOffset(value string, row, col int) int {
	off := 0
	for i, line := range strings.Split(value, "\n") {
		n := len([]rune(line))
		if i == row {
			return off + min(col, n)
		}
		off += n + 1
	}
	return len([]rune(value))
}
