package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func closeOnCleanup(t *testing.T, db *sql.DB) {
	t.Helper()
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
}

func TestOpenCreatesAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "agora.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	closeOnCleanup(t, db)

	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v, err := SchemaVersion(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if v != len(ms) {
		t.Fatalf("schema version = %d, want %d", v, len(ms))
	}

	var name string
	if err := db.QueryRowContext(t.Context(), "SELECT name FROM namespaces").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "default" {
		t.Fatalf("namespace = %q", name)
	}

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir mode = %o", perm)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	for range 2 {
		db, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM namespaces").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("namespaces = %d", n)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenSettings(t *testing.T) {
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	closeOnCleanup(t, db)

	var mode string
	if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q", mode)
	}
	var fk int
	if err := db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d", fk)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsBadPaths(t *testing.T) {
	for _, p := range []string{"", "a?b.db", "a#b.db"} {
		if _, err := Open(t.Context(), p); err == nil {
			t.Errorf("Open(%q) succeeded", p)
		}
	}
}
