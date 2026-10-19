package mcpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aahoughton/smoot/internal/core"
)

type oneAccount struct{}

func (oneAccount) Authenticate(*http.Request) (core.Principal, error) {
	return core.Principal{AccountID: 1}, nil
}

// TestRefusedStartFreesItsPlace: a request without a session id that gets
// no session (the SDK client's server/discover probe) gives back its place
// before its response reaches the client, even if the handler has not yet
// returned; the client's initialize follows at once.
func TestRefusedStartFreesItsPlace(t *testing.T) {
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("probe") {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-release
			return
		}
		w.Header().Set(sessionHeader, "s1")
		w.WriteHeader(http.StatusOK)
	})
	g := &guard{authn: oneAccount{}, next: next, ttl: SessionTimeout, owners: map[string]owner{}, perAccount: 1, total: 10}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	post := func(url string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	probe := post(srv.URL + "?probe")
	defer func() { _ = probe.Body.Close() }()
	resp := post(srv.URL)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize after a refused probe: status %d", resp.StatusCode)
	}
}
