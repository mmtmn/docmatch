package extensions

import (
	"net/http"
	"strings"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

func bearer(r *http.Request) (string, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = parts[1]
	}
	return returnValue, strings.HasPrefix(returnValue, accessPrefix)
}

// OriginExempt applies only to explicit credentials with no ambient cookies.
// Ordinary web requests keep the existing CSRF and CORS policy unchanged.
func OriginExempt(r *http.Request) bool {
	if r.Header.Get("Cookie") != "" {
		return false
	}
	_, ok := bearer(r)
	return ok || r.Method == http.MethodPost && r.URL.Path == "/v1/extensions/token"
}

func allowed(r *http.Request) bool {
	path := r.URL.Path
	if path == "/v1/extensions/revoke" {
		return r.Method == http.MethodPost
	}
	if path == "/v1/me/organizations" || path == "/v1/me" {
		return r.Method == http.MethodGet
	}
	if path == "/v1/projects" || strings.HasPrefix(path, "/v1/projects/") {
		return true
	}
	if r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/uploads/") {
		_, err := publicid.Decode("upl", strings.TrimPrefix(path, "/v1/uploads/"))
		return err == nil
	}
	return false
}

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearer(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Cookie") != "" || !allowed(r) {
			apix.Handle(w, r, apix.ErrForbidden)
			return
		}
		p, err := s.Authenticate(r.Context(), raw)
		if err != nil {
			apix.Handle(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(session.ContextWithPrincipal(r.Context(), p)))
	})
}

// This middleware follows tenancy: the selected account has been authenticated
// before the rollout check. Existing reads and revocation remain available.
func (s *Service) RolloutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, extension := bearer(r)
		if extension && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodDelete && r.URL.Path != "/v1/extensions/revoke" && !strings.HasSuffix(r.URL.Path, "/cancel") {
			p, ok := session.FromContext(r.Context())
			if !ok {
				apix.Handle(w, r, apix.ErrUnauthenticated)
				return
			}
			if !s.rollout.Allows(p.OrgID) {
				apix.Handle(w, r, apix.E(contracts.ErrFeaturePaused, nil))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Resolve bypasses the API-key resolver only after extension middleware has
// authenticated the request. It retains app pricing and all live project checks.
func (s *Service) Resolve(cookie, other apix.Resolver) apix.Resolver {
	return func(r *http.Request) (apix.Principal, error) {
		raw, ok := bearer(r)
		if !ok {
			return other(r)
		}
		if !allowed(r) {
			return apix.Principal{}, apix.ErrForbidden
		}
		// SSE rechecks also validate expiry, configuration, rotation and revocation.
		if _, err := s.Authenticate(r.Context(), raw); err != nil {
			return apix.Principal{}, err
		}
		return cookie(r)
	}
}

func (s *Service) Mount(r *httpx.Router) {
	r.HandleFunc(http.MethodGet, "/v1/extensions/clients/{client}", httpx.BodyNone, s.clientInfo)
	r.Handle(http.MethodPost, "/v1/extensions/authorize", httpx.BodyJSON, session.RequireFunc(s.authorize))
	r.Handle(http.MethodPost, "/v1/extensions/token", httpx.BodyJSON, s.limit(http.HandlerFunc(s.token)))
	r.Handle(http.MethodPost, "/v1/extensions/revoke", httpx.BodyNone, session.RequireFunc(s.revoke))
}
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
func (s *Service) clientInfo(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	c, err := s.Client(r.PathValue("client"), r.URL.Query().Get("redirect_uri"))
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, map[string]string{"id": c.ID, "name": c.Name})
}
func (s *Service) authorize(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	// Only the existing signed-in web page may consent, never another extension.
	if !session.HasSessionCookie(r) || r.Header.Get("Authorization") != "" {
		apix.Handle(w, r, apix.ErrForbidden)
		return
	}
	var in AuthorizeRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		apix.Handle(w, r, err)
		return
	}
	p, _ := session.FromContext(r.Context())
	target, err := s.Authorize(r.Context(), p, in)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, map[string]string{"redirect_uri": target})
}
func (s *Service) token(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
		apix.Handle(w, r, apix.ErrForbidden)
		return
	}
	var in TokenRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		apix.Handle(w, r, err)
		return
	}
	var out Tokens
	var err error
	switch in.GrantType {
	case "authorization_code":
		out, err = s.Exchange(r.Context(), in)
	case "refresh_token":
		out, err = s.Refresh(r.Context(), in)
	default:
		err = validation()
	}
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, out)
}
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if _, ok := bearer(r); !ok {
		apix.Handle(w, r, apix.ErrForbidden)
		return
	}
	p, _ := session.FromContext(r.Context())
	_, err := s.pool.Exec(r.Context(), `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND user_id=$2`, p.SessionID, p.UserID)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
