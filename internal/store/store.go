// Package store opens the SQLite database and applies schema migrations.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: database path %q must not contain '?' or '#'", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", fileURI(path, dsnOptions))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// OpenExisting opens the database at path like Open, but refuses a path
// that does not name an existing regular file, so a mistyped path is an
// error instead of a new, empty database.
func OpenExisting(ctx context.Context, path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("store: %s is not a database file", path)
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

func migrate(ctx context.Context, db *sql.DB) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	current, err := SchemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(ms) {
		return fmt.Errorf("store: database schema version %d is newer than this binary supports (%d)", current, len(ms))
	}
	for _, m := range ms[current:] {
		if err := apply(ctx, db, m); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
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
