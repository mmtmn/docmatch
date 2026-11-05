package estimate

import (
	"net/http"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// Handler serves POST /v1/runs/estimate.
type Handler struct {
	svc  *Service
	auth apix.Resolver
	uri  func(u uploads.Upload) string
}

// NewHandler binds the service to the auth resolver; uri renders the
// storage URI of an upload for the harness.
func NewHandler(svc *Service, auth apix.Resolver, uri func(u uploads.Upload) string) *Handler {
	if auth == nil {
		auth = apix.Unauthenticated()
	}
	return &Handler{svc: svc, auth: auth, uri: uri}
}

// Mount registers the route on r.
func (h *Handler) Mount(r *httpx.Router) {
	r.HandleFunc(http.MethodPost, "/v1/runs/estimate", httpx.BodyJSON, h.estimate)
}

func (h *Handler) estimate(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	var req Request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apix.Handle(w, r, err)
		return
	}
	q, err := h.svc.Estimate(r.Context(), p, req, h.uri)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, q)
}
