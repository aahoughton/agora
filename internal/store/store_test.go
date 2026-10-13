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

// TestMigratesAVersionOneDatabase upgrades a database written by the first
// schema and checks that its rows survive with NULL accounts.
func TestMigratesAVersionOneDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path+"?"+dsnOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(t.Context(), raw, ms[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `
		INSERT INTO conversations (namespace_id, name, end_condition, created_by, created_at, last_seq, last_at)
			VALUES (1, 'c', 'none', 'alice', 1, 1, 1);
		INSERT INTO participants (conversation_id, agent, state, joined_at, last_seen_at) VALUES (1, 'alice', 'active', 1, 1);
		INSERT INTO messages (conversation_id, seq, kind, author, body, created_at) VALUES (1, 1, 'system', 'alice', 'alice joined', 1);
		INSERT INTO accounts (name, created_at) VALUES ('Pat', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	closeOnCleanup(t, db)
	if v, err := SchemaVersion(t.Context(), db); err != nil || v != len(ms) {
		t.Fatalf("schema version = %d, %v; want %d", v, err, len(ms))
	}
	var owner, creator, author sql.NullInt64
	var admin int
	err = db.QueryRowContext(t.Context(), `SELECT p.account_id, c.created_by_account, m.account_id, a.server_admin
		FROM participants p, conversations c, messages m, accounts a`).Scan(&owner, &creator, &author, &admin)
	if err != nil || owner.Valid || creator.Valid || author.Valid || admin != 0 {
		t.Fatalf("migrated row = %v %v %v %d, %v", owner, creator, author, admin, err)
	}
	// Account names are unique regardless of case.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO accounts (name, created_at) VALUES ('pat', 2)`); err == nil {
		t.Fatal("a second account named pat was accepted")
	}
}

func TestOpenExistingRefusesMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenExisting(t.Context(), filepath.Join(dir, "missing.db")); err == nil {
		t.Fatal("opened a missing file")
	}
	if _, err := OpenExisting(t.Context(), dir); err == nil {
		t.Fatal("opened a directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.db")); !os.IsNotExist(err) {
		t.Fatalf("a file was created: %v", err)
	}
	path := filepath.Join(dir, "agora.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}
