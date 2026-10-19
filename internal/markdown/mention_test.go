package markdown

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/tone"
)

func TestMentions(t *testing.T) {
	people := NewPeople([]string{"ca-one", "both"}, []string{"cora", "both"})
	one := `<strong class="mention tone-` + strconv.Itoa(tone.Of("ca-one")) + `">@ca-one</strong>`
	both := `<strong class="mention tone-` + strconv.Itoa(tone.Of("both")) + `">@both</strong>`
	cora := `<strong class="mention observer">@cora</strong>`
	for _, tc := range []struct{ body, want string }{
		{"@ca-one please", "<p>" + one + " please</p>"},
		{"ask @ca-one.", "<p>ask " + one + ".</p>"},
		{"(@cora) and @ca-one", "<p>(" + cora + ") and " + one + "</p>"},
		{"**@ca-one** first", "<p><strong>" + one + "</strong> first</p>"},
		{"line @ca-one\nnext", "<p>line " + one + "\nnext</p>"},
		{"@both", "<p>" + both + "</p>"},
		{"@nobody here", "<p>@nobody here</p>"},
		{"@ca-onex", "<p>@ca-onex</p>"},
		{"me@ca-one", "<p>me@ca-one</p>"},
		{"`@ca-one`", "<p><code>@ca-one</code></p>"},
		{"```\n@ca-one\n```", "<pre><code>@ca-one\n</code></pre>"},
		{"[@ca-one](https://example.com)", `<p><a href="https://example.com">@ca-one</a></p>`},
		{`\@ca-one`, "<p>@ca-one</p>"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var b bytes.Buffer
			if err := Parser.Convert([]byte(tc.body), &b, WithPeople(people)); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(b.String()); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	t.Run("no people, no mentions", func(t *testing.T) {
		var b bytes.Buffer
		if err := Parser.Convert([]byte("@ca-one"), &b); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(b.String()); got != "<p>@ca-one</p>" {
			t.Fatalf("got %s", got)
		}
	})
}
