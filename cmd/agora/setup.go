package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/profiles"
	"github.com/aahoughton/agora/skills"
)

func clientUsage() string {
	return fmt.Sprintf(`usage: agora client <command> [args]

Set up an agent client (%s) to use agora.

  config NAME [--stop-hook] [--with-token]   print how to connect it to the server
  skills (NAME | --dir DIR) [--force]         install agora's skills where it reads them
`, strings.Join(profiles.Names(), ", "))
}

func runClient(args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, clientUsage())
		return exitUsage
	}
	switch args[0] {
	case "config":
		return runClientConfig(args[1:], stdout, stderr, getenv)
	case "skills":
		return runClientSkills(args[1:], stdout, stderr, getenv)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, clientUsage())
		return exitOK
	default:
		fmt.Fprintf(stderr, "agora client: unknown command %q\n\n%s", args[0], clientUsage())
		return exitUsage
	}
}

// clientNames lists the clients agora has a profile for.
func clientNames() string { return strings.Join(profiles.Names(), ", ") }

func runClientConfig(args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("client config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `usage: agora client config NAME [flags]

Prints how to connect agent client NAME (%s) to this server's MCP
endpoint. --max-wait must match the server's --mcp-max-wait so the client
allows waits that long. With --stop-hook it also prints a Stop hook that
keeps a session from ending while it is expected in a conversation.

`, clientNames())
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace)
	maxWait := fs.Duration("max-wait", config.DefaultMCPMaxWait, "the server's --mcp-max-wait")
	stopHook := fs.Bool("stop-hook", false, "also print a Stop hook")
	withToken := fs.Bool("with-token", false, "set the client up to send the token in "+config.EnvToken+", for a server with --auth token (the token itself is never printed)")
	name, err := splitName(fs, args)
	if err != nil {
		return flagError(stderr, err)
	}
	if name == "" {
		return flagError(stderr, errors.New("a client name is required: "+clientNames()))
	}
	p, err := profiles.Get(name)
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
	op, err := p.MCPOperation(mcpURL, *maxWait, tokenEnv)
	if err != nil {
		fmt.Fprintln(stderr, "agora client config:", err)
		return exitError
	}
	ops := []profiles.Operation{op}
	if *stopHook {
		// The hook reaches the same server and namespace as the MCP entry;
		// the agent name and token still come from the environment.
		hook, err := p.HookOperation("agora hook stop --url " + shellWord(base) + " --namespace " + shellWord(cc.Namespace))
		if err != nil {
			fmt.Fprintln(stderr, "agora client config:", err)
			return exitError
		}
		ops = append(ops, hook)
	}
	for i := range ops {
		ops[i], err = p.ResolveOperation(ops[i], getenv)
		if err != nil {
			fmt.Fprintln(stderr, "agora client config:", err)
			return exitError
		}
	}
	for _, op := range ops {
		fmt.Fprint(stdout, op.Instructions())
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

func runClientSkills(args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("client skills", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `usage: agora client skills (NAME | --dir DIR) [--force]

Writes agora's skills (%s) where agent client NAME (%s)
reads them, or into DIR for any other client, and lists each file it
writes. It refuses to replace a file that differs from the one it would
write unless --force is given, and then writes nothing.

`, strings.Join(skillNames(), ", "), clientNames())
		fs.PrintDefaults()
	}
	dir := fs.String("dir", "", "install into this directory instead of the client's skills directory")
	force := fs.Bool("force", false, "replace files that differ")
	clientName, err := splitName(fs, args)
	if err != nil {
		return flagError(stderr, err)
	}
	if clientName != "" && *dir != "" {
		return flagError(stderr, errors.New("give a client name or --dir, not both"))
	}
	target := *dir
	if target == "" {
		if clientName == "" {
			return flagError(stderr, errors.New("a client name or --dir is required: "+clientNames()))
		}
		p, err := profiles.Get(clientName)
		if err != nil {
			return flagError(stderr, err)
		}
		if target, err = p.Resolve(p.SkillsDir, getenv); err != nil {
			fmt.Fprintln(stderr, "agora client skills:", err)
			return exitError
		}
	}
	if err := installSkills(skills.FS, target, *force, stdout); err != nil {
		fmt.Fprintln(stderr, "agora client skills:", err)
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
