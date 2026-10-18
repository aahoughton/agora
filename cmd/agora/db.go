package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

const dbUsage = `usage: agora db <command> --db PATH [flags]

Work on a database file directly, without a server. They take no server
flags; bootstrap and recover also work while the server runs.

  migrate --db PATH [--check]       upgrade the database to this build's schema
  load --db PATH [-i FILE]          create a new database from a dump
  bootstrap --db PATH --name NAME   create the first server admin and its token
  recover --db PATH --name NAME     re-enable an account as server admin, with a new token
`

func runDB(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, dbUsage)
		return exitUsage
	}
	switch args[0] {
	case "migrate":
		return runDBMigrate(ctx, args[1:], stdout, stderr)
	case "load":
		return runDBLoad(ctx, args[1:], stdin, stdout, stderr)
	case "bootstrap", "recover":
		return runDBAccount(ctx, args[0], args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, dbUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "agora db: unknown command %q\n\n%s", args[0], dbUsage)
		return exitUsage
	}
}

// exitPending is agora db migrate --check's exit code when migrations are
// pending.
const exitPending = 3

func runDBMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("db migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora db migrate --db PATH [--check]

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
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if *dbPath == "" {
		return flagError(stderr, errors.New("--db PATH is required"))
	}
	current, err := store.Inspect(ctx, *dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "agora db migrate:", err)
		return exitError
	}
	latest := store.LatestVersion()
	switch {
	case current > latest:
		fmt.Fprintf(stderr, "agora db migrate: %s is at schema %d, newer than this build's %d; use a newer agora\n", *dbPath, current, latest)
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
		fmt.Fprintln(stderr, "agora db migrate:", err)
		return exitError
	}
	if err := db.Close(); err != nil {
		fmt.Fprintln(stderr, "agora db migrate:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "%s migrated from schema %d to %d; the previous version is in %s.pre-v%d\n", *dbPath, current, latest, *dbPath, current)
	if _, err := os.Stat(fmt.Sprintf("%s.pre-v%d", *dbPath, current)); err != nil {
		fmt.Fprintln(stdout, "(or a timestamped .pre-v file next to it)")
	}
	return exitOK
}

func runDBLoad(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("db load", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora db load --db PATH [-i FILE]

Creates a new database at PATH from a dump (agora dump), in one
transaction, then migrates it to this build's schema. PATH must not
exist; point agora serve at it to use it. A backup (agora backup) needs no
loading: it is a database file already.

`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", "", "database file to create (required; must not exist)")
	in := fs.String("i", "", "dump file to read (default: standard input)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if *dbPath == "" {
		return flagError(stderr, errors.New("--db PATH is required"))
	}
	if _, err := os.Lstat(*dbPath); err == nil {
		fmt.Fprintf(stderr, "agora db load: %s exists; db load creates a new database\n", *dbPath)
		return exitError
	}
	src, closeSrc, err := openInput(*in, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "agora db load:", err)
		return exitError
	}
	defer closeSrc()
	r := bufio.NewReaderSize(src, 1<<20)
	h, err := core.ReadDumpHeader(r, store.LatestVersion())
	if err != nil {
		fmt.Fprintln(stderr, "agora db load:", strings.TrimPrefix(err.Error(), "load: "))
		return exitError
	}
	n, err := loadNew(ctx, *dbPath, h, r)
	if err != nil {
		fmt.Fprintln(stderr, "agora db load:", strings.TrimPrefix(err.Error(), "load: "))
		return exitError
	}
	scope := "everything"
	if h.Namespace != "" {
		scope = "namespace " + h.Namespace
	}
	fmt.Fprintf(stdout, "loaded %d rows (%s, schema %d) into %s\n", n, scope, h.Schema, *dbPath)
	return exitOK
}

func runDBAccount(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("db "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: agora db %s --db PATH --name NAME\n\n", cmd)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", "", "the server's database file (required; it must exist)")
	name := fs.String("name", "", "account name (required)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if *dbPath == "" || *name == "" {
		return flagError(stderr, errors.New("--db and --name are required"))
	}
	db, err := store.OpenExisting(ctx, *dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "agora db "+cmd+":", err)
		return exitError
	}
	defer func() { _ = db.Close() }()
	svc := core.New(db, core.Options{Policy: core.RolePolicy()})
	var tok core.IssuedToken
	if cmd == "bootstrap" {
		tok, err = svc.Bootstrap(ctx, *name)
	} else {
		tok, err = svc.Recover(ctx, *name)
	}
	if err != nil {
		fmt.Fprintln(stderr, "agora db "+cmd+":", err)
		return exitError
	}
	fmt.Fprintf(stdout, "%s is a server admin. Its token (shown once):\n%s\n", tok.Account, tok.Secret)
	return exitOK
}
