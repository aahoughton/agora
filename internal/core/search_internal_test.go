package core

import "testing"

func TestFTSQuery(t *testing.T) {
	for in, want := range map[string]string{
		"migration":           `"migration"`,
		"  two   words ":      `"two" "words"`,
		`"a phrase" word`:     `"a phrase" "word"`,
		"migrat*":             `"migrat"*`,
		`say"quote`:           `"sayquote"`,
		`"unclosed phrase`:    `"unclosed phrase"`,
		"OR NOT AND":          `"OR" "NOT" "AND"`,
		"body:x NEAR(a b)":    `"body:x" "NEAR(a" "b)"`,
		"seen_seq":            `"seen_seq"`,
		`"it ""works"" fine"`: `"it " "works" " fine"`,
		"!! word ** ?":        `"word"`,
	} {
		got, err := ftsQuery(in)
		if err != nil || got != want {
			t.Errorf("ftsQuery(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
}
