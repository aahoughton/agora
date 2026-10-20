package config

import (
	"flag"
	"fmt"
	"regexp"
)

// Environment variables for client commands.
const (
	EnvURL       = "SMOOT_URL"
	EnvNamespace = "SMOOT_NAMESPACE"
	EnvAgent     = "SMOOT_AGENT"
	EnvObserver  = "SMOOT_OBSERVER"
	EnvToken     = "SMOOT_TOKEN"
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
	// WithToken registers --token, resolved from EnvToken.
	WithToken
)

// ClientFlags registers --url on fs, plus --namespace and --token when
// with asks for them. Call the returned function after fs.Parse to resolve
// flag > env > default. A flag that is not registered is not read from the
// environment either, and its field in Client stays empty.
func ClientFlags(fs *flag.FlagSet, with ClientFlag) func(Getenv) (Client, error) {
	u := fs.String("url", "", "server URL (env "+EnvURL+", default "+DefaultURL+")")
	var ns, tok *string
	if with&WithNamespace != 0 {
		ns = fs.String("namespace", "", "namespace (env "+EnvNamespace+", default default)")
	}
	if with&WithToken != 0 {
		tok = fs.String("token", "", "bearer token for a server with --auth token (env "+EnvToken+"; prefer the env var, since flags show in process listings)")
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
			c.Token = firstNonEmpty(*tok, getenv(EnvToken))
		}
		return c, nil
	}
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
		if !nameRE.MatchString(agent) {
			return "", fmt.Errorf("invalid agent name %q", agent)
		}
		return agent, nil
	}
}
