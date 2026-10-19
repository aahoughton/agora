package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/store"
)

const tinyDump = `{"format":"smoot-dump","version":1,"schema":2,"created_at":"x"}
{"table":"namespaces","row":{"id":1,"name":"default","created_at":1}}
{"end":true,"rows":1}
`

func loadFrom(t *testing.T, target, dump string) error {
	t.Helper()
	r := bufio.NewReader(strings.NewReader(dump))
	h, err := core.ReadDumpHeader(r, store.LatestVersion())
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadNew(t.Context(), target, h, r)
	return err
}

// A file that appears at the target while a load runs is never opened,
// replaced or removed.
func TestLoadNeverTouchesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "smoot.db")
	if err := os.WriteFile(target, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadFrom(t, target, tinyDump); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("err = %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "precious" {
		t.Fatalf("target now %q, %v", b, err)
	}
	// A failing load does not remove it either.
	if err := loadFrom(t, target, strings.Replace(tinyDump, `"rows":1`, `"rows":2`, 1)); err == nil {
		t.Fatal("bad dump loaded")
	}
	if b, _ := os.ReadFile(target); string(b) != "precious" {
		t.Fatal("a failed load removed the existing file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".smoot-load-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestLoadedDatabaseIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "smoot.db")
	if err := loadFrom(t, target, tinyDump); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("loaded database: %v %v", info, err)
	}
	db, err := store.Open(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM namespaces").Scan(&n); err != nil || n != 1 {
		t.Fatalf("namespaces = %d, %v", n, err)
	}
	_ = db.Close()
}
