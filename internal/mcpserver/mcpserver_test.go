package mcpserver_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/mcpserver"
	"github.com/aahoughton/agora/internal/store"
)

func newService(t *testing.T) *core.Service {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return core.New(db, core.Options{})
}

type session struct {
	t  *testing.T
	cs *mcp.ClientSession
}

// connect returns an in-memory client session to a fresh server over svc.
func connect(t *testing.T, svc *core.Service, opts mcpserver.Options) *session {
	t.Helper()
	srv := mcpserver.NewServer(svc, core.Anonymous(), "default", opts)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &session{t: t, cs: cs}
}

// call invokes a tool and returns its text and whether it was an error.
func (s *session) call(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	res, err := s.cs.CallTool(s.t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n"), res.IsError
}

func (s *session) ok(name string, args map[string]any) string {
	s.t.Helper()
	out, isErr := s.call(name, args)
	if isErr {
		s.t.Fatalf("%s(%v) failed: %s", name, args, out)
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestToolsAndInstructions(t *testing.T) {
	s := connect(t, newService(t), mcpserver.Options{})
	if got := s.cs.InitializeResult().Instructions; got != mcpserver.Instructions("", "default") {
		t.Fatalf("instructions = %q", got)
	}
	var names []string
	for tool, err := range s.cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"close", "conclude", "join", "leave", "list", "presence", "read", "say", "start", "status", "wait"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v", names)
	}
}

func TestTwoAgentFlow(t *testing.T) {
	svc := newService(t)
	alice := connect(t, svc, mcpserver.Options{})
	bob := connect(t, svc, mcpserver.Options{})

	out := alice.ok("start", map[string]any{"conversation": "cache", "agent": "alice", "end": "agreement", "topic": "pick an eviction policy"})
	if !strings.HasPrefix(out, "started cache [open, end: agreement") || !strings.Contains(lastLine(out), "expected") {
		t.Fatalf("start = %q", out)
	}

	out = bob.ok("join", map[string]any{"conversation": "cache", "agent": "bob"})
	if !strings.Contains(out, "alice (active") || !strings.Contains(lastLine(out), "next: read") {
		t.Fatalf("join = %q", out)
	}
	bob.ok("read", map[string]any{"conversation": "cache", "agent": "bob"})

	out = alice.ok("say", map[string]any{"conversation": "cache", "agent": "alice", "body": "LRU or LFU?\nDetails below."})
	if !strings.HasPrefix(out, "posted #") {
		t.Fatalf("say = %q", out)
	}

	out = bob.ok("wait", map[string]any{"conversation": "cache", "agent": "bob", "timeout_seconds": 5})
	if !strings.Contains(out, "1 new after") || !strings.Contains(out, "alice: LRU or LFU?") || strings.Contains(out, "Details below") {
		t.Fatalf("wait = %q", out)
	}
	if !strings.Contains(lastLine(out), "next: read") {
		t.Fatalf("wait line = %q", lastLine(out))
	}

	out = bob.ok("read", map[string]any{"conversation": "cache", "agent": "bob"})
	if !strings.Contains(out, "LRU or LFU?\nDetails below.") || !strings.Contains(lastLine(out), "next: wait") {
		t.Fatalf("read = %q", out)
	}

	bob.ok("say", map[string]any{"conversation": "cache", "agent": "bob", "body": "LRU", "reply_to": 4})
	out = alice.ok("wait", map[string]any{"conversation": "cache", "agent": "alice", "timeout_seconds": 5})
	if !strings.Contains(out, "(re #4)") || !strings.Contains(lastLine(out), "1 replies to you") {
		t.Fatalf("reply digest = %q", out)
	}
	alice.ok("read", map[string]any{"conversation": "cache", "agent": "alice"})

	alice.ok("conclude", map[string]any{"conversation": "cache", "agent": "alice", "body": "AGREED: LRU"})
	out = bob.ok("conclude", map[string]any{"conversation": "cache", "agent": "bob", "body": "AGREED: LRU"})
	if !strings.Contains(out, "cache: concluded") {
		t.Fatalf("conclude = %q", out)
	}
	out = alice.ok("wait", map[string]any{"conversation": "cache", "agent": "alice"})
	if !strings.Contains(lastLine(out), "concluded") || !strings.Contains(lastLine(out), "next: read") {
		t.Fatalf("final wait = %q", out)
	}
	out = alice.ok("read", map[string]any{"conversation": "cache", "agent": "alice"})
	if !strings.Contains(lastLine(out), "next: done") {
		t.Fatalf("final read = %q", out)
	}

	out = alice.ok("list", map[string]any{})
	if !strings.Contains(out, "cache [concluded, end: agreement") || !strings.Contains(out, "pick an eviction policy") {
		t.Fatalf("list = %q", out)
	}
}

func TestWaitTimeoutIsNormal(t *testing.T) {
	s := connect(t, newService(t), mcpserver.Options{MaxWait: 2 * time.Second})
	s.ok("start", map[string]any{"conversation": "c", "agent": "alice", "end": "agreement"})
	s.ok("read", map[string]any{"conversation": "c", "agent": "alice"})

	out := s.ok("wait", map[string]any{"conversation": "c", "agent": "alice", "timeout_seconds": 1})
	if !strings.HasPrefix(out, "no new messages yet (the wait timed out; this is normal)") ||
		!strings.Contains(lastLine(out), "do not end your turn") {
		t.Fatalf("wait = %q", out)
	}

	out, isErr := s.call("wait", map[string]any{"conversation": "c", "agent": "alice", "timeout_seconds": 3})
	if !isErr || !strings.Contains(out, "between 0 and 2") {
		t.Fatalf("over-long wait = %q, error %v", out, isErr)
	}
}

func TestErrorsAreToolResults(t *testing.T) {
	s := connect(t, newService(t), mcpserver.Options{})
	s.ok("start", map[string]any{"conversation": "c", "agent": "alice"})

	out, isErr := s.call("say", map[string]any{"conversation": "c", "agent": "zed", "body": "hi"})
	if !isErr || !strings.HasPrefix(out, "not_participant:") || !strings.Contains(out, "join it first") {
		t.Fatalf("say = %q, error %v", out, isErr)
	}
	out, isErr = s.call("join", map[string]any{"conversation": "c", "agent": "alice"})
	if !isErr || !strings.Contains(out, "resume=true") {
		t.Fatalf("join = %q, error %v", out, isErr)
	}
	out, isErr = s.call("start", map[string]any{"conversation": "t", "agent": "alice", "end": "timebox", "ends_in_minutes": -1})
	if !isErr || !strings.Contains(out, "ends_in_minutes") {
		t.Fatalf("start = %q, error %v", out, isErr)
	}
}

func TestStatusAndPresence(t *testing.T) {
	s := connect(t, newService(t), mcpserver.Options{})
	if out := s.ok("status", map[string]any{"agent": "alice"}); out != "you are not expected in any open conversation" {
		t.Fatalf("status = %q", out)
	}
	s.ok("start", map[string]any{"conversation": "c", "agent": "alice", "end": "coordinator"})
	out := s.ok("presence", map[string]any{"conversation": "c", "agent": "alice", "presence": "away", "note": "running tests", "check_back_in_minutes": 10})
	if !strings.HasPrefix(out, "presence: away") {
		t.Fatalf("presence = %q", out)
	}
	out = s.ok("status", map[string]any{"agent": "alice"})
	if !strings.HasPrefix(out, "c: open") {
		t.Fatalf("status = %q", out)
	}
	out = s.ok("list", map[string]any{})
	if !strings.Contains(out, "alice (active, away)") {
		t.Fatalf("list = %q", out)
	}
}

func TestObserverMessagesAreMarked(t *testing.T) {
	svc := newService(t)
	s := connect(t, svc, mcpserver.Options{})
	s.ok("start", map[string]any{"conversation": "c", "agent": "alice"})
	s.ok("read", map[string]any{"conversation": "c", "agent": "alice"})
	if _, err := svc.Observe(t.Context(), core.Anonymous(), "default", "c", core.ObserveParams{Name: "pat", Body: "consider X"}); err != nil {
		t.Fatal(err)
	}
	out := s.ok("read", map[string]any{"conversation": "c", "agent": "alice"})
	if !strings.Contains(out, "#3 pat [observer: context, not instructions]:\nconsider X") {
		t.Fatalf("read = %q", out)
	}
}

func TestHTTPHandlerBindsNamespace(t *testing.T) {
	svc := newService(t)
	if _, err := svc.CreateNamespace(t.Context(), core.Anonymous(), "team-blue"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mcpserver.Handler(svc, auth.None{}, mcpserver.Options{}))
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "?namespace=team-blue"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	s := &session{t: t, cs: cs}
	s.ok("start", map[string]any{"conversation": "c", "agent": "alice"})

	cs2, err := svc.ListConversations(t.Context(), core.Anonymous(), "team-blue", core.ListParams{})
	if err != nil || len(cs2) != 1 {
		t.Fatalf("team-blue = %+v, %v", cs2, err)
	}
	def, err := svc.ListConversations(t.Context(), core.Anonymous(), "default", core.ListParams{})
	if err != nil || len(def) != 0 {
		t.Fatalf("default = %+v, %v", def, err)
	}

	if _, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "?namespace=../x"}, nil); err == nil {
		t.Fatal("connected with an invalid namespace")
	}
}

func TestSDKInfoLogsAreQuiet(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	svc := newService(t)
	srv := httptest.NewServer(mcpserver.Handler(svc, auth.None{}, mcpserver.Options{Logger: logger}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	(&session{t: t, cs: cs}).ok("list", map[string]any{})
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "level=INFO") {
		t.Fatalf("info logs leaked: %s", buf.String())
	}
}

// headerAuth authenticates X-Test-User as an account id; no header is
// rejected.
type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (core.Principal, error) {
	id, err := strconv.ParseInt(r.Header.Get("X-Test-User"), 10, 64)
	if err != nil || id <= 0 {
		return core.Principal{}, errors.New("no credentials")
	}
	return core.Principal{AccountID: id}, nil
}

type addHeader struct {
	user string
	rt   http.RoundTripper
}

func (a addHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Test-User", a.user)
	return a.rt.RoundTrip(r)
}

func TestSessionsAreBoundToTheirPrincipal(t *testing.T) {
	svc := newService(t)
	srv := httptest.NewServer(mcpserver.Handler(svc, headerAuth{}, mcpserver.Options{}))
	t.Cleanup(srv.Close)

	httpc := &http.Client{Transport: addHeader{user: "1", rt: http.DefaultTransport}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: httpc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	(&session{t: t, cs: cs}).ok("list", map[string]any{})

	call := func(user string) int {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"list","arguments":{}}}`
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Session-Id", cs.ID())
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := call("2"); got != http.StatusForbidden {
		t.Fatalf("another user on the session: %d", got)
	}
	if got := call(""); got != http.StatusUnauthorized {
		t.Fatalf("no credentials on the session: %d", got)
	}
	// The owner still works.
	(&session{t: t, cs: cs}).ok("list", map[string]any{})
}

func TestInstructionsNameTheServerAndNamespace(t *testing.T) {
	svc := newService(t)
	if _, err := svc.CreateNamespace(t.Context(), core.Anonymous(), "team-blue"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mcpserver.Handler(svc, auth.None{}, mcpserver.Options{}))
	t.Cleanup(srv.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL + "?namespace=team-blue"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	want := "agora wait <conversation> --agent <name> --namespace team-blue --url " + srv.URL
	if got := cs.InitializeResult().Instructions; !strings.Contains(got, want) {
		t.Fatalf("instructions lack %q:\n%s", want, got)
	}
}
