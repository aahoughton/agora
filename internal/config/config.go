// Package config resolves runtime settings for agora subcommands.
//
// Precedence for every setting is: command-line flag, then environment
// variable, then built-in default.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variable names.
const (
	EnvDB   = "AGORA_DB"
	EnvAddr = "AGORA_ADDR"
	EnvWeb  = "AGORA_WEB"
	EnvAuth = "AGORA_AUTH"

	EnvAllowedHosts = "AGORA_ALLOWED_HOSTS"
)

// DefaultAddr listens on loopback only. Exposing the server to a network is a
// deliberate choice made with --addr or AGORA_ADDR.
const DefaultAddr = "127.0.0.1:8787"

// AuthMode selects how requests are authenticated.
type AuthMode string

// AuthNone accepts every request as an anonymous principal with full access.
const AuthNone AuthMode = "none"

// Serve holds the settings for `agora serve`.
type Serve struct {
	DB   string
	Addr string
	Web  bool
	Auth AuthMode
	// AllowedHosts lists the Host names the server answers to. AnyHost
	// disables the check. When neither is set, the server allows loopback
	// names on a loopback address and any host otherwise.
	AllowedHosts []string
	AnyHost      bool
}

// Getenv looks up an environment variable; os.Getenv satisfies it.
type Getenv func(string) string

// ParseServe parses `agora serve` arguments.
func ParseServe(args []string, getenv Getenv, output io.Writer) (Serve, error) {
	var cfg Serve
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&cfg.DB, "db", "", "SQLite database path (env "+EnvDB+")")
	fs.StringVar(&cfg.Addr, "addr", "", "listen address (env "+EnvAddr+", default "+DefaultAddr+")")
	web := fs.Bool("web", false, "serve the HTML viewer (env "+EnvWeb+"=true)")
	auth := fs.String("auth", "", "auth mode: none (env "+EnvAuth+", default none)")
	allowed := fs.String("allowed-hosts", "", "comma-separated Host names to answer, or * for any (env "+EnvAllowedHosts+", default: loopback names on a loopback address, else any)")
	if err := fs.Parse(args); err != nil {
		return Serve{}, err
	}
	if fs.NArg() > 0 {
		return Serve{}, fmt.Errorf("serve: unexpected argument %q", fs.Arg(0))
	}

	if cfg.DB == "" {
		cfg.DB = getenv(EnvDB)
	}
	if cfg.DB == "" {
		p, err := DefaultDBPath(getenv)
		if err != nil {
			return Serve{}, err
		}
		cfg.DB = p
	}

	cfg.Addr = firstNonEmpty(cfg.Addr, getenv(EnvAddr), DefaultAddr)

	cfg.Web = *web
	if !flagSet(fs, "web") {
		if s := getenv(EnvWeb); s != "" {
			w, err := strconv.ParseBool(s)
			if err != nil {
				return Serve{}, fmt.Errorf("serve: invalid %s value %q: want true or false", EnvWeb, s)
			}
			cfg.Web = w
		}
	}

	mode, err := parseAuth(firstNonEmpty(*auth, getenv(EnvAuth), string(AuthNone)))
	if err != nil {
		return Serve{}, err
	}
	cfg.Auth = mode

	switch hosts := strings.TrimSpace(firstNonEmpty(*allowed, getenv(EnvAllowedHosts))); hosts {
	case "":
	case "*":
		cfg.AnyHost = true
	default:
		for h := range strings.SplitSeq(hosts, ",") {
			if h = strings.TrimSpace(h); h == "" || h == "*" {
				return Serve{}, fmt.Errorf("serve: invalid allowed-hosts %q: want names separated by commas, or * alone", hosts)
			}
			cfg.AllowedHosts = append(cfg.AllowedHosts, h)
		}
	}
	return cfg, nil
}

// DefaultDBPath returns $XDG_DATA_HOME/agora/agora.db, falling back to
// $HOME/.local/share/agora/agora.db.
func DefaultDBPath(getenv Getenv) (string, error) {
	if dir := getenv("XDG_DATA_HOME"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("XDG_DATA_HOME must be absolute, got %q", dir)
		}
		return filepath.Join(dir, "agora", "agora.db"), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", errors.New("cannot choose a default database path: set " + EnvDB + " or HOME")
	}
	return filepath.Join(home, ".local", "share", "agora", "agora.db"), nil
}

func parseAuth(s string) (AuthMode, error) {
	switch AuthMode(s) {
	case AuthNone:
		return AuthNone, nil
	case "token", "email":
		return "", fmt.Errorf("serve: auth mode %q is not implemented yet", s)
	default:
		return "", fmt.Errorf("serve: unknown auth mode %q: want none", s)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// flagSet reports whether the named flag was given on the command line.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
