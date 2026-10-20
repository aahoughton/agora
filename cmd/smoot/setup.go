package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aahoughton/smoot/internal/client"
	"github.com/aahoughton/smoot/internal/config"
	"github.com/aahoughton/smoot/internal/profiles"
	"github.com/aahoughton/smoot/internal/wording"
	"github.com/aahoughton/smoot/skills"
)

func clientUsage() string {
	return fmt.Sprintf(`usage: smoot client <command> [args]

Set up an agent client (%s) to use smoot.

  config NAME [--stop-hook [--agent NAME]] [--with-token | --with-token-file [--token-file PATH] [--smoot-path PATH]] [--approve-tools]
                                             print the client setup
  install NAME [same flags as config] [--dry-run] [--force]
                                             install config and skills
  skills (NAME | --dir DIR) [--force]         install smoot's skills where it reads them
`, strings.Join(profiles.Names(), ", "))
}

func runClient(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, clientUsage())
		return exitUsage
	}
	switch args[0] {
	case "config":
		return runClientSetup(ctx, args[1:], stdout, stderr, getenv, false)
	case "install":
		return runClientSetup(ctx, args[1:], stdout, stderr, getenv, true)
	case "skills":
		return runClientSkills(ctx, args[1:], stdout, stderr, getenv)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, clientUsage())
		return exitOK
	default:
		fmt.Fprintf(stderr, "smoot client: unknown command %q\n\n%s", args[0], clientUsage())
		return exitUsage
	}
}

// clientNames lists the clients smoot has a profile for.
func clientNames() string { return strings.Join(profiles.Names(), ", ") }

func runClientSetup(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv, install bool) int {
	action := "config"
	if install {
		action = "install"
	}
	fs := flag.NewFlagSet("client "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `usage: smoot client %s NAME [flags]

Set up agent client NAME (%s) for this server's MCP endpoint.
config prints the setup; install writes config and skills.
--max-wait must match the server's --mcp-max-wait so the client allows
waits that long. --stop-hook includes a Stop hook that keeps a session
from ending while it is expected in a conversation.
--with-token-file sets the client up to launch smoot as a stdio bridge
that reads the token from a file, for apps that do not see your shell's
environment. The config names the token file and the smoot binary by
absolute path.

`, action, clientNames())
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace)
	maxWait := fs.Duration("max-wait", config.DefaultMCPMaxWait, "the server's --mcp-max-wait")
	stopHook := fs.Bool("stop-hook", false, "include a Stop hook")
	withToken := fs.Bool("with-token", false, "set the client up to send the token in "+config.EnvToken+", for a server with --auth token (the token itself is never printed)")
	withTokenFile := fs.Bool("with-token-file", false, "set the client up to read the token from a file through the stdio bridge")
	tokenFile := fs.String("token-file", "", "with --with-token-file, the token file (env "+config.EnvTokenFile+", default $XDG_CONFIG_HOME/smoot/token or ~/.config/smoot/token)")
	smootPath := fs.String("smoot-path", "", "with --with-token-file, the smoot binary the client launches (default: smoot on PATH)")
	hookAgent := fs.String("agent", "", "with --stop-hook, the agent name the hook checks, for clients without "+config.EnvAgent+" in their environment")
	approveTools := fs.Bool("approve-tools", false, "pre-approve smoot's tools using the profile's approval setup")
	var dryRun, force bool
	if install {
		fs.BoolVar(&dryRun, "dry-run", false, "report changes without writing")
		fs.BoolVar(&force, "force", false, "replace conflicting entries with backups")
	}

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
	if *withToken && *withTokenFile {
		return flagError(stderr, errors.New("give --with-token or --with-token-file, not both"))
	}
	if !*withTokenFile && (*tokenFile != "" || *smootPath != "") {
		return flagError(stderr, errors.New("--token-file and --smoot-path need --with-token-file"))
	}
	if *hookAgent != "" && !*stopHook {
		return flagError(stderr, errors.New("--agent needs --stop-hook"))
	}
	if *hookAgent != "" {
		if err := config.CheckName("agent", *hookAgent); err != nil {
			return flagError(stderr, err)
		}
	}
	setup := profiles.Setup{MCPURL: mcpURL, MaxWait: *maxWait, ApproveTools: *approveTools}
	if *withToken {
		setup.TokenEnv = config.EnvToken
	}
	if *withTokenFile {
		if p.NoTokenFile != "" {
			return flagError(stderr, fmt.Errorf("%s has no token file setup: %s", name, p.NoTokenFile))
		}
		if setup.TokenFile, err = installTokenFile(*tokenFile, getenv); err != nil {
			return flagError(stderr, err)
		}
		if setup.SmootPath, err = installSmootPath(*smootPath); err != nil {
			return flagError(stderr, err)
		}
	}
	if *stopHook {
		program := "smoot"
		if setup.SmootPath != "" {
			program = shellWord(setup.SmootPath)
		}
		setup.HookCommand = program + " hook stop --url " + shellWord(base) + " --namespace " + shellWord(cc.Namespace)
		if setup.TokenFile != "" {
			setup.HookCommand += " --token-file " + shellWord(setup.TokenFile)
		}
		if *hookAgent != "" {
			setup.HookCommand += " --agent " + shellWord(*hookAgent)
		}
	}
	if *approveTools {
		setup.ToolNames = wording.Default().Tools()
	}
	ops, err := p.Operations(setup, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "smoot client "+action+":", err)
		return exitError
	}
	if !install {
		for _, op := range ops {
			fmt.Fprint(stdout, op.Instructions())
		}
		return exitOK
	}
	dir, err := p.Resolve(p.SkillsDir, getenv)
	if err != nil {
		return clientInstallError(stderr, "install", err, "")
	}
	plan, err := profiles.Prepare(ops, skills.FS, dir, force)
	if err == nil {
		err = plan.Apply(ctx, dryRun, stdout)
	}
	if err != nil {
		preview := []string{"smoot", "client", "config", name, "--url", cc.URL, "--namespace", cc.Namespace, "--max-wait", strings.ReplaceAll(maxWait.String(), "µ", "u")}
		for _, option := range []struct {
			flag    string
			enabled bool
		}{{"--with-token", *withToken}, {"--with-token-file", *withTokenFile}, {"--stop-hook", *stopHook}, {"--approve-tools", *approveTools}} {
			if option.enabled {
				preview = append(preview, option.flag)
			}
		}
		if *withTokenFile {
			preview = append(preview, "--token-file", setup.TokenFile, "--smoot-path", setup.SmootPath)
		}
		if *hookAgent != "" {
			preview = append(preview, "--agent", *hookAgent)
		}
		for i, arg := range preview {
			preview[i] = shellWord(arg)
		}
		return clientInstallError(stderr, "install", err, strings.Join(preview, " "))
	}

	return exitOK
}

// installTokenFile returns the absolute path of the token file a client
// should read, refusing one that a client would refuse.
func installTokenFile(flagFile string, getenv config.Getenv) (string, error) {
	path, err := config.TokenFile(flagFile, getenv)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", errors.New("no default token file without HOME: pass --token-file")
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", err
	}
	if _, err := config.ReadTokenFile(path); err != nil {
		return "", err
	}
	return path, nil
}

// installSmootPath returns the absolute path of the smoot binary a client
// should launch. Apps started outside a shell get a minimal PATH, so the
// config cannot rely on finding smoot by name.
func installSmootPath(flagPath string) (string, error) {
	path := flagPath
	if path == "" {
		var err error
		if path, err = exec.LookPath("smoot"); err != nil {
			return "", errors.New("smoot is not on PATH: pass --smoot-path")
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// mise keeps each version in its own directory, so a path there breaks
	// on the next upgrade; its shim stays put.
	if strings.Contains(filepath.ToSlash(path), "/mise/installs/") {
		return "", fmt.Errorf("%s changes with every mise upgrade: pass --smoot-path with the mise shim (usually ~/.local/share/mise/shims/smoot)", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", path)
	}
	return path, nil
}

// shellWord quotes s for a POSIX shell unless it is made only of
// characters no shell treats specially.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:@%+=,-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runClientSkills(ctx context.Context, args []string, stdout, stderr io.Writer, getenv config.Getenv) int {
	fs := flag.NewFlagSet("client skills", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `usage: smoot client skills (NAME | --dir DIR) [--force]

Writes smoot's skills (%s) where agent client NAME (%s)
reads them, or into DIR for any other client, and lists each file it
writes. Identical files are unchanged. Conflicts prevent all writes unless
--force is given; replacements keep the first .smoot-bak backup.

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
			fmt.Fprintln(stderr, "smoot client skills:", err)
			return exitError
		}
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return clientInstallError(stderr, "skills", err, "")
	}
	plan, err := profiles.Prepare(nil, skills.FS, target, *force)
	if err == nil {
		err = plan.Apply(ctx, false, stdout)
	}
	if err != nil {
		return clientInstallError(stderr, "skills", err, "")
	}
	return exitOK
}

func clientInstallError(out io.Writer, action string, err error, preview string) int {
	fmt.Fprintln(out, "smoot client "+action+":", err)
	var link *profiles.SymlinkError
	if errors.As(err, &link) {
		if preview != "" {
			fmt.Fprintln(out, "for config, run in the same environment:", preview)
			fmt.Fprintln(out, "then apply the printed setup to the link target directly.")
		}
		fmt.Fprintln(out, "for skills, use 'smoot client skills --dir DIR' with a scratch directory, then copy the files to the target.")
		fmt.Fprintln(out, "or replace the link with a regular file and rerun.")
	}
	return exitError
}

func skillNames() []string {
	entries, _ := fs.ReadDir(skills.FS, ".")
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
