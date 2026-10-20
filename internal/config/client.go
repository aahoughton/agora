package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

// Environment variables for client commands.
const (
	EnvURL       = "SMOOT_URL"
	EnvNamespace = "SMOOT_NAMESPACE"
	EnvAgent     = "SMOOT_AGENT"
	EnvObserver  = "SMOOT_OBSERVER"
	EnvToken     = "SMOOT_TOKEN"
	EnvTokenFile = "SMOOT_TOKEN_FILE"
)

// DefaultURL is where client commands look for the server.
const DefaultURL = "http://127.0.0.1:8787"

// Client holds settings shared by commands that talk to a running server.
type Client struct {
	URL       string
	Namespace string
	// Token is the bearer token for a server in auth mode token.
	Token string
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ClientFlag selects the optional flags ClientFlags registers.
type ClientFlag int

const (
	// WithNamespace registers --namespace, resolved from EnvNamespace
	// and validated.
	WithNamespace ClientFlag = 1 << iota
	// WithToken registers --token and --token-file, resolved as
	// ResolveToken describes.
	WithToken
)

// ClientFlags registers --url on fs, plus --namespace and --token when
// with asks for them. Call the returned function after fs.Parse to resolve
// flag > env > default. A flag that is not registered is not read from the
// environment either, and its field in Client stays empty.
func ClientFlags(fs *flag.FlagSet, with ClientFlag) func(Getenv) (Client, error) {
	u := fs.String("url", "", "server URL (env "+EnvURL+", default "+DefaultURL+")")
	var ns, tok, tokFile *string
	if with&WithNamespace != 0 {
		ns = fs.String("namespace", "", "namespace (env "+EnvNamespace+", default default)")
	}
	if with&WithToken != 0 {
		tok = fs.String("token", "", "bearer token for a server with --auth token (env "+EnvToken+"; prefer the env var or a token file, since flags show in process listings)")
		tokFile = fs.String("token-file", "", "file holding the bearer token, readable only by you (env "+EnvTokenFile+", default "+defaultTokenFileHelp+")")
	}
	return func(getenv Getenv) (Client, error) {
		c := Client{URL: firstNonEmpty(*u, getenv(EnvURL), DefaultURL)}
		if ns != nil {
			c.Namespace = firstNonEmpty(*ns, getenv(EnvNamespace), "default")
			if !nameRE.MatchString(c.Namespace) {
				return Client{}, fmt.Errorf("invalid namespace %q", c.Namespace)
			}
		}
		if tok != nil {
			var err error
			if c.Token, err = ResolveToken(*tok, *tokFile, getenv); err != nil {
				return Client{}, err
			}
		}
		return c, nil
	}
}

// maxTokenFile bounds a token file's size; real tokens are far shorter.
const maxTokenFile = 4096

const defaultTokenFileHelp = "$XDG_CONFIG_HOME/smoot/token or ~/.config/smoot/token"

// ResolveToken returns the bearer token, taking the first that is set of
// flagToken, flagFile, EnvToken, EnvTokenFile and the default token file.
// Flags beat the environment, so a client config that names a token file
// still works where SMOOT_TOKEN is exported; giving both flags is an error.
// A token file that was named must exist; a missing default file means no
// token. Any token file read must be readable only by its owner.
func ResolveToken(flagToken, flagFile string, getenv Getenv) (string, error) {
	if flagToken != "" && flagFile != "" {
		return "", errors.New("give --token or --token-file, not both")
	}
	if flagToken != "" {
		return flagToken, nil
	}
	if flagFile != "" {
		return ReadTokenFile(flagFile)
	}
	if t := getenv(EnvToken); t != "" {
		return t, nil
	}
	if f := getenv(EnvTokenFile); f != "" {
		return ReadTokenFile(f)
	}
	path := DefaultTokenFile(getenv)
	if path == "" {
		return "", nil
	}
	tok, err := ReadTokenFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return tok, err
}

// TokenFile returns the token file a client would read without --token or
// SMOOT_TOKEN: flagFile, else EnvTokenFile, else the default. It is empty
// when there is no default because HOME is unset.
func TokenFile(flagFile string, getenv Getenv) string {
	return firstNonEmpty(flagFile, getenv(EnvTokenFile), DefaultTokenFile(getenv))
}

// DefaultTokenFile returns $XDG_CONFIG_HOME/smoot/token, falling back to
// $HOME/.config/smoot/token, or "" when neither is set. A relative
// XDG_CONFIG_HOME is ignored, as the XDG spec says.
func DefaultTokenFile(getenv Getenv) string {
	if dir := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "smoot", "token")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "smoot", "token")
	}
	return ""
}

// ReadTokenFile reads a token from path, refusing a file that group or
// others can access and one that holds no token.
func ReadTokenFile(path string) (string, error) {
	// O_NONBLOCK keeps a FIFO from blocking the open, so the regular-file
	// check below can refuse it; regular files ignore the flag.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("token file %s is not a regular file", path)
	}
	// Windows has no group and other permission bits to check.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s is accessible by group or others: chmod 600 it", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxTokenFile+1))
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if len(b) > maxTokenFile {
		return "", fmt.Errorf("token file %s must hold one token", path)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") {
		return "", fmt.Errorf("token file %s must hold one token", path)
	}
	return tok, nil
}

// CheckName reports whether name is a valid agent or namespace name; kind
// names it in the error.
func CheckName(kind, name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid %s name %q", kind, name)
	}
	return nil
}

// AgentFlag registers --agent on fs. Call the returned function after
// fs.Parse to resolve flag > env; the agent name is required.
func AgentFlag(fs *flag.FlagSet) func(Getenv) (string, error) {
	a := fs.String("agent", "", "your agent name (env "+EnvAgent+")")
	return func(getenv Getenv) (string, error) {
		agent := firstNonEmpty(*a, getenv(EnvAgent))
		if agent == "" {
			return "", fmt.Errorf("an agent name is required: pass --agent or set %s", EnvAgent)
		}
		if err := CheckName("agent", agent); err != nil {
			return "", err
		}
		return agent, nil
	}
}
