package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "127.0.0.1:8787", "ftp://h", "http://"} {
		if _, err := New(u, ""); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
}

func TestErrorDecoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/namespaces/default/status":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"stale_context","message":"moved","last_seq":7}}`))
		default:
			http.Error(w, "proxy trouble", http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/", "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Status(t.Context(), "default", "a")
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Code != "stale_context" || e.LastSeq == nil || *e.LastSeq != 7 || IsTemporary(err) {
		t.Fatalf("err = %#v", err)
	}

	err = c.Health(t.Context())
	e, ok = errors.AsType[*Error](err)
	if !ok || e.Code != "http_502" || !IsTemporary(err) {
		t.Fatalf("err = %#v", err)
	}

	if IsTemporary(context.Canceled) {
		t.Fatal("cancellation is not temporary")
	}
}

func TestRedirectsStayOnTheServer(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = leaked || r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	c, err := New(redirect.URL, "agora_secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListNamespaces(t.Context()); err == nil || leaked {
		t.Fatalf("err = %v, token sent elsewhere: %v", err, leaked)
	}
}
