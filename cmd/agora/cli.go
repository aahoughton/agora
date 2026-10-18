package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/mcpserver"
	"github.com/aahoughton/agora/internal/profiles"
	"github.com/aahoughton/agora/internal/tui"
	"github.com/aahoughton/agora/skills"
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
	var conv string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		conv, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	switch {
	case conv == "" && fs.NArg() == 1:
		conv = fs.Arg(0)
	case fs.NArg() > 0:
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if conv == "" {
		return "", errors.New("a conversation name is required")
	}
	return conv, nil
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

func runMCPConfig(args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `usage: agora mcp-config --profile NAME [flags]

Prints how to connect an agent client to this server's MCP endpoint, for
profile %s. --max-wait must match the server's --mcp-max-wait so the
client allows waits that long. With --stop-hook it also prints a Stop hook
that keeps a session from ending while it is expected in a conversation.

`, strings.Join(profiles.Names(), " or "))
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace)
	profile := fs.String("profile", "", "client profile: "+strings.Join(profiles.Names(), ", "))
	maxWait := fs.Duration("max-wait", config.DefaultMCPMaxWait, "the server's --mcp-max-wait")
	stopHook := fs.Bool("stop-hook", false, "also print a Stop hook")
	withToken := fs.Bool("with-token", false, "set the client up to send the token in "+config.EnvToken+", for a server with --auth token (the token itself is never printed)")
	if err := fs.Parse(args); err != nil {
		return flagError(stderr, err)
	}
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if *profile == "" {
		return flagError(stderr, errors.New("--profile is required: "+strings.Join(profiles.Names(), ", ")))
	}
	p, err := profiles.Get(*profile)
	if err != nil {
		return flagError(stderr, err)
	}
	cc, err := resolveClient(getenv)
	if err != nil {
		return flagError(stderr, err)
	}
	if _, err := client.New(cc.URL, cc.Token); err != nil {
		return flagError(stderr, err)
	}
	base := strings.TrimRight(cc.URL, "/")
	mcpURL := base + "/mcp?namespace=" + url.QueryEscape(cc.Namespace)
	tokenEnv := ""
	if *withToken {
		tokenEnv = config.EnvToken
	}
	out, err := p.MCPConfig(mcpURL, *maxWait, tokenEnv)
	if err != nil {
		fmt.Fprintln(stderr, "agora mcp-config:", err)
		return exitError
	}
	fmt.Fprint(stdout, out)
	if *stopHook {
		// The hook reaches the same server and namespace as the MCP entry;
		// the agent name and token still come from the environment.
		hook, err := p.StopHook("agora hook stop --url " + shellWord(base) + " --namespace " + cc.Namespace)
		if err != nil {
			fmt.Fprintln(stderr, "agora mcp-config:", err)
			return exitError
		}
		fmt.Fprint(stdout, "\n"+hook)
	}
	return exitOK
}

// shellWord quotes s for a POSIX shell unless it is made only of
// characters no shell treats specially.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:@%+=,-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const skillsUsage = "usage: agora skills install --client NAME [--dir DIR] [--force]\n"

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

func runSkills(args []string, stdout, stderr io.Writer) int {
	if code, ok := groupUsage(args, "install", skillsUsage, stdout, stderr); !ok {
		return code
	}
	fs := flag.NewFlagSet("skills install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `usage: agora skills install --client NAME [--dir DIR] [--force]

Writes agora's skills (%s) where client %s reads them, and lists each
file it writes. It refuses to replace a file that differs from the one it
would write unless --force is given, and then writes nothing.

`, strings.Join(skillNames(), ", "), strings.Join(profiles.Names(), " or "))
		fs.PrintDefaults()
	}
	clientName := fs.String("client", "", "client profile: "+strings.Join(profiles.Names(), ", "))
	dir := fs.String("dir", "", "install into this directory instead of the client's skills directory")
	force := fs.Bool("force", false, "replace files that differ")
	if err := fs.Parse(args[1:]); err != nil {
		return flagError(stderr, err)
	}
	if fs.NArg() > 0 {
		return flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	target := *dir
	if target == "" {
		if *clientName == "" {
			return flagError(stderr, errors.New("--client or --dir is required"))
		}
		p, err := profiles.Get(*clientName)
		if err != nil {
			return flagError(stderr, err)
		}
		if target, err = expandHome(p.SkillsDir); err != nil {
			fmt.Fprintln(stderr, "agora skills install:", err)
			return exitError
		}
	}
	if err := installSkills(skills.FS, target, *force, stdout); err != nil {
		fmt.Fprintln(stderr, "agora skills install:", err)
		return exitError
	}
	return exitOK
}

func skillNames() []string {
	entries, _ := fs.ReadDir(skills.FS, ".")
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}

// installSkills copies every file in src under dir. It checks all files
// before writing any, so a refusal leaves dir as it was.
func installSkills(src fs.FS, dir string, force bool, out io.Writer) error {
	type file struct {
		path string
		data []byte
	}
	var files []file
	var conflicts []string
	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(path))
		old, err := os.ReadFile(dst)
		switch {
		case err == nil && bytes.Equal(old, data):
			return nil
		case err == nil && !force:
			conflicts = append(conflicts, dst)
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return err
		}
		files = append(files, file{dst, data})
		return nil
	})
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("these files differ from agora's; pass --force to replace them:\n  %s", strings.Join(conflicts, "\n  "))
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(out, "wrote", f.path)
	}
	if len(files) == 0 {
		fmt.Fprintln(out, "skills in", dir, "are up to date")
	}
	return nil
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
