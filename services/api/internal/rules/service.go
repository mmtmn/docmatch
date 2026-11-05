package rules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Recorder appends audit entries.
type Recorder interface {
	Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error)
}

// Deps are the collaborators of the service.
type Deps struct {
	Store  Store
	Idem   apix.IdemStore
	Limits *limits.Limits
	Audit  Recorder
	Logger *slog.Logger
	Now    func() time.Time
}

// Service implements the rules operations.
type Service struct {
	d Deps
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("rules: store é obrigatório")
	}
	if d.Idem == nil {
		d.Idem = apix.NewMemIdemStore()
	}
	if d.Limits == nil {
		d.Limits = limits.Must()
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}, nil
}

var (
	sourceKinds = map[string]bool{"document": true, "table": true, "media": true, "email": true, "archive": true}
	workflowRe  = regexp.MustCompile(`^wfl_[0-9A-HJKMNP-TV-Z]{26}$`)
)

// CreateRequest is openapi RuleCreateRequest.
type CreateRequest struct {
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	AppliesTo *AppliesTo `json:"applies_to,omitempty"`
	Compile   *bool      `json:"compile,omitempty"`
}

// VersionRequest is openapi RuleVersionRequest.
type VersionRequest struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body"`
}

// PromoteRequest is openapi RulePromoteRequest.
type PromoteRequest struct {
	To string `json:"to"`
}

func (s *Service) validateText(title, body string, titleRequired bool) (string, string, error) {
	var fields []apix.FieldError
	t, err := httpx.ValidateText(title, s.d.Limits.Text.SavedRuleTitle, true)
	switch {
	case errors.Is(err, httpx.ErrTooLong):
		return "", "", apix.E(contracts.ErrTextTooLong, err).With(map[string]any{"path": "/title", "max": s.d.Limits.Text.SavedRuleTitle})
	case err != nil:
		fields = append(fields, apix.FieldError{Path: "/title", Code: "invalid"})
	case t == "" && titleRequired:
		fields = append(fields, apix.FieldError{Path: "/title", Code: "required"})
	}
	b, err := httpx.ValidateText(body, s.d.Limits.Text.SavedRuleBody, false)
	switch {
	case errors.Is(err, httpx.ErrTooLong):
		return "", "", apix.E(contracts.ErrTextTooLong, err).With(map[string]any{"path": "/body", "max": s.d.Limits.Text.SavedRuleBody})
	case err != nil:
		fields = append(fields, apix.FieldError{Path: "/body", Code: "invalid"})
	case b == "":
		fields = append(fields, apix.FieldError{Path: "/body", Code: "required"})
	}
	if len(fields) > 0 {
		return "", "", apix.Validation(fields...)
	}
	return t, b, nil
}

func validateAppliesTo(a *AppliesTo) error {
	if a == nil {
		return nil
	}
	var fields []apix.FieldError
	seen := map[string]bool{}
	for i, k := range a.SourceKinds {
		if !sourceKinds[k] || seen[k] {
			fields = append(fields, apix.FieldError{Path: fmt.Sprintf("/applies_to/source_kinds/%d", i), Code: "invalid"})
		}
		seen[k] = true
	}
	seenW := map[string]bool{}
	for i, w := range a.WorkflowIDs {
		if !workflowRe.MatchString(w) || seenW[w] {
			fields = append(fields, apix.FieldError{Path: fmt.Sprintf("/applies_to/workflow_ids/%d", i), Code: "invalid"})
		}
		seenW[w] = true
	}
	if len(fields) > 0 {
		return apix.Validation(fields...)
	}
	return nil
}

// Create saves a rule. Compilation needs a harness RPC the contract does
// not have yet, so every rule is born draft and the learning job (which
// does go through the harness) is what produces compiled candidates.
func (s *Service) Create(ctx context.Context, p apix.Principal, req CreateRequest) (Rule, error) {
	title, body, err := s.validateText(req.Title, req.Body, true)
	if err != nil {
		return Rule{}, err
	}
	if err := validateAppliesTo(req.AppliesTo); err != nil {
		return Rule{}, err
	}
	now := s.d.Now().UTC()
	r := Rule{
		OrgID: p.OrgID, Title: title, Body: body, Version: 1, Status: StatusDraft, Scope: ScopeTenant, AppliesTo: req.AppliesTo,
		Enabled: true, CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now,
		Provenance: Provenance{Origin: OriginUser, ProposedAt: now.Format(time.RFC3339)},
		Stats:      Stats{Currency: "BRL"},
	}
	if p.UserID != nil {
		r.Provenance.AcceptedBy = apix.Encode(apix.PrefixUser, *p.UserID)
		r.Provenance.AcceptedAt = now.Format(time.RFC3339)
	}
	created, err := s.d.Store.CreateRule(ctx, r)
	if err != nil {
		return Rule{}, fmt.Errorf("rules: gravar: %w", err)
	}
	if _, err := s.d.Store.InsertVersion(ctx, versionOf(created)); err != nil {
		return Rule{}, fmt.Errorf("rules: gravar versão: %w", err)
	}
	s.record(ctx, p.OrgID, p.Actor(), "rule.create", created, map[string]any{"version": created.Version, "status": created.Status})
	return created, nil
}

func versionOf(r Rule) Version {
	return Version{OrgID: r.OrgID, RuleID: r.ID, Version: r.Version, Title: r.Title, Body: r.Body, Compiled: r.Compiled, Status: r.Status, CreatedBy: r.CreatedBy, CreatedAt: r.UpdatedAt}
}

// Get returns a rule of the organization.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Rule, error) {
	r, ok, err := s.d.Store.GetRule(ctx, orgID, id)
	if err != nil {
		return Rule{}, fmt.Errorf("rules: ler: %w", err)
	}
	if !ok {
		return Rule{}, apix.E(contracts.ErrRuleNotFound, nil)
	}
	return r, nil
}

// NewVersion keeps the id, bumps the version and stores the new text as a
// draft that goes through the lifecycle again.
func (s *Service) NewVersion(ctx context.Context, p apix.Principal, id uuid.UUID, req VersionRequest) (Rule, error) {
	r, err := s.Get(ctx, p.OrgID, id)
	if err != nil {
		return Rule{}, err
	}
	if r.Scope == ScopeGlobal {
		return Rule{}, apix.E(contracts.ErrRuleReadOnly, nil)
	}
	title := req.Title
	if title == "" {
		title = r.Title
	}
	title, body, err := s.validateText(title, req.Body, true)
	if err != nil {
		return Rule{}, err
	}
	r.Title, r.Body, r.Version, r.Status, r.Compiled = title, body, r.Version+1, StatusDraft, nil
	r.UpdatedAt = s.d.Now().UTC()
	updated, err := s.d.Store.UpdateRuleVersion(ctx, r)
	if err != nil {
		return Rule{}, fmt.Errorf("rules: nova versão: %w", err)
	}
	v := versionOf(updated)
	v.CreatedBy = p.UserID
	if _, err := s.d.Store.InsertVersion(ctx, v); err != nil {
		return Rule{}, fmt.Errorf("rules: gravar versão: %w", err)
	}
	s.record(ctx, p.OrgID, p.Actor(), "rule.version", updated, map[string]any{"version": updated.Version})
	return updated, nil
}

// Promote moves candidate → verified (needs history without false
// positives) or verified → trusted.
func (s *Service) Promote(ctx context.Context, p apix.Principal, id uuid.UUID, to string) (Rule, error) {
	if to != StatusVerified && to != StatusTrusted {
		return Rule{}, apix.Validation(apix.FieldError{Path: "/to", Code: "invalid"})
	}
	r, err := s.Get(ctx, p.OrgID, id)
	if err != nil {
		return Rule{}, err
	}
	if r.Scope == ScopeGlobal {
		return Rule{}, apix.E(contracts.ErrRuleReadOnly, nil)
	}
	switch to {
	case StatusVerified:
		if r.Status != StatusCandidate || r.Stats.RunsEvaluated < 1 || r.Stats.FalsePositives > 0 {
			return Rule{}, apix.Ef(contracts.ErrRuleNotPromotable, "status %s, %d execuções, %d falsos positivos", r.Status, r.Stats.RunsEvaluated, r.Stats.FalsePositives)
		}
	case StatusTrusted:
		if r.Status != StatusVerified {
			return Rule{}, apix.Ef(contracts.ErrRuleNotPromotable, "status %s", r.Status)
		}
	}
	promoted, err := s.d.Store.PromoteRule(ctx, p.OrgID, id, to)
	if err != nil {
		return Rule{}, fmt.Errorf("rules: promover: %w", err)
	}
	s.record(ctx, p.OrgID, p.Actor(), "rule.promote", promoted, map[string]any{"from": r.Status, "to": to})
	return promoted, nil
}

// Search lists rules: with a term, by trigram similarity on title and
// body; without one, the recently used first (the picker's "used before").
func (s *Service) Search(ctx context.Context, orgID uuid.UUID, q Query) ([]Rule, error) {
	if q.Term != "" {
		term, err := httpx.ValidateText(q.Term, s.d.Limits.Text.RulePickerSearch, true)
		if errors.Is(err, httpx.ErrTooLong) {
			return nil, apix.E(contracts.ErrTextTooLong, err).With(map[string]any{"path": "/q", "max": s.d.Limits.Text.RulePickerSearch})
		}
		if err != nil {
			return nil, apix.Validation(apix.FieldError{Path: "/q", Code: "invalid"})
		}
		q.Term = term
	}
	if q.Scope == ScopeGlobal {
		// Global rules are curated by the admin API and do not exist yet.
		return []Rule{}, nil
	}
	rows, err := s.d.Store.SearchRules(ctx, orgID, q)
	if err != nil {
		return nil, fmt.Errorf("rules: buscar: %w", err)
	}
	return rows, nil
}

// MissingRules reports which ids are not rules of the organization.
func (s *Service) MissingRules(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.d.Store.ListRulesByIDs(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	found := map[uuid.UUID]bool{}
	for _, r := range rows {
		found[r.ID] = true
	}
	var missing []uuid.UUID
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// Resolve returns the rules a run applies: every trusted rule when
// useRules is set, plus the explicitly requested ones (any status but
// retired). Unknown explicit ids are an error (rule_not_found).
func (s *Service) Resolve(ctx context.Context, orgID uuid.UUID, useRules bool, explicit []uuid.UUID) ([]Rule, error) {
	out := map[uuid.UUID]Rule{}
	if useRules {
		trusted, err := s.d.Store.ListTrustedRules(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("rules: listar confiáveis: %w", err)
		}
		for _, r := range trusted {
			out[r.ID] = r
		}
	}
	if len(explicit) > 0 {
		rows, err := s.d.Store.ListRulesByIDs(ctx, orgID, explicit)
		if err != nil {
			return nil, fmt.Errorf("rules: listar: %w", err)
		}
		found := map[uuid.UUID]bool{}
		for _, r := range rows {
			found[r.ID] = true
			if r.Status != StatusRetired && r.Enabled {
				out[r.ID] = r
			}
		}
		for _, id := range explicit {
			if !found[id] {
				return nil, apix.Ef(contracts.ErrRuleNotFound, "regra %s", apix.Encode(apix.PrefixRule, id))
			}
		}
	}
	list := make([]Rule, 0, len(out))
	for _, id := range explicit {
		if r, ok := out[id]; ok {
			list = append(list, r)
			delete(out, id)
		}
	}
	for _, r := range out {
		list = append(list, r)
	}
	return list, nil
}

// Touch records that rules were used by a run.
func (s *Service) Touch(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return s.d.Store.TouchRules(ctx, orgID, ids)
}

// Propose stores the candidates the harness derived from feedback.
func (s *Service) Propose(ctx context.Context, orgID uuid.UUID, runID *uuid.UUID, proposals []*pb.Rule) ([]Rule, error) {
	now := s.d.Now().UTC()
	var out []Rule
	for _, p := range proposals {
		r := FromProto(orgID, runID, now, p)
		title, body, err := s.validateText(r.Title, r.Body, true)
		if err != nil {
			s.d.Logger.Warn("regra proposta ignorada", slog.String("err", err.Error()))
			continue
		}
		r.Title, r.Body = title, body
		created, err := s.d.Store.CreateRule(ctx, r)
		if err != nil {
			return out, fmt.Errorf("rules: gravar proposta: %w", err)
		}
		if _, err := s.d.Store.InsertVersion(ctx, versionOf(created)); err != nil {
			return out, fmt.Errorf("rules: gravar versão: %w", err)
		}
		s.record(ctx, orgID, audit.Actor{Type: audit.ActorSystem, ID: "worker"}, "rule.propose", created, map[string]any{"status": created.Status, "run_id": created.Provenance.RunID})
		out = append(out, created)
	}
	return out, nil
}

func (s *Service) record(ctx context.Context, orgID uuid.UUID, actor audit.Actor, action string, r Rule, meta map[string]any) {
	if s.d.Audit == nil {
		return
	}
	org := orgID
	if _, err := s.d.Audit.Record(ctx, &org, actor, action, audit.Target{Type: "rule", ID: r.PublicID()}, meta); err != nil {
		s.d.Logger.Error("auditoria falhou", slog.String("action", action), slog.String("err", err.Error()))
	}
}

// ------------------------------------------------------------------ HTTP

// Handler serves the rules routes.
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
	r.HandleFunc(http.MethodGet, "/v1/rules", httpx.BodyNone, h.list)
	r.HandleFunc(http.MethodPost, "/v1/rules", httpx.BodyJSON, h.create)
	r.HandleFunc(http.MethodPost, "/v1/rules/{id}/versions", httpx.BodyJSON, h.version)
	r.HandleFunc(http.MethodPost, "/v1/rules/{id}/promote", httpx.BodyJSON, h.promote)
}

// ListResponse is openapi RuleList.
type ListResponse struct {
	Items      []View  `json:"items"`
	NextCursor *string `json:"next_cursor"`
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
	qs := r.URL.Query()
	q := Query{Term: strings.TrimSpace(qs.Get("q")), Status: qs.Get("status"), Scope: qs.Get("scope"), Limit: page.Limit, Offset: page.Offset}
	if q.Status != "" && statusProto(q.Status) == pb.RuleStatus_RULE_STATUS_UNSPECIFIED {
		apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/status", Code: "invalid"}))
		return
	}
	if q.Scope != "" && q.Scope != ScopeTenant && q.Scope != ScopeGlobal {
		apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/scope", Code: "invalid"}))
		return
	}
	rows, err := h.svc.Search(r.Context(), p.OrgID, q)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	items := make([]View, 0, len(rows))
	for _, rule := range rows {
		items = append(items, rule.View())
	}
	apix.WriteJSON(w, http.StatusOK, ListResponse{Items: items, NextCursor: apix.EncodeCursor(page, len(rows))})
}

// idempotent wraps a credit-spending handler with the Idempotency-Key protocol.
func (h *Handler) idempotent(w http.ResponseWriter, r *http.Request, p apix.Principal, body []byte, fn func() (apix.Result, error)) {
	key, aerr := apix.IdemKey(r)
	if aerr != nil {
		apix.Handle(w, r, aerr)
		return
	}
	res, err := apix.Idempotent(r.Context(), h.svc.d.Idem, p.OrgID, key, apix.IdemHash(r, body), h.svc.d.Now(), fn)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, res.Status, res.Body)
}

func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, httpx.NewRequestError(http.StatusBadRequest, httpx.CodeInvalidJSON, err)
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return body, nil
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
		rule, err := h.svc.Create(r.Context(), p, req)
		if err != nil {
			return apix.Result{}, err
		}
		return apix.Result{Status: http.StatusCreated, Body: rule.View()}, nil
	})
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, err := apix.Decode(apix.PrefixRule, r.PathValue("id"))
	if err != nil {
		apix.Write(w, r, contracts.ErrRuleNotFound, nil)
		return
	}
	body, err := readBody(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	var req VersionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apix.Handle(w, r, err)
		return
	}
	h.idempotent(w, r, p, body, func() (apix.Result, error) {
		rule, err := h.svc.NewVersion(r.Context(), p, id, req)
		if err != nil {
			return apix.Result{}, err
		}
		return apix.Result{Status: http.StatusCreated, Body: rule.View()}, nil
	})
}

func (h *Handler) promote(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, err := apix.Decode(apix.PrefixRule, r.PathValue("id"))
	if err != nil {
		apix.Write(w, r, contracts.ErrRuleNotFound, nil)
		return
	}
	var req PromoteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apix.Handle(w, r, err)
		return
	}
	rule, err := h.svc.Promote(r.Context(), p, id, req.To)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, rule.View())
}
