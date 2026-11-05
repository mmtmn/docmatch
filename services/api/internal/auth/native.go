package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

// ProviderDev names the local sign-in (DM_DEV_LOGIN, DM_ENV=dev only).
const ProviderDev = "dev"

// DevLoginEmail is the account the local sign-in uses when no e-mail is
// given (.test is reserved by RFC 2606 and never resolves).
const DevLoginEmail = "dev@docmatch.test"

// Platforms, from the User-Agent the apps send (docmatch-android/<v>,
// docmatch-ios/<v>); anything else is a browser.
const (
	PlatformWeb     = "web"
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
)

// PlatformOf tells which client made the request.
func PlatformOf(r *http.Request) string {
	ua := r.UserAgent()
	switch {
	case strings.HasPrefix(ua, "docmatch-android/"):
		return PlatformAndroid
	case strings.HasPrefix(ua, "docmatch-ios/"):
		return PlatformIOS
	}
	return PlatformWeb
}

// methodsView is the AuthMethods schema: which sign-ins this deployment
// offers, so the web and the apps only show the buttons that work.
type methodsView struct {
	Email    bool `json:"email"`
	Google   bool `json:"google"`
	Apple    bool `json:"apple"`
	Password bool `json:"password"`
	Dev      bool `json:"dev"`
}

// methods handles GET /v1/auth/methods.
func (s *Service) methods(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, methodsView{
		Email: s.d.EmailEnabled, Google: s.d.Google != nil, Apple: s.d.Apple != nil, Password: true, Dev: s.d.DevLogin,
	})
}

type devLoginRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// devLogin handles POST /v1/auth/dev/login: signs in (creating the account
// on first use) without any provider, then tops the organization up with the
// local credit grant. Mounted only with DevLogin, which the configuration
// refuses outside DM_ENV=dev.
func (s *Service) devLogin(w http.ResponseWriter, r *http.Request) {
	var req devLoginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	email := DevLoginEmail
	if req.Email != "" {
		v, ok := s.normalizeEmail(req.Email)
		if !ok {
			apierr.Validation(w, r, apierr.Field{Path: "/email", Code: apierr.FieldInvalid})
			return
		}
		email = v
	}
	id := Identity{Provider: ProviderDev, Subject: email, Email: email, EmailVerified: true, Name: strings.TrimSpace(req.Name)}
	out, err := s.completeLogin(r.Context(), w, r, id, nil, map[string]any{"platform": PlatformOf(r)})
	if err != nil {
		s.logRefused(r, id, err)
		apierr.Write(w, r, codeFor(err))
		return
	}
	if s.d.DevGrant != nil {
		if err := s.d.DevGrant(r.Context(), out.Tenant.Org.ID); err != nil {
			apierr.Internal(w, r, err)
			return
		}
	}
	s.writeSession(w, out)
}

type googleNativeRequest struct {
	IDToken string `json:"id_token"`
}

// googleNative handles POST /v1/auth/google/native: the ID token the Android
// or iOS app got from Google's own sign-in. The audience is the server client
// id (Android's serverClientId) or one of GoogleAudiences (the apps' client
// ids); the account is resolved exactly like the web flow (Google subject,
// then verified e-mail), so the same person lands on the same account on
// every device.
func (s *Service) googleNative(w http.ResponseWriter, r *http.Request) {
	p := s.d.Google
	if p == nil {
		apierr.Write(w, r, contracts.ErrServiceUnavailable)
		return
	}
	if !s.allow(w, r, limitKey(s.d.ClientIP(r), "")) {
		return
	}
	var req googleNativeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	if req.IDToken == "" {
		apierr.Validation(w, r, apierr.Field{Path: "/id_token", Code: apierr.FieldRequired})
		return
	}
	id, err := s.verifyAudiences(r.Context(), p, req.IDToken)
	if err != nil {
		apierr.Write(w, r, codeFor(err))
		return
	}
	out, err := s.completeLogin(r.Context(), w, r, id, nil, map[string]any{"platform": PlatformOf(r)})
	if err != nil {
		s.logRefused(r, id, err)
		apierr.Write(w, r, codeFor(err))
		return
	}
	s.writeSession(w, out)
}

// verifyAudiences accepts an ID token issued to the provider's client id or
// to one of GoogleAudiences. No nonce: like the native Apple flow, the token
// comes straight from the provider to the app, bound by its audience.
func (s *Service) verifyAudiences(ctx context.Context, p *Provider, token string) (Identity, error) {
	audiences := append([]string{p.ClientID}, s.d.GoogleAudiences...)
	var last error
	for _, aud := range audiences {
		q := *p
		q.ClientID = aud
		id, err := q.Verify(ctx, token, "", s.d.Now())
		if err == nil {
			return id, nil
		}
		last = err
		if !errors.Is(err, crypto.ErrJWTAudience) {
			return Identity{}, err
		}
	}
	return Identity{}, last
}

// DevGrantFunc tops up the organization the local sign-in lands on.
type DevGrantFunc func(ctx context.Context, orgID uuid.UUID) error
