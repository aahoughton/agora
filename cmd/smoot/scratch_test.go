package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeRemovesStaleScratch(t *testing.T) {
	dir := t.TempDir()
	old, fresh, other := filepath.Join(dir, ".smoot-load-1"), filepath.Join(dir, ".smoot-backup-2"), filepath.Join(dir, "keep")
	for _, d := range []string{old, fresh, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * staleScratch)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, past, past); err != nil {
		t.Fatal(err)
	}
	removeStaleScratch(dir, time.Now(), slog.New(slog.DiscardHandler))
	for path, want := range map[string]bool{old: false, fresh: true, other: true} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", filepath.Base(path), err == nil, want)
		}
	}
}
