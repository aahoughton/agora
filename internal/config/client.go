package config

import (
	"flag"
	"fmt"
	"regexp"
)

// Environment variables for client commands.
const (
	EnvURL       = "AGORA_URL"
	EnvNamespace = "AGORA_NAMESPACE"
	EnvAgent     = "AGORA_AGENT"
)

// DefaultURL is where client commands look for the server.
const DefaultURL = "http://127.0.0.1:8787"

// Client holds settings shared by commands that talk to a running server.
type Client struct {
	URL       string
	Namespace string
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ClientFlags registers --url and --namespace on fs. Call the returned
// function after fs.Parse to resolve flag > env > default.
func ClientFlags(fs *flag.FlagSet) func(Getenv) (Client, error) {
	u := fs.String("url", "", "server URL (env "+EnvURL+", default "+DefaultURL+")")
	ns := fs.String("namespace", "", "namespace (env "+EnvNamespace+", default default)")
	return func(getenv Getenv) (Client, error) {
		c := Client{
			URL:       firstNonEmpty(*u, getenv(EnvURL), DefaultURL),
			Namespace: firstNonEmpty(*ns, getenv(EnvNamespace), "default"),
		}
		if !nameRE.MatchString(c.Namespace) {
			return Client{}, fmt.Errorf("invalid namespace %q", c.Namespace)
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
