package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

type result struct {
	status int
	header http.Header
	body   []byte
}

func do(t *testing.T, method, url string) result {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return result{resp.StatusCode, resp.Header, body}
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(New(Deps{Authenticator: auth.None{}, Logger: slog.New(slog.DiscardHandler)}))
	defer srv.Close()

	res := do(t, http.MethodGet, srv.URL+"/healthz")
	if res.status != http.StatusOK {
		t.Fatalf("status = %d", res.status)
	}
	if ct := res.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(res.body, &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %v", body)
	}
}

func TestHealthzRejectsPost(t *testing.T) {
	srv := httptest.NewServer(New(Deps{Authenticator: auth.None{}, Logger: slog.New(slog.DiscardHandler)}))
	defer srv.Close()

	res := do(t, http.MethodPost, srv.URL+"/healthz")
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", res.status)
	}
}

func newFull(t *testing.T, hosts []string) *httptest.Server {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := httptest.NewServer(New(Deps{
		Service: core.New(db, core.Options{}), Authenticator: auth.None{},
		Logger: slog.New(slog.DiscardHandler), AllowedHosts: hosts,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func send(t *testing.T, method, url, body string, header map[string]string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestCrossSiteBrowserWritesAreRefused(t *testing.T) {
	srv := newFull(t, LoopbackHosts)
	url := srv.URL + "/v1/namespaces"
	if got := send(t, http.MethodPost, url, `{"name":"evil"}`, map[string]string{"Content-Type": "text/plain", "Sec-Fetch-Site": "cross-site"}); got != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d", got)
	}
	if got := send(t, http.MethodPost, url, `{"name":"evil2"}`, map[string]string{"Origin": "https://evil.example"}); got != http.StatusForbidden {
		t.Fatalf("foreign-origin POST = %d", got)
	}
	// Non-browser clients send neither header.
	if got := send(t, http.MethodPost, url, `{"name":"fine"}`, nil); got != http.StatusCreated {
		t.Fatalf("CLI POST = %d", got)
	}
}

func TestAllowedHosts(t *testing.T) {
	srv := newFull(t, []string{"agora.lan", "LOCALHOST"})
	for host, want := range map[string]int{"agora.lan:8787": 200, "Agora.LAN": 200, "localhost:1": 200, "127.0.0.1": 403} {
		if got := send(t, http.MethodGet, srv.URL+"/v1/namespaces", "", map[string]string{"Host": host}); got != want {
			t.Errorf("host %s = %d, want %d", host, got, want)
		}
	}
}

func TestLoopbackServerRefusesReboundHosts(t *testing.T) {
	srv := newFull(t, LoopbackHosts)
	if got := send(t, http.MethodGet, srv.URL+"/v1/namespaces", "", map[string]string{"Host": "evil.example:8787"}); got != http.StatusForbidden {
		t.Fatalf("rebound host = %d", got)
	}
	for _, h := range []string{"localhost:8787", "127.0.0.1:8787", "[::1]:8787", "localhost"} {
		if got := send(t, http.MethodGet, srv.URL+"/v1/namespaces", "", map[string]string{"Host": h}); got != http.StatusOK {
			t.Fatalf("host %s = %d", h, got)
		}
	}
	// Without a host list any host is accepted (for proxies).
	open := newFull(t, nil)
	if got := send(t, http.MethodGet, open.URL+"/v1/namespaces", "", map[string]string{"Host": "agora.example"}); got != http.StatusOK {
		t.Fatalf("open server = %d", got)
	}
}

func TestRequestsAreLogged(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := httptest.NewServer(New(Deps{Authenticator: auth.None{}, Logger: logger}))
	defer srv.Close()

	do(t, http.MethodGet, srv.URL+"/healthz?x=secret")
	do(t, http.MethodGet, srv.URL+"/v1/nowhere")
	for _, want := range []string{
		"level=DEBUG msg=http method=GET path=/healthz status=200 bytes=16 dur=",
		"level=INFO msg=http method=GET path=/v1/nowhere status=404",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("query string logged:\n%s", buf.String())
	}
}

// A client that sends headers and then dribbles (or stalls) its body is cut
// off, on every route, not only the JSON API.
func TestSlowBodiesAreCutOff(t *testing.T) {
	got := make(chan error, 1)
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		got <- err
	})
	srv := httptest.NewServer(New(Deps{Authenticator: auth.None{}, Logger: slog.New(slog.DiscardHandler), Web: slow, BodyReadTimeout: 100 * time.Millisecond}))
	defer srv.Close()
	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "POST /login HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\ntoken="); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err == nil {
			t.Fatal("the stalled body read succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the stalled body was not cut off")
	}
}
