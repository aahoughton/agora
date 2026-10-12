package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Bridge serves MCP on local (normally stdio) by relaying to the server's
// streamable HTTP endpoint. It mirrors the remote server's instructions and
// tools at startup.
//
// If a call fails because the connection to the server is lost, the bridge
// reconnects for later calls but does not replay the failed one: a lost
// `say` may already have been applied. The agent is told to retry instead.
func Bridge(ctx context.Context, endpoint string, local mcp.Transport, version string, logger *slog.Logger) error {
	r := &remote{endpoint: endpoint, version: version, logger: logger}
	sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	init := sess.InitializeResult()
	srv := mcp.NewServer(&mcp.Implementation{Name: "agora", Version: init.ServerInfo.Version}, &mcp.ServerOptions{
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
	c := mcp.NewClient(&mcp.Implementation{Name: "agora-bridge", Version: r.version}, nil)
	sess, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.endpoint}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", r.endpoint, err)
	}
	r.sess = sess
	return sess, nil
}

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
		return bridgeFailure(fmt.Sprintf("agora server unreachable at %s (%v); retry the call shortly", r.endpoint, err)), nil
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

// lostCallAdvice tells the agent how to recover from a call whose outcome is
// unknown.
func lostCallAdvice(tool string, err error) string {
	prefix := fmt.Sprintf("lost the connection to the agora server during %s (%v). ", tool, err)
	switch tool {
	case "read", "wait": // wait may deliver messages inline and mark them read
		return prefix + "The server may already have marked those messages read: call read with after set to the last seq you actually saw."
	case "say", "conclude", "leave", "close", "start", "join":
		return prefix + "It may or may not have been applied. Call read with after set to the last seq you saw to check before retrying."
	default:
		return prefix + "Retry the call."
	}
}

func bridgeFailure(msg string) *mcp.CallToolResult {
	r := text("bridge_error: " + msg)
	r.IsError = true
	return r
}
