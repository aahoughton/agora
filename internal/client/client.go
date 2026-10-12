// Package client is a Go client for the agora JSON API. The CLI and the TUI
// use it; neither opens the database.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

// Client talks to one agora server.
type Client struct {
	base *url.URL
	http *http.Client
}

// New returns a client for the server at baseURL (for example
// http://127.0.0.1:8787).
func New(baseURL string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("client: invalid server URL %q: want http(s)://host[:port]", baseURL)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	// No client timeout: waits are long by design and bounded by ctx.
	return &Client{base: u, http: &http.Client{}}, nil
}

// URL returns the server base URL.
func (c *Client) URL() string { return c.base.String() }

// Error is an error response from the server.
type Error struct {
	Status  int
	Code    string
	Message string
	LastSeq *int64
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Temporary reports whether retrying the same request later may succeed.
func (e *Error) Temporary() bool {
	return e.Status == http.StatusServiceUnavailable || e.Status == http.StatusTooManyRequests ||
		e.Status == http.StatusBadGateway || e.Status == http.StatusGatewayTimeout
}

// IsTemporary reports whether err is a transport failure or a server
// response worth retrying.
func IsTemporary(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Temporary()
	}
	return err != nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var eb struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				LastSeq *int64 `json:"last_seq"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &eb) != nil || eb.Error.Code == "" {
			return &Error{Status: resp.StatusCode, Code: "http_" + strconv.Itoa(resp.StatusCode), Message: http.StatusText(resp.StatusCode)}
		}
		return &Error{Status: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message, LastSeq: eb.Error.LastSeq}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func convPath(ns, conv string) string {
	return "/v1/namespaces/" + url.PathEscape(ns) + "/conversations/" + url.PathEscape(conv)
}

// Health checks GET /healthz.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil, nil)
}

// WaitParams describe a wait.
type WaitParams struct {
	Agent string
	After *int64
	// Timeout is sent in whole seconds; the server caps it.
	Timeout time.Duration
}

// Wait blocks on the server until there is something new for the agent.
func (c *Client) Wait(ctx context.Context, ns, conv string, p WaitParams) (core.Digest, error) {
	q := url.Values{"agent": {p.Agent}, "timeout": {strconv.Itoa(int(p.Timeout / time.Second))}}
	if p.After != nil {
		q.Set("after", strconv.FormatInt(*p.After, 10))
	}
	var d core.Digest
	err := c.do(ctx, http.MethodGet, convPath(ns, conv)+"/wait", q, nil, &d)
	return d, err
}

// Status returns the agent's standing in every open conversation it is part
// of.
func (c *Client) Status(ctx context.Context, ns, agent string) ([]core.Standing, error) {
	var out struct {
		Standings []core.Standing `json:"standings"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/namespaces/"+url.PathEscape(ns)+"/status", url.Values{"agent": {agent}}, nil, &out)
	return out.Standings, err
}

// ListNamespaces returns the namespaces the caller can read.
func (c *Client) ListNamespaces(ctx context.Context) ([]core.Namespace, error) {
	var out struct {
		Namespaces []core.Namespace `json:"namespaces"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/namespaces", nil, nil, &out)
	return out.Namespaces, err
}

// ListConversations returns the conversations in a namespace, archived ones
// only if includeArchived is set.
func (c *Client) ListConversations(ctx context.Context, ns string, includeArchived bool) ([]core.Conversation, error) {
	var q url.Values
	if includeArchived {
		q = url.Values{"include_archived": {"true"}}
	}
	var out struct {
		Conversations []core.Conversation `json:"conversations"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/namespaces/"+url.PathEscape(ns)+"/conversations", q, nil, &out)
	return out.Conversations, err
}

// GetConversation returns one conversation with its participants.
func (c *Client) GetConversation(ctx context.Context, ns, conv string) (core.Conversation, error) {
	var out core.Conversation
	err := c.do(ctx, http.MethodGet, convPath(ns, conv), nil, nil, &out)
	return out, err
}

// FollowResult is a batch of new messages and the conversation after them.
type FollowResult struct {
	core.ReadResult
	Conversation core.Conversation `json:"conversation"`
}

// Follow blocks on the server until the log moves past after or timeout
// passes. A timeout returns no messages and the current conversation.
func (c *Client) Follow(ctx context.Context, ns, conv string, after int64, timeout time.Duration) (FollowResult, error) {
	q := url.Values{"after": {strconv.FormatInt(after, 10)}, "timeout": {strconv.Itoa(int(timeout / time.Second))}}
	var out FollowResult
	err := c.do(ctx, http.MethodGet, convPath(ns, conv)+"/follow", q, nil, &out)
	return out, err
}

// Observe posts an observer comment as name.
func (c *Client) Observe(ctx context.Context, ns, conv, name, body string) (core.Message, error) {
	var out core.Message
	err := c.do(ctx, http.MethodPost, convPath(ns, conv)+"/observe", nil, map[string]string{"name": name, "body": body}, &out)
	return out, err
}
