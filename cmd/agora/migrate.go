package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/aahoughton/agora/internal/store"
)

// exitPending is agora migrate --check's exit code when migrations are
// pending.
const exitPending = 3

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora migrate --db PATH [--check]

Brings an existing database to this build's schema, after copying it to
PATH.pre-vN (N its current schema version). agora serve does the same on
start; run this to upgrade as a separate step, for example before
starting a new container. With --check it only reports: exit 0 when the
database is current, 3 when migrations are pending.

`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", "", "the database file (required; it must exist)")
	check := fs.Bool("check", false, "report pending migrations without applying them")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if *dbPath == "" || fs.NArg() > 0 {
		return flagError(stderr, errors.New("--db PATH is required"))
	}
	current, err := store.Inspect(ctx, *dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "agora migrate:", err)
		return exitError
	}
	latest := store.LatestVersion()
	switch {
	case current > latest:
		fmt.Fprintf(stderr, "agora migrate: %s is at schema %d, newer than this build's %d; use a newer agora\n", *dbPath, current, latest)
		return exitError
	case current == latest:
		fmt.Fprintf(stdout, "%s is at schema %d, current\n", *dbPath, current)
		return exitOK
	case *check:
		fmt.Fprintf(stdout, "%s is at schema %d; %d migrations pending (to %d)\n", *dbPath, current, latest-current, latest)
		return exitPending
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	db, err := store.OpenWith(ctx, *dbPath, store.Options{Logger: logger, Snapshot: true})
	if err != nil {
		fmt.Fprintln(stderr, "agora migrate:", err)
		return exitError
	}
	if err := db.Close(); err != nil {
		fmt.Fprintln(stderr, "agora migrate:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "%s migrated from schema %d to %d; the previous version is in %s.pre-v%d\n", *dbPath, current, latest, *dbPath, current)
	if _, err := os.Stat(fmt.Sprintf("%s.pre-v%d", *dbPath, current)); err != nil {
		fmt.Fprintln(stdout, "(or a timestamped .pre-v file next to it)")
	}
	return exitOK
}
