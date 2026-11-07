package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/emailpolicy"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// loginResult is what every successful login produces.
type loginResult struct {
	User    account.User
	Tenant  account.Tenant
	Session session.Session
	Created bool
}

// limitKey builds the limiter key for an address and an e-mail (or any
// other secondary identifier; empty means per address only).
func limitKey(ip netip.Addr, second string) string {
	return ratelimit.AuthKey(ip, second)
}

// allow applies the auth rate limit and the lockout; it writes the 429 and
// returns false when the request must stop.
func (s *Service) allow(w http.ResponseWriter, r *http.Request, key string) bool {
	locked := s.d.Lockout.Remaining(key) > 0
	d, err := s.guard.Allow(r.Context(), key)
	if err != nil {
		logx.From(r.Context(), s.d.Logger).Error("limitador indisponível", slog.String("err", err.Error()))
		return true // fail open: a broken limiter must not take logins down
	}
	ratelimit.SetHeaders(w.Header(), s.d.Policy, d)
	if d.Allowed {
		return true
	}
	w.Header().Set(ratelimit.HeaderRetry, retryAfter(d.RetryAfter))
	if locked {
		apierr.Write(w, r, contracts.ErrTooManyAttempts)
	} else {
		apierr.Write(w, r, contracts.ErrRateLimited)
	}
	return false
}

func retryAfter(d time.Duration) string {
	secs := int(d / time.Second)
	if d%time.Second != 0 || secs == 0 {
		secs++
	}
	return itoa(secs)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// normalizeEmail validates and canonicalizes an e-mail address: NFC, lower
// case, no display name, bounded by limits.yaml.
func (s *Service) normalizeEmail(raw string) (string, bool) {
	v, err := httpx.ValidateText(raw, s.d.Limits.Text.Email, true)
	if err != nil || v == "" {
		return "", false
	}
	v = strings.ToLower(v)
	at := strings.LastIndex(v, "@")
	if at < 1 || at == len(v)-1 || strings.ContainsAny(v, " <>()[],;:\"\\") {
		return "", false
	}
	domain := v[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return "", false
	}
	return v, true
}

// completeLogin finds or creates the user for id (known may carry an already
// loaded user, as in the password flow), refuses blocked accounts, issues
// the session and records the audit entry.
func (s *Service) completeLogin(ctx context.Context, w http.ResponseWriter, r *http.Request, id Identity, known *account.User, meta map[string]any) (loginResult, error) {
	var (
		u       account.User
		created bool
		err     error
	)
	if known != nil {
		u = *known
	} else {
		u, created, err = s.resolveUser(ctx, r, id)
		if err != nil {
			return loginResult{}, err
		}
	}
	if u.SuspendedAt != nil {
		return loginResult{}, ErrSuspended
	}
	if u.DeletionRequestedAt != nil {
		return loginResult{}, ErrDeletionPending
	}
	if id.appleCredential != nil {
		if err := s.d.AppleTokens.Save(ctx, u.ID, id.appleCredential.clientID, id.appleCredential.refreshToken); err != nil {
			return loginResult{}, err
		}
	}
	if id.Name != "" && u.DisplayName == "" {
		if name, nerr := httpx.ValidateText(id.Name, s.d.Limits.Text.DisplayName, true); nerr == nil && name != "" {
			if err := s.d.Store.SetDisplayName(ctx, u.ID, name); err != nil {
				return loginResult{}, err
			}
			u.DisplayName = name
		}
	}
	t, ok, err := s.d.Store.PrimaryTenant(ctx, u.ID)
	if err != nil {
		return loginResult{}, err
	}
	if !ok {
		return loginResult{}, ErrNoTenant
	}
	if err := s.d.Store.TouchLogin(ctx, u.ID); err != nil {
		return loginResult{}, err
	}
	sess, err := s.d.Sessions.Issue(ctx, w, r, u.ID, t.Org.ID)
	if err != nil {
		return loginResult{}, err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["method"] = id.Provider
	meta["session_id"] = sess.ID.String()
	meta["ip_prefix"] = sess.IPPrefix
	meta["new_user"] = created
	if err := s.record(ctx, t.Org.ID, u.ID, auditLogin, audit.Target{Type: "session", ID: sess.ID.String()}, meta); err != nil {
		// The session exists but the trail does not: undo and fail closed.
		_, _ = s.d.Sessions.Store().Revoke(ctx, sess.ID)
		s.d.Sessions.Clear(w)
		return loginResult{}, err
	}
	return loginResult{User: u, Tenant: t, Session: sess, Created: created}, nil
}

// resolveUser maps a provider identity to a user: by provider subject, then
// by verified e-mail (linking the provider), else a new account.
func (s *Service) resolveUser(ctx context.Context, r *http.Request, id Identity) (account.User, bool, error) {
	if id.Provider == ProviderGoogle || id.Provider == ProviderApple {
		u, ok, err := s.d.Store.UserByProvider(ctx, id.Provider, id.Subject)
		if err != nil {
			return account.User{}, false, err
		}
		if ok {
			return u, false, nil
		}
	}
	if !id.EmailVerified {
		return account.User{}, false, ErrEmailUnverified
	}
	if id.Email == "" {
		return account.User{}, false, ErrEmailUnverified
	}
	u, ok, err := s.d.Store.UserByEmail(ctx, id.Email)
	if err != nil {
		return account.User{}, false, err
	}
	if ok {
		switch id.Provider {
		case ProviderGoogle, ProviderApple:
			if err := s.d.Store.LinkProvider(ctx, u.ID, id.Provider, id.Subject); err != nil {
				return account.User{}, false, err
			}
			if err := s.recordSafe(ctx, u.ID, auditProviderLink, audit.Target{Type: "user", ID: u.ID.String()}, map[string]any{"provider": id.Provider}); err != nil {
				return account.User{}, false, err
			}
		default:
			if u.EmailVerifiedAt == nil {
				if err := s.d.Store.MarkEmailVerified(ctx, u.ID); err != nil {
					return account.User{}, false, err
				}
			}
		}
		return u, false, nil
	}
	return s.signup(ctx, r, id)
}

// signup creates the user with a personal organization and its data key.
func (s *Service) signup(ctx context.Context, r *http.Request, id Identity) (account.User, bool, error) {
	if emailpolicy.Disposable(id.Email) {
		return account.User{}, false, ErrDisposableEmail
	}
	now := s.d.Now()
	orgID, err := uuid.NewV7()
	if err != nil {
		return account.User{}, false, err
	}
	name := ""
	if id.Name != "" {
		if v, nerr := httpx.ValidateText(id.Name, s.d.Limits.Text.DisplayName, true); nerr == nil {
			name = v
		}
	}
	orgName := name
	if orgName == "" {
		orgName = id.Email[:strings.LastIndex(id.Email, "@")]
	}
	if n := s.d.Limits.Text.CompanyName; httpx.CountChars(orgName) > n {
		orgName = string([]rune(orgName)[:n])
	}
	u := account.User{Email: id.Email, DisplayName: name, EmailVerifiedAt: &now}
	switch id.Provider {
	case ProviderGoogle:
		u.GoogleSub = id.Subject
	case ProviderApple:
		u.AppleSub = id.Subject
	}
	o := account.Org{ID: orgID, Name: orgName, Slug: strings.ToLower(publicid.Encode(publicid.PrefixOrg, orgID)), PlanCode: defaultPlan, Country: s.d.Country(r)}
	wrapped, err := crypto.WrapDataKey(s.d.Keys.Key(crypto.PurposeKEK), crypto.NewDataKey(), orgID.String())
	if err != nil {
		return account.User{}, false, err
	}
	u, o, err = s.d.Store.Signup(ctx, u, o, wrapped)
	if err != nil {
		return account.User{}, false, err
	}
	if err := s.record(ctx, o.ID, u.ID, auditSignup, audit.Target{Type: "org", ID: o.ID.String()}, map[string]any{"provider": id.Provider, "private_email": id.PrivateEmail}); err != nil {
		return account.User{}, false, err
	}
	return u, true, nil
}

func (s *Service) record(ctx context.Context, orgID, userID uuid.UUID, action string, target audit.Target, meta any) error {
	var org *uuid.UUID
	if orgID != uuid.Nil {
		org = &orgID
	}
	_, err := s.d.Audit.Record(ctx, org, audit.Actor{Type: audit.ActorUser, ID: userID.String()}, action, target, meta)
	return err
}

// recordSafe records on the user's primary organization chain, or on the
// system chain when the user has none.
func (s *Service) recordSafe(ctx context.Context, userID uuid.UUID, action string, target audit.Target, meta any) error {
	t, ok, err := s.d.Store.PrimaryTenant(ctx, userID)
	if err != nil {
		return err
	}
	org := uuid.Nil
	if ok {
		org = t.Org.ID
	}
	return s.record(ctx, org, userID, action, target, meta)
}

// logRefused logs a refused login without personal data.
func (s *Service) logRefused(r *http.Request, id Identity, err error) {
	logx.From(r.Context(), s.d.Logger).Warn("login recusado", slog.String("provider", id.Provider), slog.String("reason", err.Error()))
}

// writeSession answers 200 with the Session schema.
func (s *Service) writeSession(w http.ResponseWriter, out loginResult) {
	httpx.WriteJSON(w, http.StatusOK, account.NewSessionView(out.User, out.Tenant, out.Session.AccessExpiresAt))
}
