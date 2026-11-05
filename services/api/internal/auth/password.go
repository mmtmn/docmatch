package auth

import (
	"errors"
	"net/http"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

type passwordLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

// passwordLogin handles POST /auth/password/login. Unknown address, missing
// password and wrong password all take the same path (a dummy hash is
// verified when there is nothing to compare against) and the same answer.
func (s *Service) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordLoginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	addr, ok := s.normalizeEmail(req.Email)
	if !ok {
		apierr.Validation(w, r, apierr.Field{Path: "/email", Code: apierr.FieldInvalid, Max: s.d.Limits.Text.Email})
		return
	}
	if req.Password == "" || httpx.CountChars(req.Password) > s.d.Limits.Text.PasswordMax {
		apierr.Validation(w, r, apierr.Field{Path: "/password", Code: apierr.FieldInvalid, Max: s.d.Limits.Text.PasswordMax})
		return
	}
	if req.TOTPCode != "" && !digitsRe.MatchString(req.TOTPCode) {
		apierr.Validation(w, r, apierr.Field{Path: "/totp_code", Code: apierr.FieldInvalid})
		return
	}
	key := limitKey(s.d.ClientIP(r), addr)
	if !s.allow(w, r, key) {
		return
	}
	u, found, err := s.d.Store.UserByEmail(r.Context(), addr)
	if err != nil {
		apierr.Internal(w, r, err)
		return
	}
	hash := u.PasswordHash
	if !found || hash == "" {
		hash = crypto.DummyHash()
	}
	match, verr := crypto.VerifyPassword(hash, req.Password)
	if !found || u.PasswordHash == "" || verr != nil || !match {
		s.d.Lockout.Strike(key)
		apierr.Write(w, r, contracts.ErrInvalidCredentials)
		return
	}
	if err := s.secondFactor(r, u, req.TOTPCode); err != nil {
		s.d.Lockout.Strike(key)
		apierr.Write(w, r, codeFor(err))
		return
	}
	if crypto.NeedsRehash(u.PasswordHash) {
		s.d.Logger.Info("senha com parâmetros antigos; rehash pendente", "user_id", u.ID.String())
	}
	_ = s.guard.Succeed(r.Context(), key)
	identity := Identity{Provider: ProviderPassword, Email: u.Email, EmailVerified: true}
	out, err := s.completeLogin(r.Context(), w, r, identity, &u, map[string]any{"second_factor": req.TOTPCode != ""})
	if err != nil {
		s.logRefused(r, identity, err)
		apierr.Write(w, r, codeFor(err))
		return
	}
	s.writeSession(w, out)
}

// secondFactor runs the hook: an enrolled user must present a valid code.
func (s *Service) secondFactor(r *http.Request, u account.User, code string) error {
	required, err := s.d.SecondFactor.Required(r.Context(), u)
	if err != nil {
		return err
	}
	if !required {
		return nil
	}
	if code == "" {
		return ErrInvalidCredentials
	}
	if err := s.d.SecondFactor.Verify(r.Context(), u, code); err != nil {
		return errors.Join(ErrInvalidCredentials, err)
	}
	return nil
}

// refresh handles POST /auth/refresh.
func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, err := s.d.Sessions.Refresh(ctx, w, r)
	if err != nil {
		if errors.Is(err, session.ErrReuse) || errors.Is(err, session.ErrDeviceMismatch) {
			_ = s.record(ctx, sess.OrgID, sess.UserID, auditSessionReuse, audit.Target{Type: "session", ID: sess.ID.String()},
				map[string]any{"family_id": sess.FamilyID.String(), "reason": err.Error()})
		}
		s.d.Sessions.Clear(w)
		apierr.Write(w, r, contracts.ErrUnauthenticated)
		return
	}
	u, ok, err := s.d.Store.UserByID(ctx, sess.UserID)
	if err != nil {
		apierr.Internal(w, r, err)
		return
	}
	if !ok || u.DeletionRequestedAt != nil || u.SuspendedAt != nil {
		_, _ = s.d.Sessions.Store().Revoke(ctx, sess.ID)
		s.d.Sessions.Clear(w)
		apierr.Write(w, r, contracts.ErrUnauthenticated)
		return
	}
	t, ok, err := s.d.Store.Tenant(ctx, sess.OrgID, u.ID)
	if err != nil {
		apierr.Internal(w, r, err)
		return
	}
	if !ok {
		_, _ = s.d.Sessions.Store().Revoke(ctx, sess.ID)
		s.d.Sessions.Clear(w)
		apierr.Write(w, r, contracts.ErrUnauthenticated)
		return
	}
	if err := s.record(ctx, t.Org.ID, u.ID, auditRefresh, audit.Target{Type: "session", ID: sess.ID.String()}, map[string]any{"rotations": sess.Rotations}); err != nil {
		apierr.Internal(w, r, err)
		return
	}
	s.writeSession(w, loginResult{User: u, Tenant: t, Session: sess})
}

// logout handles POST /auth/logout.
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, err := s.d.Sessions.Logout(ctx, w, r)
	if err != nil {
		apierr.Write(w, r, contracts.ErrUnauthenticated)
		return
	}
	if err := s.record(ctx, sess.OrgID, sess.UserID, auditLogout, audit.Target{Type: "session", ID: sess.ID.String()}, nil); err != nil {
		apierr.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Mount registers the authentication routes.
func (s *Service) Mount(r *httpx.Router) {
	r.HandleFunc(http.MethodPost, GoogleStartPath, httpx.BodyJSON, s.start(ProviderGoogle))
	r.HandleFunc(http.MethodGet, GoogleCallbackPath, httpx.BodyNone, s.googleCallback)
	r.HandleFunc(http.MethodPost, AppleStartPath, httpx.BodyJSON, s.start(ProviderApple))
	r.HandleFunc(http.MethodPost, AppleCallbackPath, httpx.BodyJSON, s.appleCallback)
	r.HandleFunc(http.MethodPost, EmailStartPath, httpx.BodyJSON, s.emailStart)
	r.HandleFunc(http.MethodPost, EmailVerifyPath, httpx.BodyJSON, s.emailVerify)
	r.HandleFunc(http.MethodPost, PasswordLoginPath, httpx.BodyJSON, s.passwordLogin)
	r.HandleFunc(http.MethodPost, RefreshPath, httpx.BodyNone, s.refresh)
	r.HandleFunc(http.MethodPost, LogoutPath, httpx.BodyNone, s.logout)
	r.HandleFunc(http.MethodGet, MethodsPath, httpx.BodyNone, s.methods)
	r.HandleFunc(http.MethodPost, GoogleNativePath, httpx.BodyJSON, s.googleNative)
	if s.d.DevLogin {
		r.HandleFunc(http.MethodPost, DevLoginPath, httpx.BodyJSON, s.devLogin)
	}
}
