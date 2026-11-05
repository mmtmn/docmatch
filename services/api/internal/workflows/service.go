package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// RuleChecker reports which of the referenced rules do not exist (rules.Service).
type RuleChecker interface {
	MissingRules(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error)
}

// Recorder appends audit entries.
type Recorder interface {
	Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error)
}

// Deps are the collaborators of the service.
type Deps struct {
	Store  Store
	Rules  RuleChecker
	Limits *limits.Limits
	Audit  Recorder
	Logger *slog.Logger
	Now    func() time.Time
}

// Service implements the CRUD.
type Service struct {
	d Deps
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("workflows: store é obrigatório")
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

func (s *Service) checkRules(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 || s.d.Rules == nil {
		return nil
	}
	missing, err := s.d.Rules.MissingRules(ctx, orgID, ids)
	if err != nil {
		return fmt.Errorf("workflows: conferir regras: %w", err)
	}
	if len(missing) > 0 {
		return apix.Ef(contracts.ErrRuleNotFound, "regra %s", apix.Encode(apix.PrefixRule, missing[0])).With(map[string]any{"rule_id": apix.Encode(apix.PrefixRule, missing[0])})
	}
	return nil
}

// Create validates and stores a configuration.
func (s *Service) Create(ctx context.Context, p apix.Principal, cfg Config) (Workflow, error) {
	ruleIDs, aerr := cfg.Validate(s.d.Limits)
	if aerr != nil {
		return Workflow{}, aerr
	}
	if err := s.checkRules(ctx, p.OrgID, ruleIDs); err != nil {
		return Workflow{}, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return Workflow{}, err
	}
	now := s.d.Now().UTC()
	w, err := s.d.Store.CreateWorkflow(ctx, Workflow{OrgID: p.OrgID, Name: cfg.Name, Config: raw, CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return Workflow{}, fmt.Errorf("workflows: gravar: %w", err)
	}
	s.record(ctx, p, "workflow.create", w, map[string]any{"sources": len(cfg.Sources)})
	return w, nil
}

// Get returns a workflow of the organization.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Workflow, error) {
	w, ok, err := s.d.Store.GetWorkflow(ctx, orgID, id)
	if err != nil {
		return Workflow{}, fmt.Errorf("workflows: ler: %w", err)
	}
	if !ok {
		return Workflow{}, apix.E(contracts.ErrWorkflowNotFound, nil)
	}
	return w, nil
}

// Config decodes the stored configuration.
func (s *Service) Config(w Workflow) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(w.Config, &cfg); err != nil {
		return Config{}, fmt.Errorf("workflows: configuração gravada inválida: %w", err)
	}
	return cfg, nil
}

// List pages the workflows of the organization by name.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, page apix.Page) ([]Workflow, error) {
	rows, err := s.d.Store.ListWorkflows(ctx, orgID, page.Limit, page.Offset)
	if err != nil {
		return nil, fmt.Errorf("workflows: listar: %w", err)
	}
	return rows, nil
}

// RunsCount counts the runs created from a workflow.
func (s *Service) RunsCount(ctx context.Context, orgID, id uuid.UUID) int64 {
	n, err := s.d.Store.CountRuns(ctx, orgID, id)
	if err != nil {
		s.d.Logger.Warn("contar execuções do fluxo falhou", slog.String("err", err.Error()))
		return 0
	}
	return n
}

// Update applies a patch: the sent sections replace the stored ones and
// the whole configuration is validated again.
func (s *Service) Update(ctx context.Context, p apix.Principal, id uuid.UUID, patch Patch) (Workflow, error) {
	if patch.Empty() {
		return Workflow{}, apix.Validation(apix.FieldError{Path: "/", Code: "min_properties"})
	}
	w, err := s.Get(ctx, p.OrgID, id)
	if err != nil {
		return Workflow{}, err
	}
	cfg, err := s.Config(w)
	if err != nil {
		return Workflow{}, err
	}
	patch.Apply(&cfg)
	ruleIDs, aerr := cfg.Validate(s.d.Limits)
	if aerr != nil {
		return Workflow{}, aerr
	}
	if err := s.checkRules(ctx, p.OrgID, ruleIDs); err != nil {
		return Workflow{}, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return Workflow{}, err
	}
	w.Name, w.Config, w.UpdatedAt = cfg.Name, raw, s.d.Now().UTC()
	updated, err := s.d.Store.UpdateWorkflow(ctx, w)
	if err != nil {
		return Workflow{}, fmt.Errorf("workflows: atualizar: %w", err)
	}
	s.record(ctx, p, "workflow.update", updated, nil)
	return updated, nil
}

func (s *Service) record(ctx context.Context, p apix.Principal, action string, w Workflow, meta map[string]any) {
	if s.d.Audit == nil {
		return
	}
	org := p.OrgID
	if _, err := s.d.Audit.Record(ctx, &org, p.Actor(), action, audit.Target{Type: "workflow", ID: w.PublicID()}, meta); err != nil {
		s.d.Logger.Error("auditoria falhou", slog.String("action", action), slog.String("err", err.Error()))
	}
}

// Handler serves the workflows routes.
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
	r.HandleFunc(http.MethodGet, "/v1/workflows", httpx.BodyNone, h.list)
	r.HandleFunc(http.MethodPost, "/v1/workflows", httpx.BodyJSON, h.create)
	r.HandleFunc(http.MethodGet, "/v1/workflows/{id}", httpx.BodyNone, h.get)
	r.HandleFunc(http.MethodPatch, "/v1/workflows/{id}", httpx.BodyJSON, h.patch)
}

// ListResponse is openapi WorkflowList.
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
	rows, err := h.svc.List(r.Context(), p.OrgID, page)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	items := make([]View, 0, len(rows))
	for _, wf := range rows {
		items = append(items, wf.View(h.svc.RunsCount(r.Context(), p.OrgID, wf.ID)))
	}
	apix.WriteJSON(w, http.StatusOK, ListResponse{Items: items, NextCursor: apix.EncodeCursor(page, len(rows))})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	var cfg Config
	if err := httpx.DecodeJSON(r, &cfg); err != nil {
		apix.Handle(w, r, err)
		return
	}
	wf, err := h.svc.Create(r.Context(), p, cfg)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusCreated, wf.View(0))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, err := apix.Decode(apix.PrefixWorkflow, r.PathValue("id"))
	if err != nil {
		apix.Write(w, r, contracts.ErrWorkflowNotFound, nil)
		return
	}
	wf, err := h.svc.Get(r.Context(), p.OrgID, id)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, wf.View(h.svc.RunsCount(r.Context(), p.OrgID, wf.ID)))
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	p, err := h.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, err := apix.Decode(apix.PrefixWorkflow, r.PathValue("id"))
	if err != nil {
		apix.Write(w, r, contracts.ErrWorkflowNotFound, nil)
		return
	}
	var patch Patch
	if err := httpx.DecodeJSON(r, &patch); err != nil {
		apix.Handle(w, r, err)
		return
	}
	wf, err := h.svc.Update(r.Context(), p, id, patch)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, wf.View(h.svc.RunsCount(r.Context(), p.OrgID, wf.ID)))
}
