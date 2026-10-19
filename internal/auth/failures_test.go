package auth

import (
	"errors"
	"net/http"
	"net/netip"
	"testing"

	"github.com/aahoughton/smoot/internal/config"
	"github.com/aahoughton/smoot/internal/core"
)

func TestFailedAuthenticationsAreLimited(t *testing.T) {
	svc := service(t, config.AuthToken)
	tok, err := svc.Bootstrap(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	authn := Limited{Next: Bearer{Service: svc}, Failures: NewFailures(3, nil, nil)}
	from := func(addr, authz string) *http.Request {
		r := request(t, authz)
		r.RemoteAddr = addr + ":1234"
		return r
	}
	// No credentials at all is not a failure.
	for range 5 {
		if _, err := authn.Authenticate(from("10.0.0.1", "")); core.CodeOf(err) != core.CodeUnauthorized {
			t.Fatalf("no credentials: %v", err)
		}
	}
	for range 3 {
		if _, err := authn.Authenticate(from("10.0.0.1", "Bearer smoot_wrong")); core.CodeOf(err) != core.CodeUnauthorized {
			t.Fatalf("bad token: %v", err)
		}
	}
	// Further bad tokens from that address are refused with a wait...
	_, err = authn.Authenticate(from("10.0.0.1", "Bearer smoot_wrong"))
	if ce, ok := errors.AsType[*core.Error](err); !ok || ce.Code != core.CodeRateLimited || ce.RetryAfter <= 0 {
		t.Fatalf("after failures: %v", err)
	}
	// ...but a valid token from the same address (a shared NAT, say) still
	// works, as it does from anywhere else.
	for _, addr := range []string{"10.0.0.1", "10.0.0.2"} {
		if _, err := authn.Authenticate(from(addr, "Bearer "+tok.Secret)); err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	f := Proxies(trusted)
	tests := []struct {
		remote, xff, want string
	}{
		{"192.0.2.1:5", "", "192.0.2.1"},
		{"192.0.2.1:5", "203.0.113.9", "192.0.2.1"},            // an untrusted peer cannot claim another address
		{"10.1.2.3:5", "203.0.113.9", "203.0.113.9"},           // a trusted proxy can
		{"10.1.2.3:5", "203.0.113.9, 10.9.9.9", "203.0.113.9"}, // through a chain of trusted proxies
		{"10.1.2.3:5", "198.51.100.7, 203.0.113.9", "203.0.113.9"},
		{"10.1.2.3:5", "nonsense", "10.1.2.3"},
		{"10.1.2.3:5", "fe80::1%\u009b2J", "10.1.2.3"},
	}
	for _, tt := range tests {
		r := request(t, "")
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := f.ClientIP(r); got != tt.want {
			t.Errorf("%s via %q = %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}
