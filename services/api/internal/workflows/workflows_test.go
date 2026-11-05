package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

func validConfig() Config {
	return Config{
		Name:     "Conciliação bancária",
		Sources:  []SourceRole{{Role: "bank_statement", Kind: "table", Label: "Extrato"}, {Role: "ledger", Kind: "any", Accept: []string{"text/csv", ".xlsx"}}},
		Matching: &Matching{Keys: []string{"amount", "date"}, Strategies: []string{"exact", "sum"}},
		Rules:    &Rules{Text: "Tarifa até R$ 30 é parte do pagamento"},
		Budget:   &Budget{Lane: "app"},
		Outputs:  &Outputs{Formats: []string{"xlsx"}},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	l := limits.Must()
	valid := validConfig()
	if _, err := valid.Validate(l); err != nil {
		t.Fatalf("config válida recusada: %v", err)
	}
	mut := func(f func(c *Config)) Config {
		c := validConfig()
		f(&c)
		return c
	}
	bad := map[string]Config{
		"sem nome":           mut(func(c *Config) { c.Name = "" }),
		"nome longo":         mut(func(c *Config) { c.Name = strings.Repeat("a", 81) }),
		"sem fontes":         mut(func(c *Config) { c.Sources = nil }),
		"role duplicado":     mut(func(c *Config) { c.Sources[1].Role = "bank_statement" }),
		"role inválido":      mut(func(c *Config) { c.Sources[0].Role = "Bank" }),
		"kind inválido":      mut(func(c *Config) { c.Sources[0].Kind = "pdf" }),
		"accept inválido":    mut(func(c *Config) { c.Sources[1].Accept = []string{"nope"} }),
		"sem chaves":         mut(func(c *Config) { c.Matching = &Matching{} }),
		"chave inválida":     mut(func(c *Config) { c.Matching.Keys = []string{"color"} }),
		"chave duplicada":    mut(func(c *Config) { c.Matching.Keys = []string{"amount", "amount"} }),
		"estratégia":         mut(func(c *Config) { c.Matching.Strategies = []string{"magic"} }),
		"tolerância":         mut(func(c *Config) { v := 101.0; c.Matching.Tolerances = &Tolerances{AmountPercent: &v} }),
		"similaridade":       mut(func(c *Config) { v := 2.0; c.Matching.Tolerances = &Tolerances{TextSimilarity: &v} }),
		"dias negativos":     mut(func(c *Config) { v := -1; c.Matching.Tolerances = &Tolerances{Days: &v} }),
		"regra inválida":     mut(func(c *Config) { c.Rules = &Rules{RuleIDs: []string{"rul_x"}} }),
		"llm inválido":       mut(func(c *Config) { c.LLM = &LLM{Mode: "cloud"} }),
		"budget negativo":    mut(func(c *Config) { v := int64(-1); c.Budget = &Budget{DefaultCredits: &v} }),
		"lane inválida":      mut(func(c *Config) { c.Budget = &Budget{Lane: "warp"} }),
		"formato inválido":   mut(func(c *Config) { c.Outputs = &Outputs{Formats: []string{"doc"}} }),
		"manifest":           mut(func(c *Config) { c.Outputs = &Outputs{UIManifestID: "x"} }),
		"versão zero":        mut(func(c *Config) { v := 0; c.Version = &v }),
		"label com controle": mut(func(c *Config) { c.Sources[0].Label = "a\x01b" }),
	}
	for name, c := range bad {
		if _, err := c.Validate(l); err == nil {
			t.Errorf("%s deveria falhar", name)
		}
	}
	long := mut(func(c *Config) { c.Rules = &Rules{Text: strings.Repeat("x", 4001)} })
	if _, err := long.Validate(l); err == nil || err.Code != contracts.ErrTextTooLong {
		t.Fatalf("texto longo = %v", err)
	}
	withRules := mut(func(c *Config) { c.Rules = &Rules{RuleIDs: []string{apix.Encode(apix.PrefixRule, uuid.New())}} })
	ids, err := withRules.Validate(l)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids = %v %v", ids, err)
	}
	if !(Patch{}).Empty() || (Patch{Name: new(string)}).Empty() {
		t.Fatal("Patch.Empty")
	}
}

type fakeRules struct {
	missing []uuid.UUID
	err     error
}

func (f fakeRules) MissingRules(context.Context, uuid.UUID, []uuid.UUID) ([]uuid.UUID, error) {
	return f.missing, f.err
}

func newService(t *testing.T, rules RuleChecker) (*Service, *MemStore) {
	t.Helper()
	store := NewMemStore()
	svc, err := New(Deps{Store: store, Rules: rules, Logger: logx.Discard(), Now: func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

func TestServiceCRUD(t *testing.T) {
	t.Parallel()
	svc, store := newService(t, fakeRules{})
	org := uuid.New()
	user := uuid.New()
	p := apix.Principal{OrgID: org, UserID: &user, Plan: "pro"}
	ctx := context.Background()
	w, err := svc.Create(ctx, p, validConfig())
	if err != nil || w.Name != "Conciliação bancária" || w.CreatedBy == nil {
		t.Fatalf("create = %+v %v", w, err)
	}
	got, err := svc.Get(ctx, org, w.ID)
	if err != nil || got.ID != w.ID {
		t.Fatal("get")
	}
	if _, err := svc.Get(ctx, uuid.New(), w.ID); err == nil {
		t.Fatal("outra organização não deveria ver")
	}
	if _, err := svc.Get(ctx, org, uuid.New()); err == nil {
		t.Fatal("inexistente")
	}
	store.SetRuns(w.ID, 3)
	if svc.RunsCount(ctx, org, w.ID) != 3 {
		t.Fatal("RunsCount")
	}
	name := "Novo nome"
	updated, err := svc.Update(ctx, p, w.ID, Patch{Name: &name, Rules: &Rules{AutoLearning: boolPtr(true)}})
	if err != nil || updated.Name != "Novo nome" {
		t.Fatalf("update = %+v %v", updated, err)
	}
	cfg, _ := svc.Config(updated)
	if cfg.Rules == nil || cfg.Rules.AutoLearning == nil || !*cfg.Rules.AutoLearning || cfg.Rules.Text != "" || len(cfg.Sources) != 2 {
		t.Fatalf("seções substituídas: %+v", cfg)
	}
	if _, err := svc.Update(ctx, p, w.ID, Patch{}); err == nil {
		t.Fatal("patch vazio")
	}
	bad := ""
	if _, err := svc.Update(ctx, p, w.ID, Patch{Name: &bad}); err == nil {
		t.Fatal("patch inválido")
	}
	for i := 0; i < 3; i++ {
		c := validConfig()
		c.Name = "Fluxo " + string(rune('a'+i))
		if _, err := svc.Create(ctx, p, c); err != nil {
			t.Fatal(err)
		}
	}
	// Listed by name (then id), as the SQL query does: Fluxo a, b, c, Novo nome.
	rows, err := svc.List(ctx, org, apix.Page{Limit: 2})
	if err != nil || len(rows) != 2 || rows[0].Name != "Fluxo a" || rows[1].Name != "Fluxo b" {
		t.Fatalf("list = %v %v", rows, err)
	}
	rows, _ = svc.List(ctx, org, apix.Page{Limit: 2, Offset: 2})
	if len(rows) != 2 || rows[0].Name != "Fluxo c" || rows[1].Name != "Novo nome" {
		t.Fatalf("página 2 = %v", rows)
	}
	// Missing rule references.
	missingSvc, _ := newService(t, fakeRules{missing: []uuid.UUID{uuid.New()}})
	c := validConfig()
	c.Rules = &Rules{RuleIDs: []string{apix.Encode(apix.PrefixRule, uuid.New())}}
	_, err = missingSvc.Create(ctx, p, c)
	var ae *apix.Error
	if !errors.As(err, &ae) || ae.Code != contracts.ErrRuleNotFound {
		t.Fatalf("regra ausente = %v", err)
	}
	errSvc, _ := newService(t, fakeRules{err: errors.New("banco")})
	if _, err := errSvc.Create(ctx, p, c); err == nil {
		t.Fatal("erro do checker deveria propagar")
	}
	if _, err := New(Deps{}); err == nil {
		t.Fatal("deps vazias")
	}
}

func boolPtr(b bool) *bool { return &b }

func TestHandler(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t, fakeRules{})
	org := uuid.New()
	p := apix.Principal{OrgID: org, Plan: "pro"}
	r := httpx.NewRouter(limits.Must())
	NewHandler(svc, apix.Static(p)).Mount(r)
	srv := httptest.NewServer(httpx.Chain(r, httpx.RequestIDMiddleware))
	t.Cleanup(srv.Close)
	do := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequestWithContext(context.Background(), method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	cfg, _ := json.Marshal(validConfig())
	code, out := do(http.MethodPost, "/v1/workflows", string(cfg))
	if code != http.StatusCreated || out["runs_count"] != float64(0) || !strings.HasPrefix(out["id"].(string), "wfl_") {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["id"].(string)
	if code, out := do(http.MethodGet, "/v1/workflows/"+id, ""); code != http.StatusOK || out["id"] != id {
		t.Fatalf("get = %d %v", code, out)
	}
	if code, out := do(http.MethodGet, "/v1/workflows/wfl_nope", ""); code != http.StatusNotFound || out["code"] != "workflow_not_found" {
		t.Fatalf("get inválido = %d %v", code, out)
	}
	if code, out := do(http.MethodGet, "/v1/workflows/"+apix.Encode(apix.PrefixWorkflow, uuid.New()), ""); code != http.StatusNotFound || out["code"] != "workflow_not_found" {
		t.Fatalf("get inexistente = %d %v", code, out)
	}
	if code, out := do(http.MethodGet, "/v1/workflows?limit=5", ""); code != http.StatusOK || len(out["items"].([]any)) != 1 || out["next_cursor"] != nil {
		t.Fatalf("list = %d %v", code, out)
	}
	if code, _ := do(http.MethodGet, "/v1/workflows?limit=0", ""); code != http.StatusBadRequest {
		t.Fatal("limit inválido")
	}
	if code, out := do(http.MethodPatch, "/v1/workflows/"+id, `{"name":"Outro"}`); code != http.StatusOK {
		t.Fatalf("patch = %d %v", code, out)
	}
	if code, out := do(http.MethodPatch, "/v1/workflows/"+id, `{}`); code != http.StatusBadRequest || out["code"] != "validation_failed" {
		t.Fatalf("patch vazio = %d %v", code, out)
	}
	if code, _ := do(http.MethodPatch, "/v1/workflows/wfl_x", `{"name":"a"}`); code != http.StatusNotFound {
		t.Fatal("patch id inválido")
	}
	if code, out := do(http.MethodPost, "/v1/workflows", `{"name":"x"}`); code != http.StatusBadRequest || out["code"] != "validation_failed" {
		t.Fatalf("create inválido = %d %v", code, out)
	}
	if code, out := do(http.MethodPost, "/v1/workflows", `{"name":"x","typo":1}`); code != http.StatusBadRequest || out["code"] != "invalid_json" {
		t.Fatalf("campo extra = %d %v", code, out)
	}
	anon := httpx.NewRouter(limits.Must())
	NewHandler(svc, nil).Mount(anon)
	rec := httptest.NewRecorder()
	anon.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workflows", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem auth = %d", rec.Code)
	}
}
