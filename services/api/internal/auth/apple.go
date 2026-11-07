package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
)

const maxAppleRefreshToken = 16384

// A rejected login may already have consumed its authorization code. Keep the
// newly issued grant in the anonymous retry queue, especially during deletion.
func (s *Service) completeAppleLogin(ctx context.Context, w http.ResponseWriter, r *http.Request, id Identity, known *account.User, meta map[string]any) (loginResult, error) {
	out, err := s.completeLogin(ctx, w, r, id, known, meta)
	if err != nil && id.appleCredential != nil {
		grant := id.appleCredential
		if queueErr := s.d.AppleTokens.QueueRevocation(ctx, grant.clientID, grant.refreshToken); queueErr != nil {
			s.d.Logger.Error("could not queue rejected Apple grant revocation")
		}
	}
	return out, err
}

// appleForClient binds both the ID-token audience and the signed secret to one
// client. Callers must validate native audiences before choosing this client.
func (p *Provider) appleForClient(clientID string) (*Provider, error) {
	if p.Name != ProviderApple || clientID == "" {
		return nil, ErrProvider
	}
	q := *p
	q.ClientID = clientID
	if clientID != p.ClientID {
		if p.appleClientSecret == nil {
			return nil, ErrProviderUnavailable
		}
		q.ClientSecret = func(now time.Time) (string, error) { return p.appleClientSecret(clientID, now) }
	}
	return &q, nil
}

func (s *Service) exchangeAppleNative(ctx context.Context, p *Provider, token, code string) (Identity, error) {
	for _, audience := range append([]string{p.ClientID}, s.d.AppleAudiences...) {
		candidate := *p
		candidate.ClientID = audience
		identity, err := candidate.Verify(ctx, token, "", s.d.Now())
		if errors.Is(err, crypto.ErrJWTAudience) {
			continue
		}
		if err != nil {
			return Identity{}, err
		}
		client, err := p.appleForClient(audience)
		if err != nil {
			return Identity{}, err
		}
		tokens, err := client.exchangeTokens(ctx, code, "", "", s.d.Now())
		if err != nil {
			return Identity{}, err
		}
		confirmed, err := client.Verify(ctx, tokens.IDToken, "", s.d.Now())
		if err != nil {
			return Identity{}, err
		}
		if confirmed.Subject != identity.Subject || tokens.RefreshToken == "" || len(tokens.RefreshToken) > maxAppleRefreshToken {
			return Identity{}, ErrProvider
		}
		confirmed.appleCredential = &appleCredential{audience, tokens.RefreshToken}
		return confirmed, nil
	}
	return Identity{}, crypto.ErrJWTAudience
}

// RevokeApple revokes a previously verified client's refresh token. The worker
// retries failures; neither provider response bodies nor tokens enter errors.
func (p *Provider) RevokeApple(ctx context.Context, clientID, token string) error {
	client, err := p.appleForClient(clientID)
	if err != nil {
		return err
	}
	if client.RevokeURL == "" {
		return ErrProviderUnavailable
	}
	secret, err := client.ClientSecret(time.Now())
	if err != nil {
		return ErrProvider
	}
	form := url.Values{"client_id": {clientID}, "client_secret": {secret}, "token": {token}, "token_type_hint": {"refresh_token"}}
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrProvider
	}
	req.Header.Set("Content-Type", formContentType)
	httpClient := *client.Client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := httpClient.Do(req)
	if err != nil {
		return ErrProvider
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxTokenResponse))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: revocation status %d", ErrProvider, resp.StatusCode)
	}
	return nil
}
