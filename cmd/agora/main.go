// Command agora is a conversation service for agents and the people watching
// them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/server"
	"github.com/aahoughton/agora/internal/store"
	"github.com/aahoughton/agora/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: agora <command> [flags]

commands:
  serve       run the server (JSON API at /v1, MCP at /mcp)
  mcp         serve MCP on stdio, relaying to a running server
  wait        block until there is something new for an agent
  status      show where an agent is expected
  tui         browse and follow conversations in the terminal
  mcp-config  print the setup for an agent client (claude, codex)
  skills      install agora's agent skills (skills install)
  hook        run as an agent client hook (hook stop)
  whoami      show the server and who your token belongs to
  admin       manage accounts, tokens and members (agora admin help)
  backup      download a copy of the server's database
  dump        download a dump of a namespace or of everything
  load        create a database from a dump, or add a namespace to a server
  migrate     upgrade a database to this build's schema
  health      check that the server is up
  version     print the version

Run 'agora <command> -h' for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv config.Getenv) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		cfg, err := config.ParseServe(args[1:], getenv, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		logger := cfg.NewLogger(stderr)
		if err := serve(ctx, cfg, logger, nil); err != nil {
			logger.Error("serve failed", "err", err)
			return 1
		}
		return 0
	case "mcp":
		return runMCP(ctx, args[1:], stderr, getenv)
	case "wait":
		return runWait(ctx, args[1:], stdout, stderr, getenv)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr, getenv)
	case "mcp-config":
		return runMCPConfig(args[1:], stdout, stderr, getenv)
	case "skills":
		return runSkills(args[1:], stdout, stderr)
	case "hook":
		return runHook(ctx, args[1:], stdin, stdout, stderr, getenv)
	case "tui":
		return runTUI(ctx, args[1:], stderr, getenv)
	case "whoami":
		return runWhoami(ctx, args[1:], stdout, stderr, getenv)
	case "admin":
		return runAdmin(ctx, args[1:], stdout, stderr, getenv)
	case "backup":
		return runBackup(ctx, args[1:], stdout, stderr, getenv)
	case "dump":
		return runDump(ctx, args[1:], stdout, stderr, getenv)
	case "load":
		return runLoad(ctx, args[1:], stdin, stdout, stderr, getenv)
	case "migrate":
		return runMigrate(ctx, args[1:], stdout, stderr)
	case "health":
		return runHealth(ctx, args[1:], stdout, stderr, getenv)
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "agora: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// serve runs the HTTP server until ctx is done. If ready is non-nil it
// receives the bound address once the listener is open.
func serve(ctx context.Context, cfg config.Serve, logger *slog.Logger, ready chan<- string) error {
	policy, err := auth.PolicyFor(cfg.Auth)
	if err != nil {
		return err
	}
	db, err := store.OpenWith(ctx, cfg.DB, store.Options{Logger: logger, Snapshot: true})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	removeStaleScratch(filepath.Dir(cfg.DB), time.Now(), logger)
	opts := core.Options{Policy: policy}
	var failures *auth.Failures
	if cfg.Auth == config.AuthToken {
		opts.WriteLimit = cfg.WriteLimit
		failures = auth.NewFailures(cfg.AuthFailureLimit, cfg.TrustedProxies, nil)
	}
	svc := core.New(db, opts)
	authn, err := auth.For(cfg.Auth, svc)
	if err != nil {
		return err
	}
	activity := auth.NewActivity(cfg.TrustedProxies, nil)
	if cfg.Auth == config.AuthToken {
		authn = auth.Limited{Next: authn, Failures: failures, Activity: activity}
	}
	var viewer http.Handler
	if cfg.Web {
		// Browsers sign in with a session cookie; the API and MCP take
		// bearer tokens only.
		viewerAuth := authn
		if cfg.Auth == config.AuthToken {
			viewerAuth = auth.Limited{Next: auth.Session{Service: svc}, Failures: failures, Activity: activity}
		}
		viewer, err = web.Handler(svc, viewerAuth, web.Options{
			ThemeDir: cfg.ThemeDir, Logger: logger, Version: version,
			Login: cfg.Auth == config.AuthToken, SecureCookies: strings.HasPrefix(cfg.PublicURL, "https://"),
			Failures: failures, Activity: activity,
		})
		if err != nil {
			return err
		}
	} else if cfg.ThemeDir != "" {
		return errors.New("--theme-dir has no effect without --web")
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}
	if cfg.Auth == config.AuthNone {
		logger.Warn("auth is off: anyone who can reach this address can read and post", "addr", ln.Addr().String())
	}
	logger.Info("listening", "addr", ln.Addr().String(), "db", cfg.DB, "web", cfg.Web, "version", version)
	if ready != nil {
		ready <- ln.Addr().String()
	}

	// Request contexts derive from base, which is cancelled at shutdown so
	// long-poll waits return instead of holding Shutdown open.
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{
		Handler: server.New(server.Deps{
			Service: svc, Authenticator: authn, Logger: logger, MCPMaxWait: cfg.MCPMaxWait, Version: version, AuthMode: string(cfg.Auth),
			AllowedHosts: allowedHosts(cfg, ln.Addr()), Web: viewer, Activity: activity,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	cancelBase()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("stopped")
	return nil
}

// allowedHosts resolves the Host allowlist: explicit configuration wins;
// otherwise a loopback listener answers only loopback names and any other
// listener answers any host.
func allowedHosts(cfg config.Serve, addr net.Addr) []string {
	switch {
	case cfg.AnyHost:
		return nil
	case len(cfg.AllowedHosts) > 0:
		return cfg.AllowedHosts
	}
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP.IsLoopback() {
		return server.LoopbackHosts
	}
	return nil
}

// staleScratch is how old a scratch directory left by a load or backup must
// be before serve removes it; younger ones may belong to one in progress.
const staleScratch = time.Hour

// removeStaleScratch removes the private scratch directories a load or
// backup that crashed left beside the database; they hold copies of
// conversations.
func removeStaleScratch(dir string, now time.Time, logger *slog.Logger) {
	for _, pattern := range []string{".agora-load-*", ".agora-backup-*"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			info, err := os.Lstat(m)
			if err != nil || !info.IsDir() || now.Sub(info.ModTime()) < staleScratch {
				continue
			}
			if err := os.RemoveAll(m); err != nil {
				logger.Warn("could not remove a stale scratch directory", "path", m, "err", err)
				continue
			}
			logger.Info("removed a stale scratch directory", "path", m)
		}
	}
}
