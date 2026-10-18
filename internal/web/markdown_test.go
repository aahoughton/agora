package web

import (
	"strings"
	"testing"
)

func TestMarkdownIsSafe(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{"raw html dropped", `hi <script>alert(1)</script> <b>x</b>`, nil, []string{"<script", "<b>"}},
		{"html block dropped", "<div onclick=\"x()\">\nhello\n</div>", nil, []string{"<div", "onclick"}},
		{"javascript link", `[click](javascript:alert(1))`, []string{"click"}, []string{"href", "javascript"}},
		{"data link", `[x](data:text/html,<script>)`, nil, []string{"href", "<script"}},
		{"relative link", `[x](/admin)`, []string{"x"}, []string{"href"}},
		{"https link kept", `[docs](https://example.com/a?b=1)`, []string{`<a href="https://example.com/a?b=1">docs</a>`}, nil},
		{"mailto kept", `<mailto:a@example.com>`, []string{`href="mailto:a@example.com"`}, nil},
		{"autolink", `see https://example.com`, []string{`href="https://example.com"`}, nil},
		{"image never loads", `![chart](https://example.com/c.png)`, []string{`href="https://example.com/c.png"`, "[image: chart]"}, []string{"<img"}},
		{"bad image source", `![x](javascript:alert(1))`, []string{"[image: x]"}, []string{"<img", "href", "javascript"}},
		{"attribute injection", `[x](https://e.com/"onmouseover="alert(1))`, nil, []string{`"onmouseover`}},
		{"code escaped", "`<b>`", []string{"<code>&lt;b&gt;</code>"}, nil},
		{"gfm table", "| a |\n|---|\n| 1 |", []string{"<table>"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := renderMarkdown(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			got := string(h)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in %s", w, got)
				}
			}
		})
	}
}

func TestInitials(t *testing.T) {
	for in, want := range map[string]string{"claude-implementer": "CI", "aah": "A", "codex_tests.v2": "CT", "x y z": "XY", "--": "?"} {
		if got := initials(in); got != want {
			t.Errorf("initials(%q) = %q, want %q", in, got, want)
		}
	}
}
