package rules

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
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

func newService(t *testing.T) (*Service, *MemStore) {
	t.Helper()
	store := NewMemStore()
	svc, err := New(Deps{Store: store, Logger: logx.Discard(), Now: func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

func assertCode(t *testing.T, err error, code contracts.ErrorCode) {
	t.Helper()
	var ae *apix.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("erro = %v, esperado %s", err, code)
	}
}

func TestLifecycle(t *testing.T) {
	t.Parallel()
	svc, store := newService(t)
	ctx := context.Background()
	org, user := uuid.New(), uuid.New()
	p := apix.Principal{OrgID: org, UserID: &user, Plan: "pro"}
	r, err := svc.Create(ctx, p, CreateRequest{Title: "Tarifa bancária", Body: "Tarifa até R$ 30 no dia de um pagamento é parte dele", AppliesTo: &AppliesTo{SourceKinds: []string{"table"}}})
	if err != nil || r.Status != StatusDraft || r.Version != 1 || r.Provenance.Origin != OriginUser || r.Provenance.AcceptedBy == "" {
		t.Fatalf("create = %+v %v", r, err)
	}
	v := r.View()
	if v.Compiled != nil || v.TenantID == nil || v.Stats.Currency != "BRL" || !strings.HasPrefix(v.ID, "rul_") {
		t.Fatalf("view = %+v", v)
	}
	if len(store.Versions()) != 1 {
		t.Fatal("versão inicial não gravada")
	}
	_, err = svc.Create(ctx, p, CreateRequest{Title: "", Body: "x"})
	assertCode(t, err, contracts.ErrValidationFailed)
	_, err = svc.Create(ctx, p, CreateRequest{Title: "t", Body: strings.Repeat("x", 2001)})
	assertCode(t, err, contracts.ErrTextTooLong)
	_, err = svc.Create(ctx, p, CreateRequest{Title: strings.Repeat("x", 121), Body: "b"})
	assertCode(t, err, contracts.ErrTextTooLong)
	_, err = svc.Create(ctx, p, CreateRequest{Title: "t", Body: "b", AppliesTo: &AppliesTo{SourceKinds: []string{"pdf"}, WorkflowIDs: []string{"x"}}})
	assertCode(t, err, contracts.ErrValidationFailed)
	_, err = svc.Create(ctx, p, CreateRequest{Title: "t\x01", Body: "b"})
	assertCode(t, err, contracts.ErrValidationFailed)

	// New version keeps the id, bumps the version, resets to draft.
	v2, err := svc.NewVersion(ctx, p, r.ID, VersionRequest{Body: "Tarifa até R$ 50 é parte do pagamento"})
	if err != nil || v2.ID != r.ID || v2.Version != 2 || v2.Title != r.Title || v2.Status != StatusDraft {
		t.Fatalf("version = %+v %v", v2, err)
	}
	if len(store.Versions()) != 2 {
		t.Fatal("histórico de versões")
	}
	_, err = svc.NewVersion(ctx, p, uuid.New(), VersionRequest{Body: "x"})
	assertCode(t, err, contracts.ErrRuleNotFound)
	_, err = svc.NewVersion(ctx, apix.Principal{OrgID: uuid.New()}, r.ID, VersionRequest{Body: "x"})
	assertCode(t, err, contracts.ErrRuleNotFound)

	// Promotion rules.
	_, err = svc.Promote(ctx, p, r.ID, "trusted")
	assertCode(t, err, contracts.ErrRuleNotPromotable)
	_, err = svc.Promote(ctx, p, r.ID, "invalid")
	assertCode(t, err, contracts.ErrValidationFailed)
	cur, _, _ := store.GetRule(ctx, org, r.ID)
	cur.Status, cur.Stats = StatusCandidate, Stats{RunsEvaluated: 2, FalsePositives: 1}
	_, _ = store.UpdateRuleVersion(ctx, cur)
	_, err = svc.Promote(ctx, p, r.ID, "verified")
	assertCode(t, err, contracts.ErrRuleNotPromotable)
	cur.Stats.FalsePositives = 0
	_, _ = store.UpdateRuleVersion(ctx, cur)
	verified, err := svc.Promote(ctx, p, r.ID, "verified")
	if err != nil || verified.Status != StatusVerified {
		t.Fatalf("verified = %+v %v", verified, err)
	}
	trusted, err := svc.Promote(ctx, p, r.ID, "trusted")
	if err != nil || trusted.Status != StatusTrusted {
		t.Fatalf("trusted = %+v %v", trusted, err)
	}
	// Global rules are read-only.
	global, _ := store.CreateRule(ctx, Rule{OrgID: org, Title: "g", Body: "g", Scope: ScopeGlobal, Status: StatusTrusted, Enabled: true})
	_, err = svc.Promote(ctx, p, global.ID, "trusted")
	assertCode(t, err, contracts.ErrRuleReadOnly)
	_, err = svc.NewVersion(ctx, p, global.ID, VersionRequest{Body: "x"})
	assertCode(t, err, contracts.ErrRuleReadOnly)
	if global.View().TenantID != nil {
		t.Fatal("regra global sem tenant")
	}

	// Resolve: trusted + explicit.
	draft, _ := svc.Create(ctx, p, CreateRequest{Title: "Rascunho", Body: "Ignorar centavos"})
	resolved, err := svc.Resolve(ctx, org, true, []uuid.UUID{draft.ID})
	if err != nil || len(resolved) != 3 || resolved[0].ID != draft.ID {
		t.Fatalf("resolve = %v %v", resolved, err)
	}
	resolved, _ = svc.Resolve(ctx, org, false, nil)
	if len(resolved) != 0 {
		t.Fatal("sem regras")
	}
	_, err = svc.Resolve(ctx, org, false, []uuid.UUID{uuid.New()})
	assertCode(t, err, contracts.ErrRuleNotFound)
	if err := svc.Touch(ctx, org, []uuid.UUID{r.ID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Touch(ctx, org, nil); err != nil {
		t.Fatal(err)
	}
	touched, _, _ := store.GetRule(ctx, org, r.ID)
	if touched.UseCount != 1 || touched.LastUsedAt == nil {
		t.Fatal("touch")
	}
	missing, err := svc.MissingRules(ctx, org, []uuid.UUID{r.ID, uuid.New()})
	if err != nil || len(missing) != 1 {
		t.Fatalf("missing = %v %v", missing, err)
	}
	if m, _ := svc.MissingRules(ctx, org, nil); m != nil {
		t.Fatal("missing vazio")
	}

	// Search: recently used first without a term; substring with one.
	rows, err := svc.Search(ctx, org, Query{Limit: 10})
	if err != nil || len(rows) < 3 || rows[0].ID != r.ID {
		t.Fatalf("search = %v %v", rows, err)
	}
	rows, _ = svc.Search(ctx, org, Query{Term: "centavos", Limit: 10})
	if len(rows) != 1 || rows[0].ID != draft.ID {
		t.Fatalf("busca por termo = %v", rows)
	}
	rows, _ = svc.Search(ctx, org, Query{Status: StatusTrusted, Scope: ScopeTenant, Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("filtro = %v", rows)
	}
	if rows, _ := svc.Search(ctx, org, Query{Scope: ScopeGlobal}); len(rows) != 0 {
		t.Fatal("globais ainda não existem")
	}
	_, err = svc.Search(ctx, org, Query{Term: strings.Repeat("x", 101)})
	assertCode(t, err, contracts.ErrTextTooLong)
	_, err = svc.Search(ctx, org, Query{Term: "a\nb"})
	assertCode(t, err, contracts.ErrValidationFailed)

	// Proposals from the harness.
	runID := uuid.New()
	params, _ := structpb.NewStruct(map[string]any{"max": 30})
	proposed, err := svc.Propose(ctx, org, &runID, []*pb.Rule{
		{Title: "Aprendida", Body: "Quando x, y", Compiled: &pb.CompiledRule{Language: "sql", Text: "SELECT 1", Hash: strings.Repeat("a", 64), Parameters: params, CompiledAt: timestamppb.Now()}, Stats: &pb.RuleStats{RunsEvaluated: 1, ExplainedAmount: 100}, Provenance: &pb.RuleProvenance{Model: "m"}},
		{Title: "Sem compilação", Body: "corpo"},
		{Title: "", Body: "inválida"},
	})
	if err != nil || len(proposed) != 2 || proposed[0].Status != StatusCandidate || proposed[0].Compiled == nil || proposed[0].Compiled.Parameters["max"] != float64(30) || proposed[1].Status != StatusDraft {
		t.Fatalf("propose = %+v %v", proposed, err)
	}
	if proposed[0].Provenance.RunID != apix.Encode(apix.PrefixRun, runID) || proposed[0].Provenance.Origin != OriginModel {
		t.Fatalf("provenance = %+v", proposed[0].Provenance)
	}
	pbRule := ToProto(proposed[0])
	if pbRule.GetStatus() != pb.RuleStatus_RULE_STATUS_CANDIDATE || pbRule.GetCompiled().GetText() != "SELECT 1" || pbRule.GetProvenance().GetOrigin() != pb.RuleOrigin_RULE_ORIGIN_MODEL || pbRule.GetCompiled().GetParameters() == nil {
		t.Fatalf("ToProto = %v", pbRule)
	}
	if ToProto(global).GetScope() != pb.RuleScope_RULE_SCOPE_GLOBAL {
		t.Fatal("scope global")
	}
	if MarshalJSONB(nil) != nil || string(MarshalJSONB(map[string]int{"a": 1})) != `{"a":1}` {
		t.Fatal("MarshalJSONB")
	}
	if _, err := New(Deps{}); err == nil {
		t.Fatal("deps vazias")
	}
}

func TestHandler(t *testing.T) {
	t.Parallel()
	svc, store := newService(t)
	org := uuid.New()
	p := apix.Principal{OrgID: org, Plan: "pro"}
	r := httpx.NewRouter(limits.Must())
	NewHandler(svc, apix.Static(p)).Mount(r)
	srv := httptest.NewServer(httpx.Chain(r, httpx.RequestIDMiddleware))
	t.Cleanup(srv.Close)
	do := func(method, path, body, idem string) (int, map[string]any) {
		req, _ := http.NewRequestWithContext(context.Background(), method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if idem != "" {
			req.Header.Set(apix.HeaderIdempotencyKey, idem)
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
	body := `{"title":"Tarifa","body":"Tarifa até R$ 30 é parte do pagamento"}`
	if code, out := do(http.MethodPost, "/v1/rules", body, ""); code != http.StatusBadRequest || out["code"] != "missing_idempotency_key" {
		t.Fatalf("sem Idempotency-Key = %d %v", code, out)
	}
	code, out := do(http.MethodPost, "/v1/rules", body, "k1")
	if code != http.StatusCreated || out["status"] != "draft" {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["id"].(string)
	if code, out := do(http.MethodPost, "/v1/rules", body, "k1"); code != http.StatusCreated || out["id"] != id {
		t.Fatalf("replay = %d %v", code, out)
	}
	if code, out := do(http.MethodPost, "/v1/rules", `{"title":"Outra","body":"x"}`, "k1"); code != http.StatusConflict || out["code"] != "idempotency_key_reused" {
		t.Fatalf("chave reutilizada = %d %v", code, out)
	}
	if code, out := do(http.MethodPost, "/v1/rules", `{"title":"","body":"x"}`, "k2"); code != http.StatusBadRequest || out["code"] != "validation_failed" {
		t.Fatalf("inválido = %d %v", code, out)
	}
	if code, out := do(http.MethodPost, "/v1/rules/"+id+"/versions", `{"body":"Tarifa até R$ 50"}`, "k3"); code != http.StatusCreated || out["version"] != float64(2) {
		t.Fatalf("version = %d %v", code, out)
	}
	if code, _ := do(http.MethodPost, "/v1/rules/rul_x/versions", `{"body":"x"}`, "k4"); code != http.StatusNotFound {
		t.Fatal("version id inválido")
	}
	if code, out := do(http.MethodPost, "/v1/rules/"+id+"/promote", `{"to":"verified"}`, ""); code != http.StatusConflict || out["code"] != "rule_not_promotable" {
		t.Fatalf("promote = %d %v", code, out)
	}
	if code, _ := do(http.MethodPost, "/v1/rules/rul_x/promote", `{"to":"verified"}`, ""); code != http.StatusNotFound {
		t.Fatal("promote id inválido")
	}
	if code, out := do(http.MethodGet, "/v1/rules?q=tarifa", "", ""); code != http.StatusOK || len(out["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, out)
	}
	if code, out := do(http.MethodGet, "/v1/rules?status=nope", "", ""); code != http.StatusBadRequest || out["code"] != "validation_failed" {
		t.Fatalf("status inválido = %d %v", code, out)
	}
	if code, _ := do(http.MethodGet, "/v1/rules?scope=x", "", ""); code != http.StatusBadRequest {
		t.Fatal("scope inválido")
	}
	if code, _ := do(http.MethodGet, "/v1/rules?limit=1000", "", ""); code != http.StatusBadRequest {
		t.Fatal("limit inválido")
	}
	uid, _ := apix.Decode(apix.PrefixRule, id)
	cur, _, _ := store.GetRule(context.Background(), org, uid)
	cur.Status, cur.Stats = StatusCandidate, Stats{RunsEvaluated: 1}
	_, _ = store.UpdateRuleVersion(context.Background(), cur)
	if code, out := do(http.MethodPost, "/v1/rules/"+id+"/promote", `{"to":"verified"}`, ""); code != http.StatusOK || out["status"] != "verified" {
		t.Fatalf("promote ok = %d %v", code, out)
	}
	anon := httpx.NewRouter(limits.Must())
	NewHandler(svc, nil).Mount(anon)
	rec := httptest.NewRecorder()
	anon.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rules", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem auth = %d", rec.Code)
	}
}

func TestRetiredRuleLeavesAutomaticSelection(t *testing.T) {
	store := NewMemStore()
	svc, err := New(Deps{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	p := apix.Principal{OrgID: uuid.New()}
	rule, err := store.CreateRule(t.Context(), Rule{OrgID: p.OrgID, Title: "Conferir", Body: "Conferir valores", Status: StatusTrusted, Enabled: true, Scope: ScopeTenant})
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Resolve(t.Context(), p.OrgID, true, nil)
	if err != nil || len(before) != 1 {
		t.Fatal(before, err)
	}
	retired, err := svc.Promote(t.Context(), p, rule.ID, StatusRetired)
	if err != nil || retired.Status != StatusRetired {
		t.Fatal(retired, err)
	}
	after, err := svc.Resolve(t.Context(), p.OrgID, true, nil)
	if err != nil || len(after) != 0 {
		t.Fatal(after, err)
	}
}
