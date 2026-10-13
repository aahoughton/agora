package store

import (
	"database/sql"
	"log/slog"
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

func TestSnapshotBeforeMigrating(t *testing.T) {
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
	_ = raw.Close()
	if v, err := Inspect(t.Context(), path); err != nil || v != 1 {
		t.Fatalf("inspect = %d, %v", v, err)
	}

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	db, err := OpenWith(t.Context(), path, Options{Logger: logger, Snapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	info, err := os.Stat(path + ".pre-v1")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot: %v %v", info, err)
	}
	if v, err := Inspect(t.Context(), path+".pre-v1"); err != nil || v != 1 {
		t.Fatalf("snapshot schema = %d, %v", v, err)
	}
	for _, want := range []string{"database snapshot before migrating", `msg="migrated database" migration=0002_accounts.sql schema=2`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}
	// Nothing pending: no new snapshot. A new database needs none either.
	db, err = OpenWith(t.Context(), path, Options{Logger: logger, Snapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if matches, _ := filepath.Glob(path + ".pre-v*"); len(matches) != 1 {
		t.Fatalf("snapshots = %v", matches)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".agora-snapshot-*")); len(left) != 0 {
		t.Fatalf("temporary snapshot files left: %v", left)
	}
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	db, err = OpenWith(t.Context(), fresh, Options{Snapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if matches, _ := filepath.Glob(fresh + ".pre-v*"); len(matches) != 0 {
		t.Fatalf("a new database got snapshots: %v", matches)
	}
	if _, err := Inspect(t.Context(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("inspected a missing file")
	}
}

// A second upgrade from the same version keeps the first snapshot.
func TestSnapshotNeverReplacesAnother(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	if err := os.WriteFile(path+".pre-v1", []byte("earlier"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenAt(t.Context(), path, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = OpenWith(t.Context(), path, Options{Snapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if b, _ := os.ReadFile(path + ".pre-v1"); string(b) != "earlier" {
		t.Fatal("the earlier snapshot was replaced")
	}
	if matches, _ := filepath.Glob(path + ".pre-v1-*"); len(matches) != 1 {
		t.Fatalf("snapshots = %v", matches)
	}
}

// A new database is private even in a directory others can read, and so
// are SQLite's files beside it.
func TestNewDatabaseIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agora.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), "INSERT INTO namespaces (name, created_at) VALUES ('x', 1)"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(f)
		if err != nil {
			continue // a sidecar may not exist
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s: mode %o", filepath.Base(f), perm)
		}
	}
}

func TestReadableDatabaseIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	db, err = OpenWith(t.Context(), path, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if !strings.Contains(logs.String(), "readable by other users") {
		t.Fatalf("no warning: %s", logs.String())
	}
}
