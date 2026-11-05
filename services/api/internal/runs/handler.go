package runs

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/sse"
)

// Handler serves the runs routes.
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
	r.HandleFunc(http.MethodGet, "/v1/runs", httpx.BodyNone, h.list)
	r.HandleFunc(http.MethodPost, "/v1/runs", httpx.BodyJSON, h.create)
	r.HandleFunc(http.MethodGet, "/v1/runs/{id}", httpx.BodyNone, h.get)
	r.HandleFunc(http.MethodPost, "/v1/runs/{id}/cancel", httpx.BodyNone, h.cancel)
	r.HandleFunc(http.MethodGet, "/v1/runs/{id}/events", httpx.BodyNone, h.events)
	r.HandleFunc(http.MethodGet, "/v1/runs/{id}/findings", httpx.BodyNone, h.findings)
	r.HandleFunc(http.MethodPost, "/v1/runs/{id}/findings/{fid}/feedback", httpx.BodyJSON, h.feedback)
	r.HandleFunc(http.MethodGet, "/v1/runs/{id}/export", httpx.BodyNone, h.export)
	r.HandleFunc(http.MethodPost, "/v1/runs/{id}/approve-quote", httpx.BodyJSON, h.approve)
}

// ListResponse is openapi RunList.
type ListResponse struct {
	Items      []View  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// FindingListResponse is openapi FindingList.
type FindingListResponse struct {
	Items      []FindingView `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

func (h *Handler) runID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := apix.Decode(apix.PrefixRun, r.PathValue("id"))
	if err != nil {
		apix.Write(w, r, contracts.ErrRunNotFound, nil)
		return uuid.Nil, false
	}
	return id, true
}

func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, httpx.NewRequestError(http.StatusBadRequest, httpx.CodeInvalidJSON, err)
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return body, nil
}

// idempotent wraps a credit-spending handler with the Idempotency-Key protocol.
func (h *Handler) idempotent(w http.ResponseWriter, r *http.Request, p apix.Principal, body []byte, fn func() (apix.Result, error)) {
	key, aerr := apix.IdemKey(r)
	if aerr != nil {
		apix.Handle(w, r, aerr)
		return
	}
	res, err := apix.Idempotent(r.Context(), h.svc.Idem(), p.OrgID, key, apix.IdemHash(r, body), h.svc.Now(), fn)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	for k, v := range res.Headers {
		w.Header().Set(k, v)
	}
	apix.WriteJSON(w, res.Status, res.Body)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	page, aerr := apix.ParsePage(r)
	if aerr != nil {
		apix.Handle(w, r, aerr)
		return
	}
	var f ListFilter
	q := r.URL.Query()
	if st := q.Get("status"); st != "" {
		f.Status = Status(st)
		if !f.Status.Valid() {
			apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/status", Code: "invalid"}))
			return
		}
	}
	if wf := q.Get("workflow_id"); wf != "" {
		id, err := apix.Decode(apix.PrefixWorkflow, wf)
		if err != nil {
			apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/workflow_id", Code: "invalid"}))
			return
		}
		f.WorkflowID = &id
	}
	rows, err := h.svc.List(r.Context(), p.OrgID, f, page)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	items := make([]View, 0, len(rows))
	for _, run := range rows {
		items = append(items, h.svc.View(r.Context(), run))
	}
	apix.WriteJSON(w, http.StatusOK, ListResponse{Items: items, NextCursor: apix.EncodeCursor(page, len(rows))})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	body, err := readBody(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	var req CreateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apix.Handle(w, r, err)
		return
	}
	h.idempotent(w, r, p, body, func() (apix.Result, error) {
		run, err := h.svc.Create(r.Context(), p, req)
		if err != nil {
			return apix.Result{}, err
		}
		return apix.Result{Status: http.StatusAccepted, Body: h.svc.View(r.Context(), run), Headers: map[string]string{"Location": "/v1/runs/" + run.PublicID()}}, nil
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, ok := h.runID(w, r)
	if !ok {
		return
	}
	run, err := h.svc.Get(r.Context(), p.OrgID, id)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, h.svc.View(r.Context(), run))
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, ok := h.runID(w, r)
	if !ok {
		return
	}
	run, err := h.svc.Cancel(r.Context(), p, id)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, h.svc.View(r.Context(), run))
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, ok := h.runID(w, r)
	if !ok {
		return
	}
	if _, err := h.svc.Get(r.Context(), p.OrgID, id); err != nil {
		apix.Handle(w, r, err)
		return
	}
	last, err := sse.LastEventID(r)
	if err != nil {
		apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/" + sse.HeaderLastEventID, Code: "invalid"}))
		return
	}
	opts := sse.Options{IdleTimeout: h.svc.d.Limits.HTTP.SSEIdleTimeout()}
	if err := sse.Stream(w, r, h.svc.Broker(), h.svc, p.OrgID, id, last, opts); err != nil {
		h.svc.d.Logger.Warn("fluxo de eventos interrompido", "run_id", apix.Encode(apix.PrefixRun, id), "err", err.Error())
	}
}

func (h *Handler) findings(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, ok := h.runID(w, r)
	if !ok {
		return
	}
	page, aerr := apix.ParsePage(r)
	if aerr != nil {
		apix.Handle(w, r, aerr)
		return
	}
	var f FindingFilter
	q := r.URL.Query()
	if sev := q.Get("severity"); sev != "" {
		for _, s := range strings.Split(sev, ",") {
			s = strings.TrimSpace(s)
			switch s {
			case "info", "low", "medium", "high", "critical":
				f.Severities = append(f.Severities, s)
			default:
				apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/severity", Code: "invalid"}))
				return
			}
		}
	}
	if st := q.Get("status"); st != "" {
		switch st {
		case "open", "confirmed", "dismissed", "resolved":
			f.Status = st
		default:
			apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/status", Code: "invalid"}))
			return
		}
	}
	rows, err := h.svc.Findings(r.Context(), p.OrgID, id, f, page)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	items := make([]FindingView, 0, len(rows))
	for _, fd := range rows {
		items = append(items, fd.View())
	}
	apix.WriteJSON(w, http.StatusOK, FindingListResponse{Items: items, NextCursor: apix.EncodeCursor(page, len(rows))})
}

func (h *Handler) feedback(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, ok := h.runID(w, r)
	if !ok {
		return
	}
	fid, err := apix.Decode(apix.PrefixFinding, r.PathValue("fid"))
	if err != nil {
		apix.Write(w, r, contracts.ErrFindingNotFound, nil)
		return
	}
	var req FeedbackRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apix.Handle(w, r, err)
		return
	}
	f, err := h.svc.GiveFeedback(r.Context(), p, id, fid, req)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, f.View())
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, ok := h.runID(w, r)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	ex, err := h.svc.Export(r.Context(), p.OrgID, id, format)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	w.Header().Set("Content-Type", ex.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, ex.FileName))
	w.Header().Set("Cache-Control", "private, max-age=0")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if err := ex.Write(w); err != nil {
		h.svc.d.Logger.Warn("exportação interrompida", "run_id", apix.Encode(apix.PrefixRun, id), "err", err.Error())
	}
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, ok := h.runID(w, r)
	if !ok {
		return
	}
	body, err := readBody(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	var req ApproveRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apix.Handle(w, r, err)
		return
	}
	h.idempotent(w, r, p, body, func() (apix.Result, error) {
		run, err := h.svc.Approve(r.Context(), p, id, req)
		if err != nil {
			return apix.Result{}, err
		}
		return apix.Result{Status: http.StatusOK, Body: h.svc.View(r.Context(), run)}, nil
	})
}

// Export describes a generated file (see export.go).
type Export struct {
	ContentType string
	FileName    string
	Write       func(w io.Writer) error
}
