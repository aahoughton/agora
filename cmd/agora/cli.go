package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/mcpserver"
	"github.com/aahoughton/agora/internal/tui"
	"github.com/aahoughton/agora/themes"
)

// Exit codes for client commands.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitTimedOut = 3
)

// longestServerWait is the longest single wait the API accepts.
const longestServerWait = 300 * time.Second

// requestMargin is how long past its requested wait a server may take to
// answer before the request is abandoned and retried.
const requestMargin = 2 * time.Second

// splitConversation lets the conversation name come before or after flags.
func splitConversation(fs *flag.FlagSet, args []string) (string, error) {
	conv, err := splitName(fs, args)
	if err == nil && conv == "" {
		err = errors.New("a conversation name is required")
	}
	return conv, err
}

// splitName parses fs from args, allowing one positional name anywhere
// among the flags. It returns "" when there is none.
func splitName(fs *flag.FlagSet, args []string) (string, error) {
	var name string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			return name, nil
		}
		if name != "" {
			return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
		name, args = fs.Arg(0), fs.Args()[1:]
	}
}

func flagError(stderr io.Writer, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	fmt.Fprintln(stderr, err)
	return exitUsage
}

func runWait(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora wait <conversation> --agent NAME [flags]

Blocks until someone else posts in the conversation or it concludes, then
prints a short summary and exits 0. Run it in the background to wait without
spending tokens. Exits 3 if --timeout passes first, 1 on errors. Retries while
the server is unreachable.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace|config.WithToken)
	resolveAgent := config.AgentFlag(fs)
	timeout := fs.Duration("timeout", 0, "give up after this long (default: no limit)")
	conv, err := splitConversation(fs, args)
	if err != nil {
		return flagError(stderr, err)
	}
	cc, err := resolveClient(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	agent, err := resolveAgent(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	cl, err := client.New(cc.URL, cc.Token)
	if err != nil {
		return flagError(stderr, err)
	}

	var deadline time.Time
	if *timeout > 0 {
		deadline = time.Now().Add(*timeout)
	}
	timedOut := func(last *core.Digest) int {
		fmt.Fprintln(stdout, "no new messages before the timeout")
		if last != nil {
			fmt.Fprintln(stdout, last.Standing.Line)
		}
		return exitTimedOut
	}
	var last *core.Digest
	backoff := time.Second
	for first := true; ; first = false {
		per := longestServerWait
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 && !first {
				return timedOut(last)
			}
			per = max(min(per, remaining), 0)
		}
		// The server answers by per; the extra margin only matters for a
		// server that accepts the request and never responds.
		reqCtx, cancel := context.WithTimeout(ctx, per+requestMargin)
		d, err := cl.Wait(reqCtx, cc.Namespace, conv, client.WaitParams{Agent: agent, Timeout: per})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return exitError
			}
			if !deadline.IsZero() && time.Until(deadline) <= 0 {
				return timedOut(last)
			}
			if !client.IsTemporary(err) && !errors.Is(err, context.DeadlineExceeded) {
				fmt.Fprintln(stderr, "agora wait:", err)
				return exitError
			}
			sleep := backoff
			if !deadline.IsZero() {
				sleep = min(sleep, time.Until(deadline))
			}
			fmt.Fprintf(stderr, "agora wait: %v; retrying in %s\n", err, sleep.Round(time.Millisecond))
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return exitError
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		last = &d
		if d.New > 0 || d.Standing.Status != core.StatusOpen {
			fmt.Fprintln(stdout, waitSummary(d))
			fmt.Fprintln(stdout, d.Standing.Line)
			return exitOK
		}
	}
}

func waitSummary(d core.Digest) string {
	if d.New == 0 {
		return d.Standing.Conversation + " concluded"
	}
	parts := make([]string, len(d.Authors))
	for i, a := range d.Authors {
		parts[i] = fmt.Sprintf("%s x%d", a.Author, a.Count)
	}
	return fmt.Sprintf("%d new in %s (%s)", d.New, d.Standing.Conversation, strings.Join(parts, ", "))
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora status --agent NAME [flags]

Prints the agent's status line in each open conversation of the namespace
where it is expected.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace|config.WithToken)
	resolveAgent := config.AgentFlag(fs)
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
	agent, err := resolveAgent(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	cl, err := client.New(cc.URL, cc.Token)
	if err != nil {
		return flagError(stderr, err)
	}
	sts, err := cl.Status(ctx, cc.Namespace, agent)
	if err != nil {
		fmt.Fprintln(stderr, "agora status:", err)
		return exitError
	}
	if len(sts) == 0 {
		fmt.Fprintln(stdout, "not expected in any open conversation")
	}
	for _, st := range sts {
		fmt.Fprintln(stdout, st.Line)
	}
	return exitOK
}

func runHealth(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora health [--url URL]

Checks that the server answers GET /healthz, which needs no token. Exits 0
when it does, 1 when it does not.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, 0)
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := cl.Health(ctx); err != nil {
		fmt.Fprintln(stderr, "agora health:", err)
		return exitError
	}
	fmt.Fprintln(stdout, "ok")
	return exitOK
}

func runMCP(ctx context.Context, args []string, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora mcp [flags]

Serves MCP on stdin/stdout by relaying to a running agora server's /mcp
endpoint. Use it for MCP clients that only launch stdio servers. Logs go to
stderr; stdout carries protocol frames only.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace|config.WithToken)
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
	endpoint := cl.URL() + "/mcp?namespace=" + url.QueryEscape(cc.Namespace)
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	if err := mcpserver.Bridge(ctx, endpoint, cc.Token, &mcp.StdioTransport{}, version, logger); err != nil && ctx.Err() == nil {
		fmt.Fprintln(stderr, "agora mcp:", err)
		return exitError
	}
	return exitOK
}

func runTUI(ctx context.Context, args []string, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora tui [namespace[/conversation]] [flags]

Browses namespaces and conversations on a running server and follows a
conversation live, with a composer for observer comments. Without an
argument it opens the conversations of --namespace.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace|config.WithToken)
	as := fs.String("as", "", "name to post observer comments under (env "+config.EnvObserver+"; asked on first comment if unset; with a token, the token's account name)")
	themeDir := fs.String("theme-dir", "", "directory of theme files overriding the default theme's tui/ files (env "+config.EnvThemeDir+")")
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	switch {
	case target == "" && fs.NArg() == 1:
		target = fs.Arg(0)
	case fs.NArg() > 0:
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	cc, err := resolveClient(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	opts := tui.Options{Namespace: cc.Namespace, Observer: *as}
	if opts.Observer == "" {
		opts.Observer = getenv(config.EnvObserver)
	}
	if opts.Observer != "" && !core.ValidName(opts.Observer) {
		return flagError(stderr, fmt.Errorf("invalid observer name %q", opts.Observer))
	}
	if target != "" {
		ns, conv, _ := strings.Cut(target, "/")
		if !core.ValidName(ns) || (conv != "" && !core.ValidName(conv)) {
			return flagError(stderr, fmt.Errorf("invalid target %q: want namespace or namespace/conversation", target))
		}
		opts.Namespace, opts.Conversation = ns, conv
	}
	if opts.Client, err = client.New(cc.URL, cc.Token); err != nil {
		return flagError(stderr, err)
	}
	opts.Client.SetUserAgent("agora-tui")
	if cc.Token != "" {
		// With a token the server posts comments under its account, so
		// show that name, and fail now if the token does not work.
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		w, err := opts.Client.Whoami(wctx)
		cancel()
		if err != nil {
			fmt.Fprintln(stderr, "agora tui:", err)
			return exitError
		}
		if w.Account != nil {
			opts.Observer = w.Account.Name
		}
	}
	if *themeDir == "" {
		*themeDir = getenv(config.EnvThemeDir)
	}
	theme, err := themes.Open(*themeDir)
	if err != nil {
		fmt.Fprintln(stderr, "agora tui:", err)
		return exitError
	}
	if opts.Skin, err = tui.LoadSkin(theme); err != nil {
		fmt.Fprintln(stderr, "agora tui:", err)
		return exitError
	}
	if err := tui.Run(ctx, opts); err != nil {
		fmt.Fprintln(stderr, "agora tui:", err)
		return exitError
	}
	return exitOK
}

// groupUsage handles a command group's first argument. It reports ok when
// args start with the group's one subcommand; otherwise it prints usage, to
// stdout with exit 0 when asked for help and to stderr with exit 2 when not.
func groupUsage(args []string, sub, usage string, stdout, stderr io.Writer) (int, bool) {
	switch {
	case len(args) > 0 && args[0] == sub:
		return 0, true
	case len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		fmt.Fprint(stdout, usage)
		return exitOK, false
	case len(args) > 0:
		fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usage)
		return exitUsage, false
	default:
		fmt.Fprint(stderr, usage)
		return exitUsage, false
	}
}

func runWhoami(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora whoami [flags]

Prints the server, its version and auth mode, and who the token (if any)
belongs to, with its role in each namespace.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithToken)
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	w, err := cl.Whoami(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "agora whoami:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "server:  %s (version %s, auth %s)\n", cl.URL(), w.Version, w.AuthMode)
	switch {
	case w.Account == nil:
		fmt.Fprintln(stdout, "account: none (auth is off; everyone has full access)")
		return exitOK
	case w.Account.ServerAdmin:
		fmt.Fprintf(stdout, "account: %s (server admin)\n", w.Account.Name)
	default:
		fmt.Fprintf(stdout, "account: %s\n", w.Account.Name)
	}
	if len(w.Memberships) == 0 {
		fmt.Fprintln(stdout, "roles:   none")
	}
	for i, m := range w.Memberships {
		label := "         "
		if i == 0 {
			label = "roles:   "
		}
		fmt.Fprintf(stdout, "%s%s in %s\n", label, m.Role, m.Namespace)
	}
	return exitOK
}
