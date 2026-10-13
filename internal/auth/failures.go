package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/ratelimit"
)

// Failures limits failed authentications per client address: a token or
// session cookie that was presented and rejected, or a failed viewer login.
// A request with no credentials at all is not a failure.
type Failures struct {
	lim     *ratelimit.Limiter
	trusted []netip.Prefix
}

// NewFailures allows perMinute failures per client address. The client
// address is the connection's, unless that is one of trusted (a proxy), in
// which case it comes from X-Forwarded-For. It returns nil, which allows
// everything, when perMinute is 0.
func NewFailures(perMinute int, trusted []netip.Prefix, now func() time.Time) *Failures {
	if perMinute <= 0 {
		return nil
	}
	return &Failures{lim: ratelimit.New(perMinute, time.Minute, 0, now), trusted: trusted}
}

// Check refuses a client that has used up its failures.
func (f *Failures) Check(r *http.Request) error {
	if f == nil {
		return nil
	}
	if ok, wait := f.lim.Check(f.ClientIP(r)); !ok {
		return core.RateLimited("failed sign-ins from this address", wait)
	}
	return nil
}

// Fail records a failed authentication.
func (f *Failures) Fail(r *http.Request) {
	if f != nil {
		f.lim.Allow(f.ClientIP(r))
	}
}

// ClientIP is the address a request came from.
func (f *Failures) ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !f.isTrusted(addr) {
		return host
	}
	// Walk X-Forwarded-For from the right: the first address that is not
	// a trusted proxy is the client.
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil || hop.Zone() != "" { // a zone is free text
			break
		}
		if !f.isTrusted(hop) {
			return hop.String()
		}
	}
	return host
}

func (f *Failures) isTrusted(a netip.Addr) bool {
	for _, p := range f.trusted {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// Limited counts an authenticator's rejections against Failures: once a
// client address has had too many, further rejected credentials from it
// get rate_limited instead of unauthorized. A valid credential always gets
// through, so one client failing cannot lock out others behind the same
// address.
type Limited struct {
	Next     Authenticator
	Failures *Failures
}

// Authenticate implements Authenticator.
func (l Limited) Authenticate(r *http.Request) (core.Principal, error) {
	p, err := l.Next.Authenticate(r)
	if core.CodeOf(err) != core.CodeUnauthorized || !presented(r) {
		return p, err
	}
	if cerr := l.Failures.Check(r); cerr != nil {
		return core.Principal{}, cerr
	}
	l.Failures.Fail(r)
	return p, err
}

// presented reports whether a request carries a credential.
func presented(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" {
		return true
	}
	_, err := r.Cookie(SessionCookie)
	return err == nil
}
