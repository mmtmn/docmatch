package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

// redirectToRe mirrors AuthStartRequest.redirect_to: a site-relative path.
var redirectToRe = regexp.MustCompile(`^/[^/\\].*$|^/$`)

// oauthState is sealed into the dm_oauth_<provider> cookie.
type oauthState struct {
	Provider   string `json:"p"`
	State      string `json:"s"`
	Nonce      string `json:"n"`
	Verifier   string `json:"v"`
	RedirectTo string `json:"r"`
	Platform   string `json:"pl"`
	Expires    int64  `json:"exp"`
}

type startRequest struct {
	RedirectTo string `json:"redirect_to"`
	Platform   string `json:"platform"`
}

type startResponse struct {
	AuthorizationURL string    `json:"authorization_url"`
	State            string    `json:"state"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *Service) provider(name string) *Provider {
	switch name {
	case ProviderGoogle:
		return s.d.Google
	case ProviderApple:
		return s.d.Apple
	}
	return nil
}

func (s *Service) callbackURL(name string) string {
	base := strings.TrimRight(s.d.APIBaseURL.String(), "/")
	if name == ProviderApple {
		return base + AppleCallbackPath
	}
	return base + GoogleCallbackPath
}

// start handles POST /auth/<provider>/start.
func (s *Service) start(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.provider(name)
		if p == nil {
			apierr.Write(w, r, contracts.ErrServiceUnavailable)
			return
		}
		if !s.allow(w, r, limitKey(s.d.ClientIP(r), "")) {
			return
		}
		var req startRequest
		if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
			if err := httpx.DecodeJSON(r, &req); err != nil {
				apierr.FromRequestError(w, r, err)
				return
			}
		}
		if req.RedirectTo != "" && (len(req.RedirectTo) > maxRedirectTo || !redirectToRe.MatchString(req.RedirectTo) || strings.ContainsAny(req.RedirectTo, "\r\n\t ")) {
			apierr.Validation(w, r, apierr.Field{Path: "/redirect_to", Code: apierr.FieldInvalid})
			return
		}
		switch req.Platform {
		case "", "web", "android", "ios":
		default:
			apierr.Validation(w, r, apierr.Field{Path: "/platform", Code: apierr.FieldInvalid})
			return
		}
		now := s.d.Now()
		st := oauthState{
			Provider: name, State: crypto.RandomToken(32), Nonce: crypto.RandomToken(32), Verifier: crypto.RandomToken(32),
			RedirectTo: req.RedirectTo, Platform: req.Platform, Expires: now.Add(OAuthStateTTL).Unix(),
		}
		if err := s.setStateCookie(w, st); err != nil {
			apierr.Internal(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, startResponse{
			AuthorizationURL: p.AuthorizationURL(s.callbackURL(name), st.State, st.Nonce, st.Verifier),
			State:            st.State,
			ExpiresAt:        time.Unix(st.Expires, 0).UTC(),
		})
	}
}

func cookieName(provider string) string { return oauthCookiePrefix + provider }

// setStateCookie seals the state into a short-lived cookie scoped to the
// callback path. Apple posts the callback cross-site, so its cookie needs
// SameSite=None (which browsers only honour with Secure).
func (s *Service) setStateCookie(w http.ResponseWriter, st oauthState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	sealed, err := crypto.Seal(s.d.Keys.Key(crypto.PurposeCookie), raw, []byte(st.Provider))
	if err != nil {
		return err
	}
	c := &http.Cookie{
		Name: cookieName(st.Provider), Value: base64URL(sealed), Path: callbackPath(st.Provider),
		MaxAge: int(OAuthStateTTL / time.Second), HttpOnly: true, Secure: s.d.Secure, SameSite: http.SameSiteLaxMode,
	}
	if st.Provider == ProviderApple && s.d.Secure {
		c.SameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, c)
	return nil
}

func callbackPath(provider string) string {
	if provider == ProviderApple {
		return AppleCallbackPath
	}
	return GoogleCallbackPath
}

// clearStateCookie expires the cookie so a state can never be replayed.
func (s *Service) clearStateCookie(w http.ResponseWriter, provider string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName(provider), Value: "", Path: callbackPath(provider), MaxAge: -1, HttpOnly: true, Secure: s.d.Secure, SameSite: http.SameSiteLaxMode})
}

// openState reads and verifies the cookie against the state parameter.
func (s *Service) openState(r *http.Request, provider, state string) (oauthState, error) {
	c, err := r.Cookie(cookieName(provider))
	if err != nil || c.Value == "" || state == "" || len(state) > 128 || len(c.Value) > 4096 {
		return oauthState{}, ErrStateMismatch
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return oauthState{}, ErrStateMismatch
	}
	raw, err := crypto.Open(s.d.Keys.Key(crypto.PurposeCookie), sealed, []byte(provider))
	if err != nil {
		return oauthState{}, ErrStateMismatch
	}
	var st oauthState
	if err := json.Unmarshal(raw, &st); err != nil || st.Provider != provider {
		return oauthState{}, ErrStateMismatch
	}
	if !crypto.EqualString(st.State, state) || s.d.Now().Unix() >= st.Expires {
		return oauthState{}, ErrStateMismatch
	}
	return st, nil
}

// wantsJSON reports whether the client asked for the session as JSON
// instead of the browser redirect.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), acceptJSON)
}

// finishOAuth answers an OAuth callback: JSON for API clients, a redirect
// into the web app otherwise.
func (s *Service) finishOAuth(w http.ResponseWriter, r *http.Request, st oauthState, out loginResult, err error) {
	if err != nil {
		code := codeFor(err)
		if wantsJSON(r) {
			apierr.Write(w, r, code)
			return
		}
		http.Redirect(w, r, s.webURL(s.d.LoginPath)+"?error="+string(code), http.StatusSeeOther)
		return
	}
	if wantsJSON(r) {
		s.writeSession(w, out)
		return
	}
	target := st.RedirectTo
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, s.webURL(target), http.StatusSeeOther)
}

func (s *Service) webURL(path string) string {
	return strings.TrimRight(s.d.PublicBaseURL.String(), "/") + path
}

// codeFor maps login errors onto catalog codes.
func codeFor(err error) contracts.ErrorCode {
	switch {
	case errors.Is(err, ErrStateMismatch):
		return contracts.ErrOauthStateMismatch
	case errors.Is(err, ErrProvider):
		return contracts.ErrOauthProviderError
	case errors.Is(err, ErrEmailUnverified):
		return contracts.ErrOauthEmailUnverified
	case errors.Is(err, ErrSuspended):
		return contracts.ErrAccountSuspended
	case errors.Is(err, ErrDeletionPending):
		return contracts.ErrAccountDeletionPending
	case errors.Is(err, ErrProviderUnavailable), errors.Is(err, ErrEmailUnavailable):
		return contracts.ErrServiceUnavailable
	case errors.Is(err, ErrChallengeNotFound):
		return contracts.ErrEmailChallengeNotFound
	case errors.Is(err, ErrCodeInvalid):
		return contracts.ErrEmailCodeInvalid
	case errors.Is(err, ErrCodeExpired):
		return contracts.ErrEmailCodeExpired
	case errors.Is(err, ErrTooManyAttempts):
		return contracts.ErrTooManyAttempts
	case errors.Is(err, ErrInvalidCredentials):
		return contracts.ErrInvalidCredentials
	case errors.Is(err, crypto.ErrJWTMalformed), errors.Is(err, crypto.ErrJWTSignature), errors.Is(err, crypto.ErrJWTAlgorithm),
		errors.Is(err, crypto.ErrJWTExpired), errors.Is(err, crypto.ErrJWTNotYetValid), errors.Is(err, crypto.ErrJWTIssuer),
		errors.Is(err, crypto.ErrJWTAudience), errors.Is(err, crypto.ErrJWTNonce), errors.Is(err, crypto.ErrJWTNoKey):
		return contracts.ErrOauthStateMismatch
	}
	return contracts.ErrInternalError
}

// googleCallback handles GET /auth/google/callback.
func (s *Service) googleCallback(w http.ResponseWriter, r *http.Request) {
	p := s.d.Google
	if p == nil {
		apierr.Write(w, r, contracts.ErrServiceUnavailable)
		return
	}
	q := r.URL.Query()
	st, err := s.openState(r, ProviderGoogle, q.Get("state"))
	s.clearStateCookie(w, ProviderGoogle)
	if err != nil {
		s.finishOAuth(w, r, st, loginResult{}, err)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		s.finishOAuth(w, r, st, loginResult{}, ErrProvider)
		return
	}
	now := s.d.Now()
	idToken, err := p.Exchange(r.Context(), q.Get("code"), st.Verifier, s.callbackURL(ProviderGoogle), now)
	if err != nil {
		s.finishOAuth(w, r, st, loginResult{}, err)
		return
	}
	id, err := p.Verify(r.Context(), idToken, st.Nonce, now)
	if err != nil {
		s.finishOAuth(w, r, st, loginResult{}, err)
		return
	}
	out, err := s.completeLogin(r.Context(), w, r, id, nil, map[string]any{"platform": st.Platform})
	if err != nil {
		s.logRefused(r, id, err)
	}
	s.finishOAuth(w, r, st, out, err)
}

// appleCallback handles POST /auth/apple/callback: the form_post of the web
// flow or the JSON of the native flow.
func (s *Service) appleCallback(w http.ResponseWriter, r *http.Request) {
	p := s.d.Apple
	if p == nil {
		apierr.Write(w, r, contracts.ErrServiceUnavailable)
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), acceptJSON) {
		s.appleNative(w, r, p)
		return
	}
	if err := r.ParseForm(); err != nil {
		apierr.Validation(w, r, apierr.Field{Path: "/", Code: apierr.FieldInvalid})
		return
	}
	st, err := s.openState(r, ProviderApple, r.PostFormValue("state"))
	s.clearStateCookie(w, ProviderApple)
	if err != nil {
		s.finishOAuth(w, r, st, loginResult{}, err)
		return
	}
	code := r.PostFormValue("code")
	if r.PostFormValue("error") != "" || code == "" {
		s.finishOAuth(w, r, st, loginResult{}, ErrProvider)
		return
	}
	now := s.d.Now()
	// The code exchange proves the authorization belongs to this client; the
	// ID token it returns is the one verified (the posted id_token is not trusted).
	idToken, err := p.Exchange(r.Context(), code, st.Verifier, s.callbackURL(ProviderApple), now)
	if err != nil {
		s.finishOAuth(w, r, st, loginResult{}, err)
		return
	}
	id, err := p.Verify(r.Context(), idToken, st.Nonce, now)
	if err != nil {
		s.finishOAuth(w, r, st, loginResult{}, err)
		return
	}
	if id.Name == "" {
		id.Name = appleUserName(r.PostFormValue("user"))
	}
	out, err := s.completeLogin(r.Context(), w, r, id, nil, map[string]any{"platform": st.Platform})
	if err != nil {
		s.logRefused(r, id, err)
	}
	s.finishOAuth(w, r, st, out, err)
}

type appleNativeRequest struct {
	IdentityToken     string `json:"identity_token"`
	AuthorizationCode string `json:"authorization_code"`
	FullName          string `json:"full_name"`
}

// appleNative verifies the identity token obtained by the iOS app. There is
// no browser-bound state here: the token comes straight from Apple to the
// app, and the audience check binds it to our client id.
func (s *Service) appleNative(w http.ResponseWriter, r *http.Request, p *Provider) {
	if !s.allow(w, r, limitKey(s.d.ClientIP(r), "")) {
		return
	}
	var req appleNativeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	if req.IdentityToken == "" || req.AuthorizationCode == "" {
		apierr.Validation(w, r, apierr.Field{Path: "/identity_token", Code: apierr.FieldRequired})
		return
	}
	id, err := p.Verify(r.Context(), req.IdentityToken, "", s.d.Now())
	if err != nil {
		apierr.Write(w, r, codeFor(err))
		return
	}
	if id.Name == "" {
		if name, lerr := httpx.ValidateText(req.FullName, s.d.Limits.Text.DisplayName, true); lerr == nil {
			id.Name = name
		}
	}
	out, err := s.completeLogin(r.Context(), w, r, id, nil, map[string]any{"platform": "ios"})
	if err != nil {
		s.logRefused(r, id, err)
		apierr.Write(w, r, codeFor(err))
		return
	}
	s.writeSession(w, out)
}

// appleUserName extracts the name from the `user` JSON Apple posts on the
// first login only.
func appleUserName(raw string) string {
	if raw == "" || len(raw) > maxAppleUserJSON {
		return ""
	}
	var u struct {
		Name struct {
			First string `json:"firstName"`
			Last  string `json:"lastName"`
		} `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return ""
	}
	name, err := httpx.ValidateText(strings.TrimSpace(u.Name.First+" "+u.Name.Last), identityNameLimit, true)
	if err != nil {
		return ""
	}
	return name
}
