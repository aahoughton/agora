// Package themes embeds the default theme for the HTML viewer and the TUI.
// See README.md for the files a theme provides.
package themes

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Default holds the default theme under "default/".
//
//go:embed default
var Default embed.FS

// Open returns the theme to render: the default theme, with files from dir
// (if not empty) replacing the default file at the same path.
func Open(dir string) (fs.FS, error) {
	base, err := fs.Sub(Default, "default")
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return base, nil
	}
	// os.Root confines reads to the directory: symlinks that point outside
	// it are refused, so a theme cannot serve other files.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("theme dir %q: %w", dir, err)
	}
	return overlay{top: root.FS(), base: base}, nil
}

// overlay serves files from top, falling back to base.
type overlay struct{ top, base fs.FS }

func (o overlay) Open(name string) (fs.File, error) {
	f, err := o.top.Open(name)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return o.base.Open(name)
}
