// Package session issues and validates browser sessions.
//
// A login creates one session row and three cookies:
//
//   - dm_session = "<id>.<access secret>", Path=/: presented on every request;
//     the server keeps SHA-256(access secret) and an access window of ten
//     minutes, after which the client must refresh (401 session_expired);
//   - dm_refresh = "<id>.<refresh secret>", Path=/v1/auth/refresh: presented
//     only to the refresh endpoint; rotated on every refresh with a sliding
//     thirty-day expiry;
//   - dm_device  = random id, Path=/, long-lived: the refresh token is bound
//     to SHA-256(device id), so a refresh cookie copied to another device is
//     useless on its own.
//
// Rotation happens in place (same id, same family). Presenting any refresh
// secret other than the current one for a known id, or the wrong device,
// means the token was stolen or replayed: the whole family is revoked. All
// cookies are HttpOnly, SameSite=Lax and Secure (Secure is dropped only in
// dev over plain http). "Stay signed in on this device" is the default: the
// cookies are persistent, not session-scoped.
package session

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

// Cookie names.
const (
	CookieSession = "dm_session"
	CookieRefresh = "dm_refresh"
	CookieDevice  = "dm_device"
)

// Defaults.
const (
	DefaultAccessTTL   = 10 * time.Minute
	DefaultRefreshTTL  = 30 * 24 * time.Hour
	DefaultDeviceTTL   = 400 * 24 * time.Hour
	DefaultRefreshPath = "/v1/auth/refresh"
	secretBytes        = 32
	maxUserAgent       = 256
)

// Errors. Handlers map them onto catalog codes: ErrAccessExpired is
// session_expired (refresh and retry); every other one is unauthenticated.
var (
	ErrNoSession      = errors.New("session: no session cookie")
	ErrInvalid        = errors.New("session: invalid session")
	ErrRevoked        = errors.New("session: session revoked")
	ErrExpired        = errors.New("session: session expired")
	ErrAccessExpired  = errors.New("session: access window expired")
	ErrReuse          = errors.New("session: refresh token reuse detected; family revoked")
	ErrDeviceMismatch = errors.New("session: device mismatch; family revoked")
	ErrNoTenant       = errors.New("session: no tenant in context")
)

// Session is one row of the sessions table.
type Session struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	OrgID    uuid.UUID // uuid.Nil when the user has not selected an organization
	FamilyID uuid.UUID

	DeviceHash  []byte
	RefreshHash []byte
	AccessHash  []byte

	IPPrefix  string
	UserAgent string

	CreatedAt       time.Time
	LastSeenAt      time.Time
	ExpiresAt       time.Time
	AccessExpiresAt time.Time
	RevokedAt       *time.Time
	RotatedAt       *time.Time
	Rotations       int
}

// Rotation is what Rotate stores.
type Rotation struct {
	RefreshHash     []byte
	AccessHash      []byte
	ExpiresAt       time.Time
	AccessExpiresAt time.Time
}

// Store is the persistence boundary; the sessions table has no RLS.
type Store interface {
	Insert(ctx context.Context, s Session) error
	// ByID returns the row in any state (revoked, expired) so the manager
	// can tell the cases apart.
	ByID(ctx context.Context, id uuid.UUID) (Session, bool, error)
	// Rotate applies r to a non-revoked row; ok is false when none matched.
	Rotate(ctx context.Context, id uuid.UUID, r Rotation) (Session, bool, error)
	Touch(ctx context.Context, id uuid.UUID) error
	Revoke(ctx context.Context, id uuid.UUID) (int64, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID) (int64, error)
	// RevokeUser revokes every session of the user except keep (uuid.Nil keeps none).
	RevokeUser(ctx context.Context, userID, keep uuid.UUID) (int64, error)
	ListActive(ctx context.Context, userID uuid.UUID) ([]Session, error)
}

// Config tunes the manager.
type Config struct {
	// Secure sets the Secure cookie attribute; false only in dev over http.
	Secure bool
	// AllowedOrigins is the browser origin allowlist for the CSRF check.
	AllowedOrigins []string
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	DeviceTTL      time.Duration
	// RefreshPath is the cookie path of dm_refresh (the refresh endpoint).
	RefreshPath string
	// Now substitutes the clock (tests).
	Now func() time.Time
	// ClientIP resolves the viewer address (geo.Edge.ClientIP); the default
	// parses RemoteAddr.
	ClientIP func(*http.Request) netip.Addr
}

func (c *Config) defaults() {
	if c.AccessTTL <= 0 {
		c.AccessTTL = DefaultAccessTTL
	}
	if c.RefreshTTL <= 0 {
		c.RefreshTTL = DefaultRefreshTTL
	}
	if c.DeviceTTL <= 0 {
		c.DeviceTTL = DefaultDeviceTTL
	}
	if c.RefreshPath == "" {
		c.RefreshPath = DefaultRefreshPath
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.ClientIP == nil {
		c.ClientIP = remoteAddr
	}
}

// Manager issues, validates, refreshes and revokes sessions.
type Manager struct {
	store   Store
	cfg     Config
	origins *httpx.OriginPolicy
}

// NewManager wires a store with the configuration.
func NewManager(store Store, cfg Config) *Manager {
	cfg.defaults()
	return &Manager{store: store, cfg: cfg, origins: httpx.NewOriginPolicy(cfg.AllowedOrigins)}
}

// Store exposes the underlying store (handlers that list sessions).
func (m *Manager) Store() Store { return m.store }

// AccessTTL returns the configured access window.
func (m *Manager) AccessTTL() time.Duration { return m.cfg.AccessTTL }

// Issue creates a session for userID in orgID and sets the three cookies.
func (m *Manager) Issue(ctx context.Context, w http.ResponseWriter, r *http.Request, userID, orgID uuid.UUID) (Session, error) {
	now := m.cfg.Now()
	id, err := uuid.NewV7()
	if err != nil {
		return Session{}, err
	}
	device := deviceID(r)
	if device == "" {
		device = crypto.RandomToken(secretBytes)
	}
	access := crypto.RandomToken(secretBytes)
	refresh := crypto.RandomToken(secretBytes)
	s := Session{
		ID: id, UserID: userID, OrgID: orgID, FamilyID: id,
		DeviceHash: crypto.Digest(device), RefreshHash: crypto.Digest(refresh), AccessHash: crypto.Digest(access),
		IPPrefix: ipPrefix(m.cfg.ClientIP(r)), UserAgent: truncate(r.UserAgent(), maxUserAgent),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(m.cfg.RefreshTTL), AccessExpiresAt: now.Add(m.cfg.AccessTTL),
	}
	if err := m.store.Insert(ctx, s); err != nil {
		return Session{}, err
	}
	m.setCookies(w, id, access, refresh, device)
	return s, nil
}

// Authenticate resolves the principal of the dm_session cookie.
func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	id, secret, err := cookieParts(r, CookieSession)
	if err != nil {
		return Principal{}, err
	}
	s, ok, err := m.store.ByID(ctx, id)
	if err != nil {
		return Principal{}, err
	}
	if !ok {
		return Principal{}, ErrInvalid
	}
	now := m.cfg.Now()
	switch {
	case s.RevokedAt != nil:
		return Principal{}, ErrRevoked
	case !now.Before(s.ExpiresAt):
		return Principal{}, ErrExpired
	case !crypto.Equal(crypto.Digest(secret), s.AccessHash):
		return Principal{}, ErrInvalid
	case !now.Before(s.AccessExpiresAt):
		return Principal{}, ErrAccessExpired
	}
	_ = m.store.Touch(ctx, id) // last_seen_at is informational; a failure must not block the request
	return Principal{SessionID: s.ID, UserID: s.UserID, OrgID: s.OrgID, SessionCreatedAt: s.CreatedAt, AccessExpiresAt: s.AccessExpiresAt}, nil
}

// Refresh rotates the refresh and access secrets of the session named by
// dm_refresh, bound to dm_device, and sets fresh cookies. A stale refresh
// secret or a foreign device revokes the whole family.
func (m *Manager) Refresh(ctx context.Context, w http.ResponseWriter, r *http.Request) (Session, error) {
	id, secret, err := cookieParts(r, CookieRefresh)
	if err != nil {
		return Session{}, err
	}
	s, ok, err := m.store.ByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrInvalid
	}
	now := m.cfg.Now()
	if s.RevokedAt != nil {
		return Session{}, ErrRevoked
	}
	if !now.Before(s.ExpiresAt) {
		return Session{}, ErrExpired
	}
	// On reuse or device mismatch the row is returned with the error so the
	// caller can audit the revocation against the right user and tenant.
	if !crypto.Equal(crypto.Digest(secret), s.RefreshHash) {
		if _, err := m.store.RevokeFamily(ctx, s.FamilyID); err != nil {
			return Session{}, err
		}
		return s, ErrReuse
	}
	device := deviceID(r)
	if device == "" || !crypto.Equal(crypto.Digest(device), s.DeviceHash) {
		if _, err := m.store.RevokeFamily(ctx, s.FamilyID); err != nil {
			return Session{}, err
		}
		return s, ErrDeviceMismatch
	}
	access := crypto.RandomToken(secretBytes)
	refresh := crypto.RandomToken(secretBytes)
	rotated, ok, err := m.store.Rotate(ctx, id, Rotation{
		RefreshHash: crypto.Digest(refresh), AccessHash: crypto.Digest(access),
		ExpiresAt: now.Add(m.cfg.RefreshTTL), AccessExpiresAt: now.Add(m.cfg.AccessTTL),
	})
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrRevoked
	}
	m.setCookies(w, id, access, refresh, device)
	return rotated, nil
}

// Logout revokes the session of dm_session and clears every cookie. It
// returns the revoked session (for the audit trail).
func (m *Manager) Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) (Session, error) {
	defer m.Clear(w)
	id, _, err := cookieParts(r, CookieSession)
	if err != nil {
		return Session{}, err
	}
	s, ok, err := m.store.ByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrInvalid
	}
	if s.RevokedAt != nil {
		return Session{}, ErrRevoked
	}
	if _, err := m.store.Revoke(ctx, id); err != nil {
		return Session{}, err
	}
	return s, nil
}

// Clear expires the session and refresh cookies (the device cookie stays:
// it identifies the browser, not the login).
func (m *Manager) Clear(w http.ResponseWriter) {
	m.setCookie(w, CookieSession, "", "/", -1)
	m.setCookie(w, CookieRefresh, "", m.cfg.RefreshPath, -1)
}

func (m *Manager) setCookies(w http.ResponseWriter, id uuid.UUID, access, refresh, device string) {
	sid := encodeID(id)
	m.setCookie(w, CookieSession, sid+"."+access, "/", m.cfg.RefreshTTL)
	m.setCookie(w, CookieRefresh, sid+"."+refresh, m.cfg.RefreshPath, m.cfg.RefreshTTL)
	m.setCookie(w, CookieDevice, device, "/", m.cfg.DeviceTTL)
}

func (m *Manager) setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	c := &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are set; Secure follows DM_ENV (off only in dev over http)
		Name: name, Value: value, Path: path,
		HttpOnly: true, Secure: m.cfg.Secure, SameSite: http.SameSiteLaxMode,
	}
	if maxAge < 0 {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(maxAge / time.Second)
	}
	http.SetCookie(w, c)
}

// encodeID renders a uuid as 22 unpadded base64url characters.
func encodeID(id uuid.UUID) string { return base64.RawURLEncoding.EncodeToString(id[:]) }

func decodeID(s string) (uuid.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) != 16 {
		return uuid.Nil, false
	}
	var id uuid.UUID
	copy(id[:], raw)
	return id, true
}

// cookieParts splits "<id>.<secret>" and validates both halves before any
// lookup, so malformed cookies never reach the database.
func cookieParts(r *http.Request, name string) (uuid.UUID, string, error) {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" {
		return uuid.Nil, "", ErrNoSession
	}
	sid, secret, ok := strings.Cut(c.Value, ".")
	if !ok || !crypto.ValidToken(secret, secretBytes) {
		return uuid.Nil, "", ErrInvalid
	}
	id, ok := decodeID(sid)
	if !ok || id == uuid.Nil {
		return uuid.Nil, "", ErrInvalid
	}
	return id, secret, nil
}

// deviceID returns the device cookie when it is well formed, else "".
func deviceID(r *http.Request) string {
	c, err := r.Cookie(CookieDevice)
	if err != nil || !crypto.ValidToken(c.Value, secretBytes) {
		return ""
	}
	return c.Value
}

// HasSessionCookie reports whether r carries a dm_session cookie at all.
func HasSessionCookie(r *http.Request) bool {
	c, err := r.Cookie(CookieSession)
	return err == nil && c.Value != ""
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// IPPrefix buckets the address like the rate limiter (/24 v4, /64 v6) so the
// stored value is coarse enough not to be personal data on its own.
func IPPrefix(ip netip.Addr) string { return ipPrefix(ip) }

func ipPrefix(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	ip = ip.Unmap()
	bits := 64
	if ip.Is4() {
		bits = 24
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return ""
	}
	return p.Masked().String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
