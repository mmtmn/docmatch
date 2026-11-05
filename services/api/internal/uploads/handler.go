package uploads

import (
	"net/http"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Handler serves the uploads routes.
type Handler struct {
	svc  *Service
	auth apix.Resolver
}

// NewHandler binds the service to the auth resolver.
func NewHandler(svc *Service, auth apix.Resolver) *Handler {
	if auth == nil {
		auth = apix.Unauthenticated()
	}
	return &Handler{svc: svc, auth: auth}
}

// Mount registers the routes on r.
func (h *Handler) Mount(r *httpx.Router) {
	r.HandleFunc(http.MethodPost, "/v1/uploads", httpx.BodyJSON, h.create)
	r.HandleFunc(http.MethodPost, "/v1/uploads/{id}/complete", httpx.BodyJSON, h.complete)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	var req TicketRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apix.Handle(w, r, err)
		return
	}
	ticket, _, err := h.svc.CreateTicket(r.Context(), p, req)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusCreated, ticket)
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, err := apix.Decode(apix.PrefixUpload, r.PathValue("id"))
	if err != nil {
		apix.Write(w, r, contracts.ErrUploadNotFound, nil)
		return
	}
	var req CompleteRequest
	// The body is optional in the contract; decode it only when present.
	if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			apix.Handle(w, r, err)
			return
		}
	}
	u, done, err := h.svc.Complete(r.Context(), p, id, req)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	status := http.StatusAccepted
	if done {
		status = http.StatusOK
	}
	apix.WriteJSON(w, status, u.View(h.svc.d.Now()))
}
