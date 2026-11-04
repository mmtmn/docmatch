package ratelimit

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

// Header names follow the IETF draft-ietf-httpapi-ratelimit-headers.
const (
	HeaderLimit     = "RateLimit-Limit"
	HeaderRemaining = "RateLimit-Remaining"
	HeaderReset     = "RateLimit-Reset"
	HeaderPolicy    = "RateLimit-Policy"
	HeaderRetry     = "Retry-After"
)

// Keyer identifies the request: the limiter key, the policy that applies and
// whether the request is subject to limiting at all (ok=false skips it).
type Keyer func(r *http.Request) (key string, policy Policy, ok bool)

// Middleware applies the limiter to every request the keyer selects. It
// writes RateLimit-* headers on every limited response and answers 429 with
// Retry-After and the pt-BR envelope when the limit is exceeded. Limiter
// errors fail open (the request proceeds) and are logged: a broken limiter
// must not take the API down.
func Middleware(l Limiter, keyer Keyer) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, policy, ok := keyer(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			d, err := l.Allow(r.Context(), key, policy)
			if err != nil {
				logx.From(r.Context(), nil).Error("limitador indisponível", slog.String("err", err.Error()))
				next.ServeHTTP(w, r)
				return
			}
			SetHeaders(w.Header(), policy, d)
			if !d.Allowed {
				w.Header().Set(HeaderRetry, strconv.Itoa(ceilSeconds(d.RetryAfter)))
				httpx.WriteError(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SetHeaders writes the RateLimit-* headers for a decision.
func SetHeaders(h http.Header, policy Policy, d Decision) {
	h.Set(HeaderLimit, strconv.Itoa(d.Limit))
	h.Set(HeaderRemaining, strconv.Itoa(d.Remaining))
	h.Set(HeaderReset, strconv.Itoa(ceilSeconds(d.ResetAfter)))
	h.Set(HeaderPolicy, policy.String())
}

// ceilSeconds rounds a duration up to whole seconds, never below 1 for a
// positive duration (clients treat 0 as "retry immediately").
func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(d.Seconds()))
}
