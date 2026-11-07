package auth

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
)

// Provider endpoints. These are the public, documented endpoints of the two
// identity providers (allowed by tools/lint/hardcode-allow.txt).
const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // endpoint, not a credential
	googleJWKSURL  = "https://www.googleapis.com/oauth2/v3/certs"
	googleIssuer   = "https://accounts.google.com"
	googleIssuer2  = "accounts.google.com"
	appleAuthURL   = "https://appleid.apple.com/auth/authorize"
	appleTokenURL  = "https://appleid.apple.com/auth/token" //nolint:gosec // endpoint, not a credential
	appleRevokeURL = "https://appleid.apple.com/auth/revoke"
	appleJWKSURL   = "https://appleid.apple.com/auth/keys"
	appleIssuer    = "https://appleid.apple.com"
	// appleSecretTTL bounds the client secret JWT (Apple allows up to six
	// months; a short one limits the damage of a leak).
	appleSecretTTL   = 5 * time.Minute
	providerTimeout  = 10 * time.Second
	maxTokenResponse = 1 << 20
)

// Provider is an OpenID Connect provider configured for the authorization
// code flow. Tests point the URLs at a local fake.
type Provider struct {
	Name     string
	ClientID string
	// ClientSecret returns the secret for the token exchange (static for
	// Google; a freshly signed ES256 JWT for Apple).
	ClientSecret      func(now time.Time) (string, error)
	appleClientSecret func(clientID string, now time.Time) (string, error)
	RevokeURL         string
	AuthURL           string
	TokenURL          string
	Issuers           []string
	Keys              crypto.KeyFunc
	Scopes            []string
	// ResponseMode is "form_post" for Apple (required when scopes are asked).
	ResponseMode string
	// PKCE adds code_challenge/code_verifier (Google; Apple ignores PKCE).
	PKCE   bool
	Client *http.Client
}

// NewGoogle configures Google Sign-In; client may be nil.
func NewGoogle(clientID, clientSecret string, client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: providerTimeout}
	}
	return &Provider{
		Name: ProviderGoogle, ClientID: clientID,
		ClientSecret: func(time.Time) (string, error) { return clientSecret, nil },
		AuthURL:      googleAuthURL, TokenURL: googleTokenURL, Issuers: []string{googleIssuer, googleIssuer2},
		Keys: crypto.NewJWKS(googleJWKSURL, client).Key, Scopes: []string{"openid", "email", "profile"}, PKCE: true, Client: client,
	}
}

// NewApple configures Sign in with Apple; the private key is the .p8 PEM
// (with literal \n allowed). client may be nil.
func NewApple(teamID, clientID, keyID, privateKeyPEM string, client *http.Client) (*Provider, error) {
	if teamID == "" || clientID == "" || keyID == "" || privateKeyPEM == "" {
		return nil, ErrProviderUnavailable
	}
	key, err := crypto.ParseECPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("auth: APPLE_PRIVATE_KEY: %w", err)
	}
	if client == nil {
		client = &http.Client{Timeout: providerTimeout}
	}
	return &Provider{
		Name: ProviderApple, ClientID: clientID,
		ClientSecret: appleSecret(key, teamID, clientID, keyID, appleIssuer),
		appleClientSecret: func(id string, now time.Time) (string, error) {
			return appleSecret(key, teamID, id, keyID, appleIssuer)(now)
		},
		RevokeURL: appleRevokeURL,
		AuthURL:   appleAuthURL, TokenURL: appleTokenURL, Issuers: []string{appleIssuer},
		Keys: crypto.NewJWKS(appleJWKSURL, client).Key, Scopes: []string{"name", "email"}, ResponseMode: "form_post", Client: client,
	}, nil
}

// appleSecret returns a builder of the client secret JWT.
func appleSecret(key *ecdsa.PrivateKey, teamID, clientID, keyID, audience string) func(time.Time) (string, error) {
	return func(now time.Time) (string, error) {
		return crypto.SignES256(key, keyID, map[string]any{
			"iss": teamID, "iat": now.Unix(), "exp": now.Add(appleSecretTTL).Unix(), "aud": audience, "sub": clientID,
		})
	}
}

// AuthorizationURL builds the redirect to the provider.
func (p *Provider) AuthorizationURL(redirectURI, state, nonce, codeVerifier string) string {
	q := url.Values{}
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(p.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	if p.ResponseMode != "" {
		q.Set("response_mode", p.ResponseMode)
	}
	if p.PKCE {
		q.Set("code_challenge", pkceChallenge(codeVerifier))
		q.Set("code_challenge_method", "S256")
	}
	if p.Name == ProviderGoogle {
		q.Set("access_type", "online")
		q.Set("prompt", "select_account")
	}
	return p.AuthURL + "?" + q.Encode()
}

// pkceChallenge is base64url(SHA-256(verifier)) without padding (S256).
func pkceChallenge(verifier string) string {
	return base64URL(crypto.Digest(verifier))
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

// Exchange trades the authorization code for the ID token.
func (p *Provider) Exchange(ctx context.Context, code, codeVerifier, redirectURI string, now time.Time) (string, error) {
	tr, err := p.exchangeTokens(ctx, code, codeVerifier, redirectURI, now)
	return tr.IDToken, err
}

func (p *Provider) exchangeTokens(ctx context.Context, code, codeVerifier, redirectURI string, now time.Time) (tokenResponse, error) {
	secret, err := p.ClientSecret(now)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: client secret: %w", ErrProvider, err)
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", secret)
	if p.PKCE {
		form.Set("code_verifier", codeVerifier)
	}
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	req.Header.Set("Content-Type", formContentType)
	req.Header.Set("Accept", acceptJSON)
	httpClient := *p.Client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponse))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil || resp.StatusCode != http.StatusOK || tr.IDToken == "" {
		return tokenResponse{}, fmt.Errorf("%w: token endpoint status %d", ErrProvider, resp.StatusCode)
	}
	return tr, nil
}

// Verify checks the ID token (signature against the provider keys, issuer,
// audience, expiry and nonce) and extracts the identity.
func (p *Provider) Verify(ctx context.Context, idToken, nonce string, now time.Time) (Identity, error) {
	claims, err := crypto.VerifyIDToken(ctx, idToken, p.Keys, crypto.IDTokenExpectations{
		Issuers: p.Issuers, Audience: p.ClientID, Nonce: nonce, Now: now,
	})
	if err != nil {
		return Identity{}, err
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = strings.TrimSpace(strings.TrimSpace(claims.GivenName) + " " + strings.TrimSpace(claims.FamilyName))
	}
	return Identity{
		Provider: p.Name, Subject: claims.Subject, Email: strings.ToLower(strings.TrimSpace(claims.Email)),
		EmailVerified: bool(claims.EmailVerified), Name: name, PrivateEmail: bool(claims.IsPrivateEmail),
	}, nil
}
