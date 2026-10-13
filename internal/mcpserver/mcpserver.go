// Package mcpserver exposes the core as MCP tools over streamable HTTP.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/wording"
)

// InlineBytes is the most message text wait returns in full, sparing the
// agent a read call for short replies.
const InlineBytes = 4000

// DefaultMaxWait keeps a wait under the 60s tool-call timeout that several
// MCP clients use by default.
const DefaultMaxWait = 50 * time.Second

// MaxReadBytes ends a read page early once its message bodies pass it, so
// a page stays well under the 16 MiB a stdio bridge accepts per message;
// the result says more follow.
const MaxReadBytes = 4 << 20

// Options configure the MCP endpoint. Zero values select the defaults.
type Options struct {
	Logger *slog.Logger
	// MaxWait caps the wait tool's timeout.
	MaxWait time.Duration
	// Version is reported to clients.
	Version string
	// URL is the server URL clients use, quoted in the instructions.
	URL string
	// Wording supplies instructions, tool descriptions and result phrases.
	Wording *wording.Wording
	// MaxSessionsPerAccount and MaxSessions cap the open MCP sessions of one
	// account and of the whole server; defaults DefaultMaxSessionsPerAccount
	// and DefaultMaxSessions. The anonymous principal (auth mode none)
	// counts as one account.
	MaxSessionsPerAccount int
	MaxSessions           int
	// Authenticator, if set, authenticates each tool call from its HTTP
	// headers, so a call uses the credential it carries rather than the
	// one that opened the session. Handler sets it.
	Authenticator auth.Authenticator
	// Activity, if set, records each authenticated MCP request.
	Activity *auth.Activity
}

// Handler serves MCP over streamable HTTP. Every request is authenticated;
// each session gets a server bound to the principal that created it and to
// the namespace named by the `namespace` query parameter (default
// "default"), and only that principal may use the session.
func Handler(svc *core.Service, authn auth.Authenticator, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.MaxWait == 0 {
		opts.MaxWait = DefaultMaxWait
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.MaxSessionsPerAccount == 0 {
		opts.MaxSessionsPerAccount = DefaultMaxSessionsPerAccount
	}
	if opts.MaxSessions == 0 {
		opts.MaxSessions = DefaultMaxSessions
	}
	sdk := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, ok := r.Context().Value(principalKey{}).(core.Principal)
		if !ok {
			return nil
		}
		ns := r.URL.Query().Get("namespace")
		if ns == "" {
			ns = "default"
		}
		if !core.ValidName(ns) {
			return nil
		}
		o := opts
		o.URL = baseURL(r)
		o.Authenticator = authn
		srv := NewServer(svc, p, ns, o)
		if ref, ok := r.Context().Value(serverRefKey{}).(*serverRef); ok {
			ref.srv = srv
		}
		return srv
	}, &mcp.StreamableHTTPOptions{
		Logger:         quiet(opts.Logger),
		SessionTimeout: SessionTimeout,
	})
	return &guard{
		authn: authn, next: sdk, ttl: SessionTimeout, owners: map[string]owner{}, activity: opts.Activity,
		perAccount: opts.MaxSessionsPerAccount, total: opts.MaxSessions,
	}
}

// Session limits. A session idle for SessionTimeout is closed; a client
// that comes back starts a new one.
const (
	SessionTimeout               = 2 * time.Hour
	DefaultMaxSessionsPerAccount = 64
	DefaultMaxSessions           = 1024
)

// baseURL is the server URL as the client reached it, for the shell-wait
// recipe in the instructions.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

type tools struct {
	svc     *core.Service
	authn   auth.Authenticator
	p       core.Principal
	ns      string
	maxWait time.Duration
	w       *wording.Wording
}

// NewServer returns an MCP server whose tools act as principal p in
// namespace ns. It panics if the
// wording does not describe exactly the registered tools and parameters.
func NewServer(svc *core.Service, p core.Principal, ns string, opts Options) *mcp.Server {
	if opts.MaxWait == 0 {
		opts.MaxWait = DefaultMaxWait
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Wording == nil {
		opts.Wording = wording.Default()
	}
	w := opts.Wording
	s := mcp.NewServer(&mcp.Implementation{Name: "agora", Version: opts.Version}, &mcp.ServerOptions{
		Instructions: w.Instructions(opts.URL, ns),
		Logger:       quiet(opts.Logger),
		Capabilities: &mcp.ServerCapabilities{},
	})
	t := &tools{svc: svc, authn: opts.Authenticator, p: p, ns: ns, maxWait: opts.MaxWait, w: w}
	s.AddReceivingMiddleware(logCalls(opts.Logger, p, ns))
	// Every tool acts only on this server (closed world). The log is
	// append-only, so writes are not destructive, except close, which ends
	// the conversation for everyone. read and wait move the caller's read
	// position, so they are not read-only. Clients such as Codex decide from
	// these whether to ask before a call.
	no, yes := false, true
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}
	write := &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}
	destructive := &mcp.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &no}

	reg := &registry{w: w}
	addTool(reg, s, "list", readOnly, t.list)
	addTool(reg, s, "start", write, t.start)
	addTool(reg, s, "join", write, t.join)
	addTool(reg, s, "read", write, t.read)
	addTool(reg, s, "say", write, t.say)
	addTool(reg, s, "wait", write, t.wait)
	addTool(reg, s, "status", readOnly, t.status)
	addTool(reg, s, "conclude", write, t.conclude)
	addTool(reg, s, "leave", write, t.leave)
	addTool(reg, s, "close", destructive, t.closeConv)
	addTool(reg, s, "presence", write, t.presence)
	reg.checkAllUsed()
	return s
}

// registry checks that the wording covers exactly the registered tools.
type registry struct {
	w    *wording.Wording
	used []string
}

func (r *registry) checkAllUsed() {
	for _, name := range r.w.Tools() {
		if !slices.Contains(r.used, name) {
			panic(fmt.Sprintf("mcpserver: tools.md describes %q, which is not a tool", name))
		}
	}
}

// addTool registers a tool whose input schema is inferred from In and whose
// descriptions come from the wording.
func addTool[In any](r *registry, s *mcp.Server, name string, ann *mcp.ToolAnnotations, h mcp.ToolHandlerFor[In, any]) {
	tw, ok := r.w.Tool(name)
	if !ok {
		panic(fmt.Sprintf("mcpserver: tools.md has no section for tool %q", name))
	}
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("mcpserver: schema for %q: %v", name, err))
	}
	for prop, ps := range schema.Properties {
		desc, ok := tw.Params[prop]
		if !ok {
			panic(fmt.Sprintf("mcpserver: tools.md %s does not describe parameter %q", name, prop))
		}
		ps.Description = desc
	}
	for prop := range tw.Params {
		if _, ok := schema.Properties[prop]; !ok {
			panic(fmt.Sprintf("mcpserver: tools.md %s describes %q, which is not a parameter", name, prop))
		}
	}
	r.used = append(r.used, name)
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: tw.Description, Annotations: ann, InputSchema: schema}, h)
}

// caller is the principal of one tool call: authenticated from the call's
// own headers when it came over HTTP, and always the session's account.
func (t *tools) caller(ctx context.Context, req *mcp.CallToolRequest) (core.Principal, error) {
	if t.authn == nil || req == nil || req.Extra == nil || req.Extra.Header == nil {
		return t.p, nil
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	if err != nil {
		return core.Principal{}, err
	}
	r.Header = req.Extra.Header
	p, err := t.authn.Authenticate(r)
	if err != nil {
		return core.Principal{}, err
	}
	if p.AccountID != t.p.AccountID {
		return core.Principal{}, &core.Error{Code: core.CodeForbidden, Message: "this MCP session belongs to another caller"}
	}
	return p, nil
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// failure reports an error to the agent as a tool result it can act on.
func failure(ctx context.Context, err error) (*mcp.CallToolResult, any, error) {
	noteFailure(ctx, err)
	msg := err.Error()
	if core.CodeOf(err) == "" {
		msg = "internal error"
	}
	r := text(msg)
	r.IsError = true
	return r, nil, nil
}

type listIn struct {
	Status          string `json:"status,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Before          string `json:"before,omitempty"`
}

// Agents list a page of live conversations unless they ask for more:
// every listed conversation costs them tokens.
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

func (t *tools) list(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	lp := core.ListParams{IncludeArchived: in.IncludeArchived, Limit: in.Limit, Before: in.Before}
	switch in.Status {
	case "":
		lp.Status = core.StatusOpen
	case "all":
	default:
		lp.Status = core.Status(in.Status)
	}
	switch {
	case lp.Limit == 0:
		lp.Limit = defaultListLimit
	case lp.Limit < 0 || lp.Limit > maxListLimit:
		return failure(ctx, &core.Error{Code: core.CodeInvalid, Message: fmt.Sprintf("limit must be between 1 and %d", maxListLimit)})
	}
	page, err := t.svc.ListConversations(ctx, p, t.ns, lp)
	if err != nil {
		return failure(ctx, err)
	}
	note(ctx, slog.Int("conversations", len(page.Conversations)), slog.Bool("more", page.Next != ""))
	return text(renderList(t.w, page)), nil, nil
}

type startIn struct {
	Conversation  string `json:"conversation"`
	Agent         string `json:"agent"`
	Topic         string `json:"topic,omitempty"`
	End           string `json:"end,omitempty"`
	EndsInMinutes int    `json:"ends_in_minutes,omitempty"`
}

func (t *tools) start(ctx context.Context, req *mcp.CallToolRequest, in startIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	ns := t.ns
	sp := core.StartParams{Name: in.Conversation, Topic: in.Topic, End: core.EndCondition(in.End), Agent: in.Agent, Join: true}
	switch {
	case in.EndsInMinutes < 0:
		return failure(ctx, &core.Error{Code: core.CodeInvalid, Message: "ends_in_minutes must be positive"})
	case in.EndsInMinutes > 0:
		ends := time.Now().Add(time.Duration(in.EndsInMinutes) * time.Minute)
		sp.EndsAt = &ends
	}
	c, err := t.svc.Start(ctx, p, ns, sp)
	if err != nil {
		return failure(ctx, err)
	}
	st, err := t.svc.StandingOf(ctx, p, ns, c.Name, in.Agent)
	if err != nil {
		return failure(ctx, err)
	}
	noteStanding(ctx, st)
	return text(fmt.Sprintf("started %s (end: %s)\n%s", c.Name, c.End, renderStanding(st))), nil, nil
}

type joinIn struct {
	Conversation string `json:"conversation"`
	Agent        string `json:"agent"`
	Resume       bool   `json:"resume,omitempty"`
}

func (t *tools) join(ctx context.Context, req *mcp.CallToolRequest, in joinIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	ns := t.ns
	st, err := t.svc.Join(ctx, p, ns, in.Conversation, core.JoinParams{Agent: in.Agent, Resume: in.Resume})
	if err != nil {
		return failure(ctx, err)
	}
	c, err := t.svc.GetConversation(ctx, p, ns, in.Conversation)
	if err != nil {
		return failure(ctx, err)
	}
	noteStanding(ctx, st)
	return text(renderConversation(t.w, c) + "\n" + renderStanding(st)), nil, nil
}

type readIn struct {
	Conversation string `json:"conversation"`
	Agent        string `json:"agent"`
	After        *int64 `json:"after,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Tail         int    `json:"tail,omitempty"`
}

func (t *tools) read(ctx context.Context, req *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	r, err := t.svc.Read(ctx, p, t.ns, in.Conversation, core.ReadParams{
		Agent: in.Agent, After: in.After, Limit: in.Limit, Tail: in.Tail, MaxBytes: MaxReadBytes,
	})
	if err != nil {
		return failure(ctx, err)
	}
	note(ctx, slog.Int("messages", len(r.Messages)), slog.Bool("more", r.More))
	if r.Standing != nil {
		noteStanding(ctx, *r.Standing)
	}
	return text(renderRead(t.w, r)), nil, nil
}

type sayIn struct {
	Conversation    string `json:"conversation"`
	Agent           string `json:"agent"`
	Body            string `json:"body"`
	ReplyTo         *int64 `json:"reply_to,omitempty"`
	ExpectedLastSeq *int64 `json:"expected_last_seq,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

func (t *tools) say(ctx context.Context, req *mcp.CallToolRequest, in sayIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	r, err := t.svc.Say(ctx, p, t.ns, in.Conversation, core.SayParams{
		Agent: in.Agent, Body: in.Body, ReplyTo: in.ReplyTo, ExpectedLastSeq: in.ExpectedLastSeq, InlineBytes: InlineBytes,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return failure(ctx, err)
	}
	note(ctx, slog.Int64("seq", r.Message.Seq), slog.Int("body_bytes", len(in.Body)))
	if r.Missed != nil {
		note(ctx, slog.Int("missed", r.Missed.New))
	}
	noteStanding(ctx, r.Standing)
	return text(renderSay(t.w, r)), nil, nil
}

type waitIn struct {
	Conversation   string `json:"conversation"`
	Agent          string `json:"agent"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func (t *tools) wait(ctx context.Context, req *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	timeout := t.maxWait
	if in.TimeoutSeconds < 0 || time.Duration(in.TimeoutSeconds)*time.Second > t.maxWait {
		return failure(ctx, &core.Error{Code: core.CodeInvalid, Message: t.w.Phrase(wording.WaitTimeoutRange,
			map[string]any{"Max": int(t.maxWait.Seconds())})})
	}
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	d, err := t.svc.Wait(ctx, p, t.ns, in.Conversation, core.WaitParams{Agent: in.Agent, Timeout: timeout, InlineBytes: InlineBytes})
	if err != nil {
		return failure(ctx, err)
	}
	note(ctx, slog.Duration("timeout", timeout), slog.Int("new", d.New), slog.Bool("timed_out", d.TimedOut))
	noteStanding(ctx, d.Standing)
	return text(renderDigest(t.w, d)), nil, nil
}

type statusIn struct {
	Agent string `json:"agent"`
}

func (t *tools) status(ctx context.Context, req *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	sts, err := t.svc.Status(ctx, p, t.ns, in.Agent)
	if err != nil {
		return failure(ctx, err)
	}
	note(ctx, slog.Int("conversations", len(sts)))
	return text(renderStatus(t.w, sts)), nil, nil
}

type concludeIn struct {
	Conversation string `json:"conversation"`
	Agent        string `json:"agent"`
	Body         string `json:"body"`
}

func (t *tools) conclude(ctx context.Context, req *mcp.CallToolRequest, in concludeIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	st, err := t.svc.Conclude(ctx, p, t.ns, in.Conversation, core.ConcludeParams{Agent: in.Agent, Body: in.Body})
	if err != nil {
		return failure(ctx, err)
	}
	noteStanding(ctx, st)
	return text(renderStanding(st)), nil, nil
}

type leaveIn struct {
	Conversation string `json:"conversation"`
	Agent        string `json:"agent"`
	Body         string `json:"body,omitempty"`
}

func (t *tools) leave(ctx context.Context, req *mcp.CallToolRequest, in leaveIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	st, err := t.svc.Leave(ctx, p, t.ns, in.Conversation, core.LeaveParams{Agent: in.Agent, Body: in.Body})
	if err != nil {
		return failure(ctx, err)
	}
	noteStanding(ctx, st)
	return text(renderStanding(st)), nil, nil
}

type closeIn struct {
	Conversation string `json:"conversation"`
	Agent        string `json:"agent"`
	Body         string `json:"body,omitempty"`
}

func (t *tools) closeConv(ctx context.Context, req *mcp.CallToolRequest, in closeIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	st, err := t.svc.Close(ctx, p, t.ns, in.Conversation, core.CloseParams{Agent: in.Agent, Body: in.Body})
	if err != nil {
		return failure(ctx, err)
	}
	noteStanding(ctx, st)
	return text(renderStanding(st)), nil, nil
}

type presenceIn struct {
	Conversation       string `json:"conversation"`
	Agent              string `json:"agent"`
	Presence           string `json:"presence"`
	Note               string `json:"note,omitempty"`
	CheckBackInMinutes int    `json:"check_back_in_minutes,omitempty"`
}

func (t *tools) presence(ctx context.Context, req *mcp.CallToolRequest, in presenceIn) (*mcp.CallToolResult, any, error) {
	p, cerr := t.caller(ctx, req)
	if cerr != nil {
		return failure(ctx, cerr)
	}
	pp := core.PresenceParams{Agent: in.Agent, Presence: in.Presence, Note: in.Note}
	switch {
	case in.CheckBackInMinutes < 0:
		return failure(ctx, &core.Error{Code: core.CodeInvalid, Message: "check_back_in_minutes must be positive"})
	case in.CheckBackInMinutes > 0:
		at := time.Now().Add(time.Duration(in.CheckBackInMinutes) * time.Minute)
		pp.CheckBackBy = &at
	}
	ns := t.ns
	if _, err := t.svc.SetPresence(ctx, p, ns, in.Conversation, pp); err != nil {
		return failure(ctx, err)
	}
	st, err := t.svc.StandingOf(ctx, p, ns, in.Conversation, in.Agent)
	if err != nil {
		return failure(ctx, err)
	}
	note(ctx, slog.String("presence", in.Presence))
	noteStanding(ctx, st)
	return text("presence: " + in.Presence + "\n" + renderStanding(st)), nil, nil
}

// quiet returns a logger that passes only warnings and errors: the SDK logs
// every session start and stop at info level.
func quiet(l *slog.Logger) *slog.Logger {
	return slog.New(minLevel{l.Handler(), slog.LevelWarn})
}

type minLevel struct {
	slog.Handler
	min slog.Level
}

func (h minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.Handler.Enabled(ctx, l)
}

func (h minLevel) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevel{h.Handler.WithAttrs(attrs), h.min}
}

func (h minLevel) WithGroup(name string) slog.Handler {
	return minLevel{h.Handler.WithGroup(name), h.min}
}
