package tui

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aahoughton/agora/themes"
)

func defaultSkin(t *testing.T) *Skin {
	t.Helper()
	theme, err := themes.Open("")
	if err != nil {
		t.Fatal(err)
	}
	sk, err := LoadSkin(theme)
	if err != nil {
		t.Fatal(err)
	}
	return sk
}

func TestLoadSkinRejectsBadFiles(t *testing.T) {
	theme, err := themes.Open("")
	if err != nil {
		t.Fatal(err)
	}
	text, err := fs.ReadFile(theme, "tui/text.md")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := fs.ReadFile(theme, "tui/palette.md")
	if err != nil {
		t.Fatal(err)
	}
	files := func(text, palette string) fstest.MapFS {
		return fstest.MapFS{
			"tui/text.md":    {Data: []byte(text)},
			"tui/palette.md": {Data: []byte(palette)},
		}
	}
	tests := []struct {
		name string
		fs   fstest.MapFS
		want string
	}{
		{"missing text", files(strings.Replace(string(text), "## brand\n", "## gone\n", 1), string(palette)), `unknown key "gone"`},
		{"missing color", files(string(text), strings.Replace(string(palette), "## accent\n", "## other\n", 1)), `unknown key "other"`},
		{"bad color", files(string(text), strings.Replace(string(palette), "#4f46e5 #8b8cf6", "blue", 1)), "want two colors"},
		{"no palette", fstest.MapFS{"tui/text.md": {Data: text}}, "palette.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadSkin(tt.fs)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestPaletteMatchesStyleCSS keeps the default theme's TUI colors equal to
// the HTML viewer's, so a participant looks the same in both.
func TestPaletteMatchesStyleCSS(t *testing.T) {
	theme, err := themes.Open("")
	if err != nil {
		t.Fatal(err)
	}
	css, err := fs.ReadFile(theme, "static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	light, dark, ok := strings.Cut(string(css), "@media (prefers-color-scheme: dark)")
	if !ok {
		t.Fatal("style.css has no dark block")
	}
	sk := defaultSkin(t)
	for _, key := range paletteKeys {
		cssVar := strings.ReplaceAll(key, "_", "-")
		re := regexp.MustCompile(`--` + regexp.QuoteMeta(cssVar) + `:\s*(#[0-9a-fA-F]{6})`)
		for _, side := range []struct {
			name  string
			block string
			got   any
		}{{"light", light, sk.light[key]}, {"dark", dark, sk.dark[key]}} {
			m := re.FindStringSubmatch(side.block)
			if m == nil {
				t.Errorf("style.css has no %s --%s", side.name, cssVar)
				continue
			}
			if got := hex(side.got); !strings.EqualFold(got, m[1]) {
				t.Errorf("%s %s = %s, style.css has %s", side.name, key, got, m[1])
			}
		}
	}
}

func hex(c any) string {
	r, g, b, _ := c.(interface{ RGBA() (r, g, b, a uint32) }).RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}
