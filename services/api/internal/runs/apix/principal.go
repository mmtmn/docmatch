package apix

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
)

type recheckKey struct{}

// RecheckContext revalidates credentials on a stream already counted against
// request limits. This flag is internal context, never a client-supplied header.
func RecheckContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, recheckKey{}, true)
}
func IsRecheck(ctx context.Context) bool { v, _ := ctx.Value(recheckKey{}).(bool); return v }

// Principal is the authenticated caller of a business endpoint: a user with
// a session or an API key, always inside exactly one organization.
type Principal struct {
	OrgID uuid.UUID
	// UserID is set for sessions; nil for API keys.
	UserID *uuid.UUID
	// APIKeyID is set for API keys; nil for sessions.
	APIKeyID *uuid.UUID
	// Plan is the organization's plan code (config/pricing.yaml plans).
	Plan           string
	MembershipRole string
	// Lanes an API key may use (empty for sessions: the app lane).
	Lanes []string
	// Scopes granted to an API key (empty for sessions: everything).
	Scopes []string
}

// Actor renders the principal for the audit trail.
func (p Principal) Actor() audit.Actor {
	if p.APIKeyID != nil {
		return audit.Actor{Type: audit.ActorAPIKey, ID: Encode(PrefixAPIKey, *p.APIKeyID)}
	}
	if p.UserID != nil {
		return audit.Actor{Type: audit.ActorUser, ID: Encode(PrefixUser, *p.UserID)}
	}
	return audit.Actor{Type: audit.ActorSystem, ID: "anonymous"}
}

// IsAPIKey reports whether the caller authenticated with an API key.
func (p Principal) IsAPIKey() bool { return p.APIKeyID != nil }

// Authentication errors a Resolver may return (wrapped or not).
var (
	ErrUnauthenticated = errors.New("apix: unauthenticated")
	ErrSessionExpired  = errors.New("apix: session expired")
	ErrInvalidAPIKey   = errors.New("apix: invalid api key")
	ErrForbidden       = errors.New("apix: forbidden")
	ErrScopeDenied     = errors.New("apix: api key scope denied")
)

// Resolver identifies the caller of a request. The auth subsystem provides
// the real one (cookie session or Bearer API key); handlers only see this
// function. It returns one of the errors above when the request carries no
// valid credential.
type Resolver func(r *http.Request) (Principal, error)

// Unauthenticated is the resolver used when no auth subsystem is wired: it
// refuses every request, so business endpoints are never open by accident.
func Unauthenticated() Resolver {
	return func(*http.Request) (Principal, error) { return Principal{}, ErrUnauthenticated }
}

// Static always answers with p (tests and the fake harness tooling).
func Static(p Principal) Resolver {
	return func(*http.Request) (Principal, error) { return p, nil }
}
