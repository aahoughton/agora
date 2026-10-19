package mcpserver_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aahoughton/smoot/internal/mcpserver"
)

// Sessions started at the same moment cannot pass a cap together.
func TestConcurrentSessionsRespectTheCap(t *testing.T) {
	svc := newService(t)
	srv := httptest.NewServer(mcpserver.Handler(svc, headerAuth{}, mcpserver.Options{MaxSessionsPerAccount: 2}))
	t.Cleanup(srv.Close)
	var opened atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			httpc := &http.Client{Transport: addHeader{user: "1", rt: http.DefaultTransport}}
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(),
				&mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: httpc}, nil)
			if err == nil {
				opened.Add(1)
				t.Cleanup(func() { _ = cs.Close() })
			}
		})
	}
	wg.Wait()
	// A place can be held briefly by an attempt its client abandoned (the
	// HTTP client may resend a POST on a fresh connection), so fewer than
	// two may open; more than two never may.
	if n := opened.Load(); n < 1 || n > 2 {
		t.Fatalf("%d sessions opened, want 1 or 2", n)
	}
}
