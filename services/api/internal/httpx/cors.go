package httpx

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CORSOptions configures the strict allowlist CORS policy. Only origins listed
// verbatim (scheme://host[:port], lower-case) are ever echoed back; there is
// no wildcard and no pattern matching.
type CORSOptions struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposedHeaders []string
	MaxAge         time.Duration
	// AllowCredentials lets browsers send cookies; sessions use cookies.
	AllowCredentials bool
}

// DefaultCORSOptions returns the policy used by the API for the given origins.
func DefaultCORSOptions(origins []string) CORSOptions {
	return CORSOptions{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "Idempotency-Key", HeaderRequestID},
		ExposedHeaders:   []string{HeaderRequestID, "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset", "Retry-After"},
		MaxAge:           10 * time.Minute,
		AllowCredentials: true,
	}
}

// OriginPolicy answers whether a browser origin is allowed.
type OriginPolicy struct {
	allowed map[string]bool
}

// NewOriginPolicy normalizes the allowlist (lower-case scheme and host, no
// path). Invalid entries are ignored rather than silently widening the list.
func NewOriginPolicy(origins []string) *OriginPolicy {
	p := &OriginPolicy{allowed: map[string]bool{}}
	for _, o := range origins {
		if n, ok := normalizeOrigin(o); ok {
			p.allowed[n] = true
		}
	}
	return p
}

// Allowed reports whether origin (as sent in the Origin header) is on the list.
func (p *OriginPolicy) Allowed(origin string) bool {
	n, ok := normalizeOrigin(origin)
	return ok && p.allowed[n]
}

// Empty reports whether no origin is allowed at all.
func (p *OriginPolicy) Empty() bool { return len(p.allowed) == 0 }

func normalizeOrigin(o string) (string, bool) {
	o = strings.TrimSpace(o)
	if o == "" || o == "null" {
		return "", false
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

// CORSMiddleware implements the allowlist policy. Requests without an Origin
// header pass through untouched. Preflights from allowed origins get a 204
// with the policy headers; preflights from other origins get a 403 envelope.
// Actual requests from other origins get no CORS headers, so the browser
// blocks the response, and the Origin check below rejects mutations.
func CORSMiddleware(opts CORSOptions) func(http.Handler) http.Handler {
	policy := NewOriginPolicy(opts.AllowedOrigins)
	methods := strings.Join(opts.AllowedMethods, ", ")
	headers := strings.Join(opts.AllowedHeaders, ", ")
	exposed := strings.Join(opts.ExposedHeaders, ", ")
	maxAge := strconv.Itoa(int(opts.MaxAge / time.Second))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Origin")
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			if !policy.Allowed(origin) {
				if preflight {
					WriteError(w, r, http.StatusForbidden, CodeForbiddenOrigin)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			if opts.AllowCredentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			if exposed != "" {
				h.Set("Access-Control-Expose-Headers", exposed)
			}
			if preflight {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", maxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// stateChanging reports whether the method can mutate state and therefore
// needs CSRF protection when cookies are the credential.
func stateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// OriginCheckMiddleware rejects state-changing requests whose browser context
// is not an allowed origin. Browsers always send Origin on such requests, so
// the allowlist decides whenever it is present. Without Origin, a
// Sec-Fetch-Site of cross-site or a foreign Referer is rejected. Requests with
// none of these headers come from non-browser clients, which cannot carry
// ambient cookie credentials, so they pass.
func OriginCheckMiddleware(origins []string) func(http.Handler) http.Handler {
	policy := NewOriginPolicy(origins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !stateChanging(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if !policy.Allowed(origin) {
					WriteError(w, r, http.StatusForbidden, CodeForbiddenOrigin)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
				WriteError(w, r, http.StatusForbidden, CodeForbiddenOrigin)
				return
			}
			if ref := r.Header.Get("Referer"); ref != "" {
				u, err := url.Parse(ref)
				if err != nil || !policy.Allowed(u.Scheme+"://"+u.Host) {
					WriteError(w, r, http.StatusForbidden, CodeForbiddenOrigin)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
