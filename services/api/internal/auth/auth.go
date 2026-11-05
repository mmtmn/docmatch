// Package auth implements the login endpoints of the API: Google and Apple
// (OpenID Connect with PKCE where supported, state and nonce bound to a
// sealed short-lived cookie), e-mail (a six-digit code and a magic-link token
// in the same message, single use, fifteen minutes, five attempts), an
// optional password (Argon2id) with a hook for a second factor, and the
// session refresh and logout endpoints on top of internal/session.
//
// Every login path ends in completeLogin: find or create the user and the
// personal organization (with its wrapped tenant data key), refuse suspended
// or deletion-pending accounts, issue the session cookies and record the
// audit entry. Rate limits combine an IP bucket with the e-mail (or the
// challenge) and an exponential lockout, so guessing slows down geometrically.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// Providers.
const (
	ProviderGoogle   = "google"
	ProviderApple    = "apple"
	ProviderEmail    = "email"
	ProviderPassword = "password"
)

// Routes (the API is served under /v1).
const (
	GoogleStartPath    = "/v1/auth/google/start"
	GoogleCallbackPath = "/v1/auth/google/callback"
	AppleStartPath     = "/v1/auth/apple/start"
	AppleCallbackPath  = "/v1/auth/apple/callback"
	EmailStartPath     = "/v1/auth/email/start"
	EmailVerifyPath    = "/v1/auth/email/verify"
	PasswordLoginPath  = "/v1/auth/password/login" //nolint:gosec // route path, not a credential
	RefreshPath        = "/v1/auth/refresh"
	LogoutPath         = "/v1/auth/logout"
)

// Web app paths the API redirects to or links. The web app (apps/web) owns
// them; change them here when its routes change.
const (
	// DefaultLoginPath is where OAuth errors redirect (?error=<code>).
	DefaultLoginPath = "/login"
	// DefaultMagicLinkPath receives ?challenge_id=<id>&code=<token> from the
	// e-mail and posts it to /auth/email/verify.
	DefaultMagicLinkPath = "/login/email"
)

// Timings.
const (
	ChallengeTTL      = 15 * time.Minute
	ChallengeAttempts = 5
	ResendAfter       = 60 * time.Second
	OAuthStateTTL     = 10 * time.Minute
	maxRedirectTo     = 512
	challengePurpose  = "login"
	codeDigits        = 6
	defaultPlan       = "starter"
	auditLogin        = "auth.login"
	auditLogout       = "auth.logout"
	auditRefresh      = "auth.refresh"
	auditSessionReuse = "auth.session_reuse"
	auditSignup       = "auth.signup"
	auditLoginRefused = "auth.login_refused"
	auditProviderLink = "auth.provider_linked"
	oauthCookiePrefix = "dm_oauth_"
	acceptJSON        = "application/json"
	formContentType   = "application/x-www-form-urlencoded"
	maxAppleUserJSON  = 2048
	identityNameLimit = 80
)

// Errors returned by the service.
var (
	ErrProviderUnavailable = errors.New("auth: provider not configured")
	ErrEmailUnavailable    = errors.New("auth: e-mail sender not configured")
	ErrStateMismatch       = errors.New("auth: oauth state mismatch")
	ErrProvider            = errors.New("auth: provider error")
	ErrEmailUnverified     = errors.New("auth: e-mail not verified by the provider")
	ErrSuspended           = errors.New("auth: account suspended")
	ErrDeletionPending     = errors.New("auth: account deletion pending")
	ErrChallengeNotFound   = errors.New("auth: challenge not found")
	ErrCodeInvalid         = errors.New("auth: code invalid")
	ErrCodeExpired         = errors.New("auth: code expired")
	ErrTooManyAttempts     = errors.New("auth: too many attempts")
	ErrInvalidCredentials  = errors.New("auth: invalid credentials")
	ErrNoTenant            = errors.New("auth: user has no organization")
)

// Identity is what a provider asserts about the person logging in.
type Identity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	// Name is set only when the provider sends one (Apple: first login only).
	Name         string
	PrivateEmail bool
}

// Challenge is an e-mail login request (magic_links row).
type Challenge struct {
	ID         uuid.UUID
	Email      string
	TokenHash  []byte
	CodeHash   []byte
	Purpose    string
	IPPrefix   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	Attempts   int
}

// Store is the persistence boundary. Users have no RLS; organizations and
// memberships do, so implementations scope those queries (signup opens the
// transaction for the new organization; tenant lookups run for the given
// organization or in system mode when the organization is unknown yet).
type Store interface {
	UserByID(ctx context.Context, id uuid.UUID) (account.User, bool, error)
	UserByEmail(ctx context.Context, email string) (account.User, bool, error)
	UserByProvider(ctx context.Context, provider, sub string) (account.User, bool, error)
	// Signup creates the user, the personal organization (o.ID is set by the
	// caller), the owner membership and the wrapped tenant key atomically.
	Signup(ctx context.Context, u account.User, o account.Org, wrappedKey []byte) (account.User, account.Org, error)
	LinkProvider(ctx context.Context, userID uuid.UUID, provider, sub string) error
	MarkEmailVerified(ctx context.Context, userID uuid.UUID) error
	SetDisplayName(ctx context.Context, userID uuid.UUID, name string) error
	TouchLogin(ctx context.Context, userID uuid.UUID) error
	PrimaryTenant(ctx context.Context, userID uuid.UUID) (account.Tenant, bool, error)
	Tenant(ctx context.Context, orgID, userID uuid.UUID) (account.Tenant, bool, error)

	// CreateChallenge stores the challenge and the e-mail in one transaction.
	CreateChallenge(ctx context.Context, ch Challenge, msg email.Message) error
	ChallengeByID(ctx context.Context, id uuid.UUID) (Challenge, bool, error)
	// ClaimChallengeAttempt atomically spends one attempt while fewer than max
	// were spent and the challenge is unconsumed; ok is false otherwise. The
	// handler claims before comparing, so concurrent guesses cannot exceed max.
	ClaimChallengeAttempt(ctx context.Context, id uuid.UUID, maxAttempts int) (int, bool, error)
	// ConsumeChallenge marks the challenge used; ok is false when it was
	// already consumed or expired (single use is decided by the store).
	ConsumeChallenge(ctx context.Context, id uuid.UUID) (Challenge, bool, error)
}

// SecondFactor is the hook for TOTP (not implemented yet): Verify runs after
// the password check and before the session is issued.
type SecondFactor interface {
	// Required reports whether the user enrolled a second factor.
	Required(ctx context.Context, u account.User) (bool, error)
	// Verify checks the submitted code; an error refuses the login.
	Verify(ctx context.Context, u account.User, code string) error
}

// NoSecondFactor is the default hook: nobody is enrolled.
type NoSecondFactor struct{}

// Required implements SecondFactor.
func (NoSecondFactor) Required(context.Context, account.User) (bool, error) { return false, nil }

// Verify implements SecondFactor.
func (NoSecondFactor) Verify(context.Context, account.User, string) error { return nil }

// Deps are the collaborators of the service.
type Deps struct {
	Store    Store
	Sessions *session.Manager
	Keys     *crypto.Keyring
	Limits   *limits.Limits
	Audit    account.Recorder
	// Google and Apple are nil when the provider is not configured (503).
	Google *Provider
	Apple  *Provider
	// EmailEnabled is false when no sender is configured (503 on e-mail login).
	EmailEnabled bool
	// PublicBaseURL is the web app (redirects, magic links); APIBaseURL
	// builds the OAuth redirect URIs.
	PublicBaseURL *url.URL
	APIBaseURL    *url.URL
	// Secure sets the Secure cookie attribute (false only in dev over http).
	Secure        bool
	LoginPath     string
	MagicLinkPath string
	// Limiter, Lockout and Policy shape the auth rate limit.
	Limiter  ratelimit.Limiter
	Lockout  *ratelimit.Lockout
	Policy   ratelimit.Policy
	ClientIP func(*http.Request) netip.Addr
	// Country resolves the viewer country for the new organization ("" unknown).
	Country      func(*http.Request) string
	SecondFactor SecondFactor
	Logger       *slog.Logger
	Now          func() time.Time
}

// Service holds the login flows.
type Service struct {
	d     Deps
	guard *ratelimit.AuthGuard
}

// New validates the dependencies and returns the service.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Sessions == nil || d.Keys == nil || d.Audit == nil {
		return nil, errors.New("auth: Store, Sessions, Keys and Audit are required")
	}
	if d.PublicBaseURL == nil || d.APIBaseURL == nil {
		return nil, errors.New("auth: PublicBaseURL and APIBaseURL are required")
	}
	if d.Limits == nil {
		d.Limits = limits.Must()
	}
	if d.LoginPath == "" {
		d.LoginPath = DefaultLoginPath
	}
	if d.MagicLinkPath == "" {
		d.MagicLinkPath = DefaultMagicLinkPath
	}
	if d.Limiter == nil {
		d.Limiter = ratelimit.NewMemory()
	}
	if d.Lockout == nil {
		d.Lockout = ratelimit.NewLockout(ratelimit.DefaultLockoutPolicy(), nil)
	}
	if d.Policy.Limit == 0 {
		d.Policy = ratelimit.Defaults().Auth
	}
	if d.ClientIP == nil {
		d.ClientIP = remoteIP
	}
	if d.Country == nil {
		d.Country = func(*http.Request) string { return "" }
	}
	if d.SecondFactor == nil {
		d.SecondFactor = NoSecondFactor{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, guard: ratelimit.NewAuthGuard(d.Limiter, d.Lockout, d.Policy)}, nil
}

// OriginExempt marks the Apple callback, a cross-site form post from
// appleid.apple.com, as exempt from the Origin check of the standard
// middleware stack. The state cookie and the nonce protect that request.
func OriginExempt(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == AppleCallbackPath
}

func remoteIP(r *http.Request) netip.Addr {
	host := r.RemoteAddr
	if ap, err := netip.ParseAddrPort(host); err == nil {
		return ap.Addr()
	}
	addr, _ := netip.ParseAddr(host)
	return addr
}
