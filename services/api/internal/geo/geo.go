// Package geo reads the viewer headers CloudFront adds at the edge, and only
// when the request provably came through CloudFront: the distribution adds a
// custom origin header carrying DM_ORIGIN_SECRET, and a request without the
// right value is treated as if it had no edge headers at all. This is what
// stops a client from spoofing its country (geo pricing) or its address
// (rate-limit buckets) by setting CloudFront-Viewer-* headers itself.
package geo

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Header names. The origin secret header name is part of the CloudFront
// distribution configuration (infra/terraform); keep both in sync.
const (
	HeaderOriginSecret  = "X-DM-Origin-Secret"
	HeaderViewerCountry = "CloudFront-Viewer-Country"
	HeaderViewerAddress = "CloudFront-Viewer-Address"
)

// Unknown is returned when the country cannot be trusted or is absent.
const Unknown = ""

// Edge validates the origin secret and exposes the viewer headers.
type Edge struct {
	// secret is stored hashed so comparisons are constant time and the raw
	// value never sits next to request data.
	secret [32]byte
	set    bool
	header string
}

// Option configures New.
type Option func(*Edge)

// WithHeaderName overrides the origin secret header name.
func WithHeaderName(name string) Option {
	return func(e *Edge) {
		if name != "" {
			e.header = name
		}
	}
}

// New returns an Edge for the configured secret. An empty secret means no
// request is ever trusted (dev without CloudFront).
func New(secret string, opts ...Option) *Edge {
	e := &Edge{header: HeaderOriginSecret}
	if secret != "" {
		e.secret = sha256.Sum256([]byte(secret))
		e.set = true
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Trusted reports whether r carries the origin secret, in constant time.
func (e *Edge) Trusted(r *http.Request) bool {
	if !e.set {
		return false
	}
	got := r.Header.Get(e.header)
	if got == "" {
		return false
	}
	sum := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(sum[:], e.secret[:]) == 1
}

// Country returns the ISO 3166-1 alpha-2 code CloudFront resolved for the
// viewer, upper-cased, or Unknown when the request is not trusted or the
// header is missing or malformed.
func (e *Edge) Country(r *http.Request) string {
	if !e.Trusted(r) {
		return Unknown
	}
	c := strings.ToUpper(strings.TrimSpace(r.Header.Get(HeaderViewerCountry)))
	if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
		return Unknown
	}
	return c
}

// ClientIP returns the viewer address CloudFront saw when the request is
// trusted, and the TCP peer address otherwise. An invalid result (no usable
// address anywhere) returns the zero netip.Addr.
func (e *Edge) ClientIP(r *http.Request) netip.Addr {
	if e.Trusted(r) {
		if ip, ok := parseViewerAddress(r.Header.Get(HeaderViewerAddress)); ok {
			return ip
		}
	}
	return parseRemoteAddr(r.RemoteAddr)
}

// parseViewerAddress accepts "203.0.113.5:4321", "[2001:db8::1]:4321" and
// CloudFront's bracket-less IPv6 form "2001:db8::1:4321".
func parseViewerAddress(v string) (netip.Addr, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return netip.Addr{}, false
	}
	if ap, err := netip.ParseAddrPort(v); err == nil {
		return ap.Addr().Unmap(), true
	}
	if i := strings.LastIndex(v, ":"); i > 0 {
		if ip, err := netip.ParseAddr(v[:i]); err == nil {
			return ip.Unmap(), true
		}
	}
	if ip, err := netip.ParseAddr(v); err == nil {
		return ip.Unmap(), true
	}
	return netip.Addr{}, false
}

func parseRemoteAddr(v string) netip.Addr {
	host, _, err := net.SplitHostPort(v)
	if err != nil {
		host = v
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}
