// Package store opens the SQLite database and applies schema migrations.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// dsnOptions configure every connection. Write transactions take the write
// lock at BEGIN (_txlock=immediate) so two writers never deadlock upgrading a
// read lock.
const dsnOptions = "_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=1&_txlock=immediate"

// Open opens (creating if needed) the database at path and migrates it to the
// latest schema. The parent directory is created with mode 0700.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	return OpenAt(ctx, path, 0)
}

// Options configure OpenWith.
type Options struct {
	// Logger, if set, gets a line for each migration applied and for the
	// snapshot.
	Logger *slog.Logger
	// Snapshot copies an existing database to PATH.pre-vN (N its schema
	// version) before applying pending migrations, so an upgrade can be
	// undone by putting the copy back and running the older build.
	Snapshot bool
}

// OpenWith is Open with options; agora serve and agora db migrate use it.
func OpenWith(ctx context.Context, path string, opts Options) (*sql.DB, error) {
	return openAt(ctx, path, 0, opts)
}

// OpenAt is Open, migrating only up to schema version (0 for the latest).
// Loading a dump uses it to create a database at the dump's schema.
func OpenAt(ctx context.Context, path string, version int) (*sql.DB, error) {
	return openAt(ctx, path, version, Options{})
}

func openAt(ctx context.Context, path string, version int, opts Options) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: database path %q must not contain '?' or '#'", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create database directory: %w", err)
	}
	// SQLite would create the file with the process umask (often 0644) and
	// give its -wal and -shm files the same mode; create it private first.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case err == nil:
		if err := f.Close(); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrExist):
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 && opts.Logger != nil {
			opts.Logger.Warn("database file is readable by other users; chmod 600 it and its -wal and -shm files",
				"path", path, "mode", fmt.Sprintf("%o", info.Mode().Perm()))
		}
	default:
		return nil, fmt.Errorf("store: create %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", fileURI(path, dsnOptions))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := migrate(ctx, db, path, version, opts); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Inspect returns the schema version of the existing database at path
// without changing it.
func Inspect(ctx context.Context, path string) (int, error) {
	if strings.ContainsAny(path, "?#") {
		return 0, fmt.Errorf("store: database path %q must not contain '?' or '#'", path)
	}
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("store: %w", err)
	}
	db, err := sql.Open("sqlite", fileURI(path, "mode=ro&_busy_timeout=5000"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	return SchemaVersion(ctx, db)
}

// Pending reports the database's schema version and how many migrations
// this build would apply to it.
func Pending(ctx context.Context, db *sql.DB) (current, pending int, err error) {
	current, err = SchemaVersion(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	return current, max(LatestVersion()-current, 0), nil
}

// snapshot copies the database to path.pre-vN (or with a timestamp if that
// exists), mode 0600. The copy is made in a private directory beside the
// database and only then published, never over an existing file; nothing is
// left behind if it fails.
func snapshot(ctx context.Context, db *sql.DB, path string, version int) (string, error) {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".agora-snapshot-")
	if err != nil {
		return "", fmt.Errorf("store: snapshot before migrating: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tmp := filepath.Join(dir, "agora.db")
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return "", fmt.Errorf("store: snapshot before migrating: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return "", err
	}
	dst := fmt.Sprintf("%s.pre-v%d", path, version)
	if err := os.Link(tmp, dst); errors.Is(err, os.ErrExist) {
		dst = fmt.Sprintf("%s.pre-v%d-%d", path, version, time.Now().UnixNano())
		if err := os.Link(tmp, dst); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return dst, nil
}

// LatestVersion is the schema version this build migrates to.
func LatestVersion() int {
	ms, err := loadMigrations()
	if err != nil {
		panic(err) // the embedded migrations are checked by tests
	}
	return len(ms)
}

// OpenExisting opens the existing database at path without changing its
// schema. It refuses a path that does not name an existing regular file, so
// a mistyped path is an error instead of a new, empty database, and a
// database whose schema is not this build's: only agora serve and agora
// migrate migrate.
func OpenExisting(ctx context.Context, path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("store: %s is not a database file", path)
	}
	v, err := Inspect(ctx, path)
	if err != nil {
		return nil, err
	}
	if latest := LatestVersion(); v != latest {
		return nil, fmt.Errorf("store: %s is at schema %d and this build needs %d; run agora db migrate --db %s (or use the matching build)", path, v, latest, path)
	}
	return Open(ctx, path)
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q: want NNNN_name.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("store: migration %q: %w", e.Name(), err)
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration %q: expected version %d", m.name, i+1)
		}
	}
	return ms, nil
}

// SchemaVersion reports the schema version recorded in the database.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

func migrate(ctx context.Context, db *sql.DB, path string, target int, opts Options) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if target < 0 || target > len(ms) {
		return fmt.Errorf("store: schema version %d is not one this binary knows (1-%d)", target, len(ms))
	}
	if target > 0 {
		ms = ms[:target]
	}
	current, err := SchemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(ms) {
		return fmt.Errorf("store: database schema version %d is newer than this binary supports (%d)", current, len(ms))
	}
	if opts.Snapshot && current > 0 && current < len(ms) {
		dst, err := snapshot(ctx, db, path, current)
		if err != nil {
			return err
		}
		if opts.Logger != nil {
			opts.Logger.Info("database snapshot before migrating", "path", dst, "schema", current)
		}
	}
	for _, m := range ms[current:] {
		if err := apply(ctx, db, m); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		if opts.Logger != nil {
			opts.Logger.Info("migrated database", "migration", m.name, "schema", m.version)
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	// PRAGMA does not accept bound parameters; version is an int we control.
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
		return err
	}
	return tx.Commit()
}
