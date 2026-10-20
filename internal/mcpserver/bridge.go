package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aahoughton/smoot/internal/wording"
)

// Bridge serves MCP on local (normally stdio) by relaying to the server's
// streamable HTTP endpoint. It mirrors the remote server's instructions and
// tools at startup.
//
// If a call fails because the connection to the server is lost, the bridge
// reconnects for later calls but does not replay the failed one: a lost
// `say` may already have been applied. The agent is told to retry instead.
//
// token, if set, is sent to the server as a bearer token.
func Bridge(ctx context.Context, endpoint, token string, local mcp.Transport, version string, logger *slog.Logger) error {
	r := &remote{endpoint: endpoint, token: token, version: version, logger: logger}
	sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	init := sess.InitializeResult()
	srv := mcp.NewServer(&mcp.Implementation{Name: "smoot", Version: init.ServerInfo.Version}, &mcp.ServerOptions{
		Instructions: init.Instructions,
		Logger:       quiet(logger),
		Capabilities: &mcp.ServerCapabilities{},
	})
	for tool, err := range sess.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools from %s: %w", endpoint, err)
		}
		srv.AddTool(tool, r.forward)
	}
	defer r.close()
	return srv.Run(ctx, local)
}

type remote struct {
	endpoint string
	token    string
	version  string
	logger   *slog.Logger

	mu   sync.Mutex
	sess *mcp.ClientSession
}

func (r *remote) session(ctx context.Context) (*mcp.ClientSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess != nil {
		return r.sess, nil
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "smoot-bridge", Version: r.version}, nil)
	t := &mcp.StreamableClientTransport{Endpoint: r.endpoint}
	if r.token != "" {
		u, err := url.Parse(r.endpoint)
		if err != nil {
			return nil, fmt.Errorf("connect to %s: %w", r.endpoint, err)
		}
		t.HTTPClient = &http.Client{
			Transport: bearer{token: r.token, origin: origin(u), next: http.DefaultTransport},
			// The token is for this server only: refuse to follow a
			// redirect anywhere else.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if origin(req.URL) != origin(u) {
					return fmt.Errorf("refusing a redirect from %s to %s", origin(u), origin(req.URL))
				}
				return nil
			},
		}
	}
	sess, err := c.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", r.endpoint, err)
	}
	r.sess = sess
	return sess, nil
}

// bearer adds a bearer token to requests for its server's origin.
type bearer struct {
	token  string
	origin string
	next   http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if origin(r.URL) != b.origin {
		return b.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// origin is a URL's scheme and host, which a token is bound to.
func origin(u *url.URL) string { return strings.ToLower(u.Scheme + "://" + u.Host) }

// drop discards sess if it is still current, so the next call reconnects.
func (r *remote) drop(sess *mcp.ClientSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == sess {
		_ = sess.Close()
		r.sess = nil
	}
}

func (r *remote) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess != nil {
		_ = r.sess.Close()
		r.sess = nil
	}
}

func (r *remote) forward(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sess, err := r.session(ctx)
	if err != nil {
		return bridgeFailure(unreachableAdvice(r.endpoint, err)), nil
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: req.Params.Name, Arguments: req.Params.Arguments})
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.logger.Warn("call failed; dropping server session", "tool", req.Params.Name, "err", err)
	r.drop(sess)
	return bridgeFailure(lostCallAdvice(req.Params.Name, err)), nil
}

// unreachableAdvice tells the agent the server could not be reached; the
// failure is reported as a tool result, not a protocol error.
func unreachableAdvice(endpoint string, err error) string {
	return wording.Default().Phrase(wording.BridgeUnreachable, map[string]any{"Endpoint": endpoint, "Err": err.Error()})
}

// lostCallAdvice tells the agent how to recover from a call whose outcome is
// unknown.
func lostCallAdvice(tool string, err error) string {
	key := wording.BridgeLostOther
	switch tool {
	case "read", "wait": // wait may deliver messages inline and mark them read
		key = wording.BridgeLostRead
	case "say", "conclude", "leave", "close", "start", "join":
		key = wording.BridgeLostWrite
	}
	return wording.Default().Phrase(key, map[string]any{"Tool": tool, "Err": err.Error()})
}

func bridgeFailure(msg string) *mcp.CallToolResult {
	r := text("bridge_error: " + msg)
	r.IsError = true
	return r
}
