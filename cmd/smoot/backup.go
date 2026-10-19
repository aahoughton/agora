package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aahoughton/smoot/internal/client"
	"github.com/aahoughton/smoot/internal/config"
	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/store"
)

// writeNewFile writes a new file at path through fill: into a 0600 temporary
// file beside it, renamed into place only if fill succeeds. It refuses a
// path that exists.
func writeNewFile(path string, fill func(io.Writer) error) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s exists; choose another name", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".part-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	bw := bufio.NewWriter(tmp)
	if err := fill(bw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path) // fails rather than replace a file created meanwhile
}

func runBackup(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: smoot backup -o FILE [flags]

Downloads a consistent copy of the server's database (server admins).
To restore: stop the server, replace its database file with the copy,
and start it again. The file is written with mode 0600.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithToken)
	out := fs.String("o", "", "file to write (required; must not exist)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if *out == "" || fs.NArg() > 0 {
		return flagError(stderr, errors.New("-o FILE is required, and nothing else"))
	}
	cc, err := resolveClient(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	cl, err := client.New(cc.URL, cc.Token)
	if err != nil {
		return flagError(stderr, err)
	}
	if err := writeNewFile(*out, func(w io.Writer) error { return cl.Backup(ctx, w) }); err != nil {
		fmt.Fprintln(stderr, "smoot backup:", err)
		return exitError
	}
	fmt.Fprintln(stdout, "wrote", *out)
	return exitOK
}

func runDump(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: smoot dump [--namespace NAME | --all] -o FILE [flags]

Downloads a dump of one namespace (namespace admins) or of everything
(server admins), for smoot load or smoot db load. A namespace dump
carries conversations, participants and messages without any account,
role or token, so loading it grants nothing. A full dump carries everything but browser sessions,
including token hashes. The file is written with mode 0600; -o - writes
the dump to standard output instead.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace|config.WithToken)
	all := fs.Bool("all", false, "dump everything instead of one namespace")
	out := fs.String("o", "", "file to write (required; must not exist), or - for standard output")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	nsFlag := false
	fs.Visit(func(f *flag.Flag) { nsFlag = nsFlag || f.Name == "namespace" })
	if fs.NArg() > 0 || *out == "" {
		return flagError(stderr, errors.New("-o FILE is required, and nothing else"))
	}
	if *all && nsFlag {
		return flagError(stderr, errors.New("give --namespace or --all, not both"))
	}
	env := getenv
	if *all {
		// --all overrides a namespace from the environment, valid or not.
		env = func(k string) string {
			if k == config.EnvNamespace {
				return ""
			}
			return getenv(k)
		}
	}
	cc, err := resolveClient(env)
	if err != nil {
		return flagError(stderr, err)
	}
	cl, err := client.New(cc.URL, cc.Token)
	if err != nil {
		return flagError(stderr, err)
	}
	ns := ""
	if !*all {
		ns = cc.Namespace
	}
	if *out == "-" {
		if err := cl.Dump(ctx, ns, stdout); err != nil {
			fmt.Fprintln(stderr, "smoot dump:", err)
			return exitError
		}
		return exitOK
	}
	if err := writeNewFile(*out, func(w io.Writer) error { return cl.Dump(ctx, ns, w) }); err != nil {
		fmt.Fprintln(stderr, "smoot dump:", err)
		return exitError
	}
	fmt.Fprintln(stdout, "wrote", *out)
	return exitOK
}

func runLoad(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: smoot load [-i FILE] [--name NAME] [flags]

Adds a namespace dump (smoot dump --namespace) to a running server, with a
server admin's token, as namespace --name or the dump's own name, which must
not exist there yet. To make a new database file from a dump, use smoot db
load.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithToken)
	name := fs.String("name", "", "the namespace's name on the server (default: the dump's)")
	in := fs.String("i", "", "dump file to read (default: standard input)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	cc, err := resolveClient(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	cl, err := client.New(cc.URL, cc.Token)
	if err != nil {
		return flagError(stderr, err)
	}
	src, closeSrc, err := openInput(*in, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "smoot load:", err)
		return exitError
	}
	defer closeSrc()
	res, err := cl.LoadNamespace(ctx, src, *name)
	if err != nil {
		fmt.Fprintln(stderr, "smoot load:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "loaded namespace %s into %s: %d conversations, %d messages\n",
		res.Namespace, cl.URL(), res.Conversations, res.Messages)
	return exitOK
}

// openInput opens path, or returns stdin when path is empty.
func openInput(path string, stdin io.Reader) (io.Reader, func(), error) {
	if path == "" {
		return stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// loadNew builds the database in a private directory beside path, from a
// file it creates itself with mode 0600, and only then publishes it at
// path, never replacing a file that is there by then. A failed load leaves
// nothing behind, and nothing outside its own directory is opened or
// removed.
func loadNew(ctx context.Context, path string, h core.DumpHeader, r *bufio.Reader) (int, error) {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".smoot-load-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tmp := filepath.Join(dir, "smoot.db")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	db, err := store.OpenAt(ctx, tmp, h.Schema)
	if err != nil {
		return 0, err
	}
	n, err := core.LoadDump(ctx, db, h, r, time.Now())
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	db, err = store.Open(ctx, tmp) // migrate to this build's schema
	if err != nil {
		return 0, err
	}
	// Fold the write-ahead log into the file, which is all that is
	// published.
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = db.Close()
		return 0, err
	}
	if err := db.Close(); err != nil {
		return 0, err
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, fmt.Errorf("%s exists; db load creates a new database", path)
		}
		return 0, err
	}
	return n, nil
}
