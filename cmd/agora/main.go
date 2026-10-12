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
	"syscall"
	"time"

	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: agora <command> [flags]

commands:
  serve     run the HTTP server
  version   print the version

Run 'agora <command> -h' for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
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
		logger := slog.New(slog.NewTextHandler(stderr, nil))
		if err := serve(ctx, cfg, logger, nil); err != nil {
			logger.Error("serve failed", "err", err)
			return 1
		}
		return 0
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

	srv := &http.Server{
		Handler:           server.New(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("stopped")
	return nil
}
