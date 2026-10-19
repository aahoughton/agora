package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/web"
)

// A comment posted by the page's script gets 204 when it lands, so a
// redirect can only mean the session ended and the draft must stay.
func TestBackgroundCommentsAreToldApartFromSignIn(t *testing.T) {
	e := newTokenEnv(t, web.Options{})
	post := func() int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.url+"/n/default/c/c/observe",
			strings.NewReader(url.Values{"body": {"hello"}}.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Smoot-Background", "1")
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(); got != http.StatusSeeOther {
		t.Fatalf("signed out = %d, want a redirect to sign in", got)
	}
	e.postRaw("/login", url.Values{"token": {e.token}})
	if got := post(); got != http.StatusNoContent {
		t.Fatalf("signed in = %d", got)
	}
}
