package projects

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

func (s *Service) Mount(r *httpx.Router) {
	r.HandleFunc(http.MethodGet, "/v1/projects/dashboard", httpx.BodyNone, s.dashboard)
	r.HandleFunc(http.MethodGet, "/v1/projects/activity", httpx.BodyNone, s.accountActivity)
	r.HandleFunc("GET", "/v1/projects", httpx.BodyNone, s.list)
	r.HandleFunc("POST", "/v1/projects", httpx.BodyJSON, s.create)
	r.HandleFunc("POST", "/v1/projects/from-run", httpx.BodyJSON, s.importRun)
	r.HandleFunc("GET", "/v1/projects/{project}", httpx.BodyNone, s.get)
	r.HandleFunc("PATCH", "/v1/projects/{project}", httpx.BodyJSON, s.update)
	r.HandleFunc("DELETE", "/v1/projects/{project}", httpx.BodyNone, s.remove)
	r.HandleFunc("PUT", "/v1/projects/{project}/sharing", httpx.BodyJSON, s.share)
	r.HandleFunc("GET", "/v1/projects/{project}/activity", httpx.BodyNone, s.activity)
	r.HandleFunc("POST", "/v1/projects/{project}/documents", httpx.BodyJSON, s.attach)
	r.HandleFunc("DELETE", "/v1/projects/{project}/documents/{document}", httpx.BodyNone, s.removeDocument)
	r.HandleFunc("POST", "/v1/projects/{project}/documents/{document}/replace", httpx.BodyJSON, s.replaceDocument)
	r.HandleFunc("POST", "/v1/projects/{project}/uploads", httpx.BodyJSON, s.createUpload)
	r.HandleFunc("POST", "/v1/projects/{project}/uploads/{upload}/complete", httpx.BodyJSON, s.completeUpload)
	r.HandleFunc("POST", "/v1/projects/{project}/runs/estimate", httpx.BodyJSON, s.estimate)
	r.HandleFunc("POST", "/v1/projects/{project}/runs", httpx.BodyJSON, s.reconcile)
	r.HandleFunc("GET", "/v1/projects/{project}/runs", httpx.BodyNone, s.listRuns)
	if s.engine != nil {
		rules.NewHandler(s.engine.Rules, s.auth).MountProject(r, s.withRules)
		runs.NewHandler(s.engine.Runs, s.Resolve(s.auth)).MountProjectResults(r)
	}
}

func (s *Service) replaceDocument(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	previous, err := publicid.Decode("doc", r.PathValue("document"))
	if err != nil {
		apix.Handle(w, r, missing())
		return
	}
	var body struct {
		Version       int64  `json:"version"`
		ReplacementID string `json:"replacement_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	replacement, err := publicid.Decode("doc", body.ReplacementID)
	if err != nil {
		apix.Handle(w, r, missing())
		return
	}
	if err := s.ReplaceDocument(r.Context(), p, id, previous, replacement, body.Version); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Get(r.Context(), p, id)
	respond(w, r, v, err)
}

func (s *Service) principal(w http.ResponseWriter, r *http.Request) (apix.Principal, uuid.UUID, bool) {
	p, err := s.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return p, uuid.Nil, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodDelete && !s.rollout.Allows(p.OrgID) {
		apix.Handle(w, r, apix.E(contracts.ErrFeaturePaused, nil))
		return p, uuid.Nil, false
	}
	id := uuid.Nil
	if value := r.PathValue("project"); value != "" {
		id, err = publicid.Decode(Prefix, value)
		if err != nil {
			apix.Handle(w, r, missing())
			return p, id, false
		}
	}
	return p, id, true
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.principal(w, r)
	if !ok {
		return
	}
	page, err := apix.ParsePage(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	items, e := s.List(r.Context(), p, r.URL.Query().Get("archived") == "true", page.Limit, page.Offset)
	respond(w, r, map[string]any{"items": items, "next_cursor": apix.EncodeCursor(page, len(items))}, e)
}
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Create(r.Context(), p, body.Name)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusCreated, v)
}
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	v, err := s.Get(r.Context(), p, id)
	respond(w, r, v, err)
}
func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body Patch
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Update(r.Context(), p, id, body)
	respond(w, r, v, err)
}
func version(r *http.Request) (int64, error) {
	v, err := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
	if err != nil || v < 1 {
		return 0, apix.Validation(apix.FieldError{Path: "/version", Code: "required"})
	}
	return v, nil
}
func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	v, err := version(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	if err := s.Delete(r.Context(), p, id, v); err != nil {
		apix.Handle(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Service) share(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Version    int64    `json:"version"`
		Visibility string   `json:"visibility"`
		UserIDs    []string `json:"user_ids"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	users := []uuid.UUID{}
	for _, value := range body.UserIDs {
		uid, err := publicid.Decode(publicid.PrefixUser, value)
		if err != nil {
			apix.Handle(w, r, missing())
			return
		}
		users = append(users, uid)
	}
	if err := s.Share(r.Context(), p, id, body.Version, body.Visibility, users); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Get(r.Context(), p, id)
	respond(w, r, v, err)
}
func (s *Service) activity(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	v, err := s.Activity(r.Context(), p, id)
	respond(w, r, map[string]any{"items": v}, err)
}
func (s *Service) attach(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body AttachRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Attach(r.Context(), p, id, body)
	respond(w, r, map[string]any{"id": v}, err)
}
func (s *Service) removeDocument(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	doc, err := publicid.Decode("doc", r.PathValue("document"))
	if err != nil {
		apix.Handle(w, r, missing())
		return
	}
	v, err := version(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	if err := s.RemoveDocument(r.Context(), p, id, doc, v); err != nil {
		apix.Handle(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Service) createUpload(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body UploadRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.CreateUpload(r.Context(), p, id, body)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusCreated, v)
}
func (s *Service) completeUpload(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	upload, err := publicid.Decode("upl", r.PathValue("upload"))
	if err != nil {
		apix.Handle(w, r, missing())
		return
	}
	var body uploads.CompleteRequest
	if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			apix.Handle(w, r, err)
			return
		}
	}
	v, done, err := s.CompleteUpload(r.Context(), p, id, upload, body)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	status := http.StatusAccepted
	if done {
		status = http.StatusOK
	}
	apix.WriteJSON(w, status, v)
}
