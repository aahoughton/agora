package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func plain(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return strings.Join(out, "\n")
}

func TestRenderBody(t *testing.T) {
	st := defaultSkin(t).styles(true)
	tests := []struct {
		name, body, want string
	}{
		{"paragraphs", "one\ntwo\n\nthree", "one two\n\nthree"},
		{"emphasis and code", "**bold** _it_ `code` ~~gone~~", "bold it code gone"},
		{"heading", "## Plan\nbody", "Plan\n\nbody"},
		{"tight list", "- a\n- b", "• a\n• b"},
		{"ordered list", "3. a\n4. b", "3. a\n4. b"},
		{"tasks", "- [x] done\n- [ ] todo", "• [x] done\n• [ ] todo"},
		{"quote", "> quoted", "│ quoted"},
		{"code block", "```go\nfunc f() {\n\treturn\n}\n```", "  func f() {\n      return\n  }"},
		{"link", "[spec](https://example.com/s)", "spec (https://example.com/s)"},
		{"bare link", "<https://example.com>", "https://example.com"},
		{"blocked link", "[x](javascript:alert(1))", "x"},
		{"image", "![chart](https://example.com/c.png)", "[image: chart] (https://example.com/c.png)"},
		{"raw html shown as text", "<b>hi</b>", "<b>hi</b>"},
		{"table", "| a | b |\n|---|---|\n| 1 | 2 |", "a │ b\n1 │ 2"},
		{"rule", "a\n\n---\n\nb", "a\n\n" + strings.Repeat("─", 40) + "\n\nb"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plain(renderBody(tt.body, 50, st)); got != tt.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestRenderBodyWraps(t *testing.T) {
	st := defaultSkin(t).styles(true)
	body := "a **bold run that is long enough to wrap** then `some code that also wraps` and " +
		strings.Repeat("x", 50) + "\n\n```\n" + strings.Repeat("y", 50) + "\n```"
	for _, l := range renderBody(body, 20, st) {
		if w := ansi.StringWidth(l); w > 20 {
			t.Errorf("line %q is %d wide", ansi.Strip(l), w)
		}
		// Every line closes what it opens, so the next line (and the
		// gutter in front of it) starts unstyled.
		if i := strings.LastIndex(l, "\x1b["); i >= 0 && !strings.HasPrefix(l[i:], "\x1b[m") && !strings.HasPrefix(l[i:], "\x1b[0m") {
			t.Errorf("line %q leaves a style open", l)
		}
	}
}

func TestSanitize(t *testing.T) {
	in := "ok\x1b[2J\x1b]8;;evil\x07 done\r\n\ttab \u202eflip\u0085"
	if got, want := sanitize(in), "ok[2J]8;;evil done\n    tab flip"; got != want {
		t.Fatalf("sanitize = %q, want %q", got, want)
	}
	for _, l := range renderBody(in, 40, defaultSkin(t).styles(true)) {
		if strings.ContainsAny(ansi.Strip(l), "\x1b\x07\r") {
			t.Fatalf("control character survived: %q", l)
		}
	}
}

func TestRenderBodyDeepNesting(t *testing.T) {
	st := defaultSkin(t).styles(true)
	for _, body := range []string{
		strings.Repeat("> ", 30) + "---",
		strings.Repeat("> ", 30) + "text that has to go somewhere",
		"- a\n" + strings.Repeat("  ", 1) + "- b\n    - c\n      - d\n        - e\n          - f\n            - g\n              - " + strings.Repeat("x", 30),
	} {
		for _, l := range renderBody(body, 12, st) {
			if w := ansi.StringWidth(l); w > 12 {
				t.Errorf("line %q is %d wide", ansi.Strip(l), w)
			}
		}
	}
}
