package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
	srv := httptest.NewServer(New())
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
	srv := httptest.NewServer(New())
	defer srv.Close()

	res := do(t, http.MethodPost, srv.URL+"/healthz")
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", res.status)
	}
}
