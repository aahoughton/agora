package markdown

import "testing"

func TestStripBidi(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                    "plain",
		"a\u202eb\u202cc":          "abc",
		"\u2066x\u2069 \u202ay":    "x y",
		"arabic \u0627\u0644 kept": "arabic \u0627\u0644 kept",
		"marks \u200f\u200e kept":  "marks \u200f\u200e kept",
	} {
		if got := StripBidi(in); got != want {
			t.Errorf("StripBidi(%q) = %q, want %q", in, got, want)
		}
	}
}
