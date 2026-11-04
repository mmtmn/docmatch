package session

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/db"
)

// Principal is the authenticated caller stored in the request context.
type Principal struct {
	SessionID        uuid.UUID
	UserID           uuid.UUID
	OrgID            uuid.UUID
	SessionCreatedAt time.Time
	AccessExpiresAt  time.Time
}

type principalKey struct{}
type authErrKey struct{}

// ContextWithPrincipal stores p in ctx (tests and internal callers).
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal stored by Middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// AuthError returns why Middleware could not authenticate the request (nil
// when there was no session cookie or the principal is present).
func AuthError(ctx context.Context) error {
	err, _ := ctx.Value(authErrKey{}).(error)
	return err
}

// Middleware authenticates the session cookie when present and stores the
// principal (or the failure) in the context. It also enforces the Origin
// check for state-changing requests that carry a session cookie: browsers
// always send Origin or Sec-Fetch-Site on such requests, so a cross-site
// forgery is rejected even if a cookie slipped through SameSite=Lax. It
// never rejects for a missing or bad cookie; Require does that.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !HasSessionCookie(r) {
			next.ServeHTTP(w, r)
			return
		}
		if stateChanging(r.Method) && !m.sameOrigin(r) {
			apierr.Write(w, r, contracts.ErrForbidden)
			return
		}
		ctx := r.Context()
		p, err := m.Authenticate(ctx, r)
		if err != nil {
			ctx = context.WithValue(ctx, authErrKey{}, err)
		} else {
			ctx = ContextWithPrincipal(ctx, p)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sameOrigin applies the allowlist: an Origin header must be allowed; without
// one, Sec-Fetch-Site must not say cross-site. Requests with neither header
// come from non-browser clients, which cannot be forged by a web page.
func (m *Manager) sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return m.origins.Allowed(origin)
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func stateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// Require answers 401 when no principal is present: session_expired when the
// access window closed (the client should refresh), unauthenticated otherwise.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			if errors.Is(AuthError(r.Context()), ErrAccessExpired) {
				apierr.Write(w, r, contracts.ErrSessionExpired)
				return
			}
			apierr.Write(w, r, contracts.ErrUnauthenticated)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireFunc is Require for plain functions.
func RequireFunc(h http.HandlerFunc) http.Handler { return Require(h) }

// WithOrgTx runs fn in a transaction scoped to the principal's organization
// (SET LOCAL app.org_id, so row-level security sees exactly that tenant).
// Every handler that touches tenant tables goes through it; ErrNoTenant is
// returned when the context has no authenticated principal with an
// organization.
func WithOrgTx(ctx context.Context, b db.Beginner, fn db.TxFunc) error {
	p, ok := FromContext(ctx)
	if !ok || p.OrgID == uuid.Nil {
		return ErrNoTenant
	}
	return db.WithOrgTx(ctx, b, p.OrgID, fn)
}
