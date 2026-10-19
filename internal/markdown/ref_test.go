package markdown

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestRefs(t *testing.T) {
	refs := Refs{Below: 10, Link: func(seq int64) string { return "/m?n=" + strconv.FormatInt(seq, 10) + "&x" }}
	ref := func(n string) string { return `<a class="ref" href="/m?n=` + n + `&amp;x">#` + n + `</a>` }
	for _, tc := range []struct{ body, want string }{
		{"AGREED: #5", "<p>AGREED: " + ref("5") + "</p>"},
		{"#5 and #9.", "<p>" + ref("5") + " and " + ref("9") + ".</p>"},
		{"(#3)", "<p>(" + ref("3") + ")</p>"},
		{"#10 is not yet", "<p>#10 is not yet</p>"},
		{"#0 #05", "<p>#0 #05</p>"},
		{"repo#5 a/b#5", "<p>repo#5 a/b#5</p>"},
		{"#4/#5", "<p>" + ref("4") + "/" + ref("5") + "</p>"},
		{"#3-#5", "<p>" + ref("3") + "-" + ref("5") + "</p>"},
		{"x/#5 x-#5 #4.#5", "<p>x/#5 x-#5 " + ref("4") + ".#5</p>"},
		{"*#3*/#4", "<p><em>" + ref("3") + "</em>/" + ref("4") + "</p>"},
		{"**#3**-#4", "<p><strong>" + ref("3") + "</strong>-" + ref("4") + "</p>"},
		{"#3 /#4", "<p>" + ref("3") + " /#4</p>"},
		{"`#3`/#4", "<p><code>#3</code>/#4</p>"},
		{"#3\n/#4", "<p>" + ref("3") + "\n/#4</p>"},
		{"#3\n\n/#4", "<p>" + ref("3") + "</p>\n<p>/#4</p>"},
		{"#5a", "<p>#5a</p>"},
		{"`#5`", "<p><code>#5</code></p>"},
		{"[#5](https://example.com)", `<p><a href="https://example.com">#5</a></p>`},
		{"# 5", "<h1>5</h1>"},
		{"it&#39;s #5", "<p>it's " + ref("5") + "</p>"},
		{"&#35;5", "<p>#5</p>"},
		{`\#5`, "<p>#5</p>"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var b bytes.Buffer
			if err := Parser.Convert([]byte(tc.body), &b, With(nil, refs)); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(b.String()); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	t.Run("no refs", func(t *testing.T) {
		var b bytes.Buffer
		if err := Parser.Convert([]byte("#5"), &b); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(b.String()); got != "<p>#5</p>" {
			t.Fatalf("got %s", got)
		}
	})
}
