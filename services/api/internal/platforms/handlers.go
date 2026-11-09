package platforms

import (
	"errors"
	"net/http"
	"time"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

func (s *Service) Mount(r *httpx.Router) {
	s.mountIntegrationInterests(r)
	r.HandleFunc(http.MethodGet, "/v1/platforms", httpx.BodyNone, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		apix.WriteJSON(w, http.StatusOK, map[string]any{"items": s.catalog})
	})
	// Public forms still use the API's normal Origin/CSRF/body policies.
	limit := ratelimit.Middleware(ratelimit.NewMemory(), func(r *http.Request) (string, ratelimit.Policy, bool) {
		return string(crypto.MAC(s.privacyKey, []byte(s.clientIP(r)))), ratelimit.Policy{Limit: 10, Period: time.Minute}, true
	})
	r.Handle(http.MethodPost, "/v1/platform-notices", httpx.BodyJSON, limit(http.HandlerFunc(s.subscribe)))
	r.Handle(http.MethodPost, "/v1/platform-notices/confirm", httpx.BodyJSON, limit(http.HandlerFunc(s.confirm)))
	r.Handle(http.MethodPost, "/v1/platform-notices/unsubscribe", httpx.BodyJSON, limit(http.HandlerFunc(s.unsubscribe)))
}
func (s *Service) subscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Platform string `json:"platform"`
		Email    string `json:"email"`
		Consent  bool   `json:"consent"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		apix.Handle(w, r, err)
		return
	}
	if err := s.Subscribe(r.Context(), in.Platform, in.Email, s.clientIP(r), in.Consent); err != nil {
		var apiErr *apix.Error
		if errors.As(err, &apiErr) && apiErr.Code == contracts.ErrRateLimited {
			w.Header().Set("Retry-After", "86400")
		}
		apix.Handle(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	apix.WriteJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}
func tokenInput(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		apix.Handle(w, r, err)
		return "", false
	}
	return in.Token, true
}
func (s *Service) confirm(w http.ResponseWriter, r *http.Request) {
	token, ok := tokenInput(w, r)
	if !ok {
		return
	}
	platform, err := s.Confirm(r.Context(), token)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	apix.WriteJSON(w, http.StatusOK, map[string]string{"platform": platform, "status": "confirmed"})
}
func (s *Service) unsubscribe(w http.ResponseWriter, r *http.Request) {
	token, ok := tokenInput(w, r)
	if !ok {
		return
	}
	if err := s.Unsubscribe(r.Context(), token); err != nil {
		apix.Handle(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) clientIP(r *http.Request) string { return s.edge.ClientIP(r).String() }
