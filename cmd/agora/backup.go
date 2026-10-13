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

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
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
		fmt.Fprint(stderr, `usage: agora backup -o FILE [flags]

Downloads a consistent copy of the server's database (server admins).
To restore: stop the server, replace its database file with the copy,
and start it again. The file is written with mode 0600.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs)
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
		fmt.Fprintln(stderr, "agora backup:", err)
		return exitError
	}
	fmt.Fprintln(stdout, "wrote", *out)
	return exitOK
}

func runDump(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora dump (--namespace NAME | --all) -o FILE [flags]

Downloads a dump of one namespace (namespace admins) or of everything
(server admins), for agora load. A namespace dump carries conversations,
participants and messages without any account, role or token, so loading
it grants nothing. A full dump carries everything but browser sessions,
including token hashes. The file is written with mode 0600.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs) // --namespace names the namespace to dump
	all := fs.Bool("all", false, "dump everything instead of one namespace")
	out := fs.String("o", "", "file to write (required; must not exist)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	nsSet := false
	fs.Visit(func(f *flag.Flag) { nsSet = nsSet || f.Name == "namespace" })
	if fs.NArg() > 0 || *out == "" || *all == nsSet {
		return flagError(stderr, errors.New("give exactly one of --namespace NAME or --all, and -o FILE"))
	}
	cc, err := resolveClient(getenv)
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
	if err := writeNewFile(*out, func(w io.Writer) error { return cl.Dump(ctx, ns, w) }); err != nil {
		fmt.Fprintln(stderr, "agora dump:", err)
		return exitError
	}
	fmt.Fprintln(stdout, "wrote", *out)
	return exitOK
}

func runLoad(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora load --db PATH [-i FILE]
       agora load [--as NAME] [-i FILE]

With --db, creates a new database at PATH from a dump (agora dump), in one
transaction, then migrates it to this build's schema. PATH must not
exist. Point agora serve at it to use it.

Without --db, adds a namespace dump to a running server (--url, with a
server admin's --token), as namespace --as or the dump's own name, which
must not exist there yet.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs)
	dbPath := fs.String("db", "", "database file to create (must not exist)")
	as := fs.String("as", "", "without --db: the namespace's name on the server (default: the dump's)")
	in := fs.String("i", "", "dump file to read (default: standard input)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	src := stdin
	if *in != "" {
		f, err := os.Open(*in)
		if err != nil {
			fmt.Fprintln(stderr, "agora load:", err)
			return exitError
		}
		defer func() { _ = f.Close() }()
		src = f
	}
	if *dbPath == "" {
		cc, err := resolveClient(getenv)
		if err != nil {
			return flagError(stderr, err)
		}
		cl, err := client.New(cc.URL, cc.Token)
		if err != nil {
			return flagError(stderr, err)
		}
		res, err := cl.LoadNamespace(ctx, src, *as)
		if err != nil {
			fmt.Fprintln(stderr, "agora load:", err)
			return exitError
		}
		fmt.Fprintf(stdout, "loaded namespace %s into %s: %d conversations, %d messages\n",
			res.Namespace, cl.URL(), res.Conversations, res.Messages)
		return exitOK
	}
	if *as != "" {
		return flagError(stderr, errors.New("--as applies only when loading into a server (without --db)"))
	}
	if _, err := os.Lstat(*dbPath); err == nil {
		fmt.Fprintf(stderr, "agora load: %s exists; load creates a new database\n", *dbPath)
		return exitError
	}
	r := bufio.NewReaderSize(src, 1<<20)
	h, err := core.ReadDumpHeader(r, store.LatestVersion())
	if err != nil {
		fmt.Fprintln(stderr, "agora", err)
		return exitError
	}
	n, err := loadNew(ctx, *dbPath, h, r)
	if err != nil {
		fmt.Fprintln(stderr, "agora", err)
		return exitError
	}
	scope := "everything"
	if h.Namespace != "" {
		scope = "namespace " + h.Namespace
	}
	fmt.Fprintf(stdout, "loaded %d rows (%s, schema %d) into %s\n", n, scope, h.Schema, *dbPath)
	return exitOK
}

// loadNew builds the database in a private directory beside path, from a
// file it creates itself with mode 0600, and only then publishes it at
// path, never replacing a file that is there by then. A failed load leaves
// nothing behind, and nothing outside its own directory is opened or
// removed.
func loadNew(ctx context.Context, path string, h core.DumpHeader, r *bufio.Reader) (int, error) {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".agora-load-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tmp := filepath.Join(dir, "agora.db")
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
			return 0, fmt.Errorf("load: %s exists; load creates a new database", path)
		}
		return 0, err
	}
	return n, nil
}
