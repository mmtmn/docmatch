package account

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

const pricingSample = `
plans:
  starter:
    document_files_per_run: 10
    document_file_mb: 25
    document_run_mb: 100
    media_files_per_run: 2000
    media_run_gb: 2
    media_run_hours: 2
    concurrent_runs: 1
    api_keys: 1
    api_lanes: [api_batch]
    auto_rule_learning: false
    ui_customizations_per_month: 1
    escalation_model: false
    retention_days: 30
  business:
    document_files_per_run: 100
    document_file_mb: 50
    document_run_mb: 500
    media_files_per_run: 50000
    media_run_gb: 100
    media_run_hours: 100
    concurrent_runs: 10
    api_keys: 20
    api_lanes: [api_batch, api_standard, api_priority]
    auto_rule_learning: true
    ui_customizations_per_month: -1
    escalation_model: true
    retention_days: 365
`

type fakeEnqueuer struct {
	exports []uuid.UUID
	erases  []uuid.UUID
	err     error
}

func (f *fakeEnqueuer) EnqueueExport(_ context.Context, id uuid.UUID) error {
	f.exports = append(f.exports, id)
	return f.err
}

func (f *fakeEnqueuer) EnqueueErase(_ context.Context, id uuid.UUID, _ time.Time) error {
	f.erases = append(f.erases, id)
	return f.err
}

// flakyAudit wraps the audit service so tests can inject failures.
type flakyAudit struct {
	inner Recorder
	err   error
}

func (f *flakyAudit) Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error) {
	if f.err != nil {
		return audit.Entry{}, f.err
	}
	return f.inner.Record(ctx, orgID, actor, action, target, meta)
}

type harness struct {
	t        *testing.T
	store    *MemoryStore
	sessions *session.Manager
	sstore   *session.MemoryStore
	audit    *audit.MemoryStore
	auditRec *flakyAudit
	svc      *Service
	handler  http.Handler
	enq      *fakeEnqueuer
	now      time.Time
	user     User
	org      Org
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return h.now }
	h.store = NewMemoryStore(clock)
	h.sstore = session.NewMemoryStore(clock)
	// Long windows: these tests move the clock by days and are not about expiry.
	h.sessions = session.NewManager(h.sstore, session.Config{Secure: true, AllowedOrigins: []string{"https://app.example.com"}, Now: clock,
		AccessTTL: 365 * 24 * time.Hour, RefreshTTL: 400 * 24 * time.Hour})
	h.audit = audit.NewMemoryStore()
	h.auditRec = &flakyAudit{inner: audit.New(h.audit)}
	keys, _ := crypto.NewKeyring(bytes.Repeat([]byte{3}, 32))
	cat, err := ParseEntitlements([]byte(pricingSample))
	if err != nil {
		t.Fatal(err)
	}
	h.enq = &fakeEnqueuer{}
	api, _ := url.Parse("https://api.example.com/")
	h.svc, err = New(Deps{
		Store: h.store, Sessions: h.sessions, Keys: keys, Entitlements: cat, Audit: h.auditRec,
		Enqueue: h.enq, APIBaseURL: api, SupportContact: "security@example.com", Logger: logx.Discard(), Now: clock,
		ExportPolicy: ratelimit.Policy{Limit: 5, Period: time.Minute}, RecentLoginWindow: 5 * time.Minute,
		Limiter: ratelimit.NewMemory(ratelimit.WithClock(clock)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter(limits.Must())
	h.svc.Mount(r)
	h.handler = httpx.Chain(r, httpx.RequestIDMiddleware, h.sessions.Middleware)
	h.user, h.org = h.store.Seed("ana@example.com", "Ana")
	h.store.CreditsOf[h.org.ID] = CreditSummary{Balance: 100, Reserved: 10, Available: 90, PlanCredits: 100}
	return h
}

func (h *harness) login(userID, orgID uuid.UUID) []*http.Cookie {
	h.t.Helper()
	rec := httptest.NewRecorder()
	if _, err := h.sessions.Issue(context.Background(), rec, httptest.NewRequest(http.MethodPost, "/", nil), userID, orgID); err != nil {
		h.t.Fatal(err)
	}
	return rec.Result().Cookies() //nolint:bodyclose // recorder
}

func (h *harness) do(method, path string, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", "https://app.example.com")
	for _, c := range cookies {
		if c.MaxAge >= 0 {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func body(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("corpo não é JSON: %s", rec.Body.String())
	}
	return m
}

func (h *harness) actions(org *uuid.UUID) []string {
	rows, _ := h.audit.Range(context.Background(), org, 1, 1<<62)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	if _, err := New(Deps{}); err == nil {
		t.Fatal("sem dependências")
	}
	h := newHarness(t)
	keys, _ := crypto.NewKeyring(bytes.Repeat([]byte{3}, 32))
	if _, err := New(Deps{Store: h.store, Sessions: h.sessions, Keys: keys}); err == nil {
		t.Fatal("sem auditoria")
	}
	svc, err := New(Deps{Store: h.store, Sessions: h.sessions, Keys: keys, Audit: audit.New(h.audit)})
	if err != nil || svc.d.Limits == nil || svc.d.DeletionGrace != DefaultDeletionGrace || svc.d.ExportTTL != DefaultExportTTL || svc.d.RecentLoginWindow != DefaultRecentLoginWindow || svc.d.ExportPolicy.Limit != 10 {
		t.Fatalf("padrões: %+v %v", svc.d, err)
	}
}

func TestGetMe(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if rec := h.do(http.MethodGet, "/v1/me", "", nil); rec.Code != 401 {
		t.Fatalf("sem sessão: %d", rec.Code)
	}
	cs := h.login(h.user.ID, h.org.ID)
	rec := h.do(http.MethodGet, "/v1/me", "", cs)
	if rec.Code != 200 {
		t.Fatalf("GET /me: %d %s", rec.Code, rec.Body.String())
	}
	me := body(t, rec)
	user := me["user"].(map[string]any)
	org := me["org"].(map[string]any)
	if !strings.HasPrefix(user["id"].(string), "usr_") || user["email"] != "ana@example.com" || user["role"] != "owner" || user["locale"] != "pt-BR" {
		t.Fatalf("user: %v", user)
	}
	if !strings.HasPrefix(org["id"].(string), "org_") || org["plan"] != nil || org["plan_status"] != "none" {
		t.Fatalf("org: %v", org)
	}
	if me["credits"].(map[string]any)["available"] != float64(90) || me["entitlements"].(map[string]any)["api_keys"] != float64(1) {
		t.Fatalf("credits/entitlements: %v", me)
	}
	if n := me["notifications"].(map[string]any); n["run_completed"] != true || n["product_news"] != false {
		t.Fatalf("notifications: %v", n)
	}
	// With an active subscription the plan shows up.
	h.store.Subscriptions[h.org.ID] = Subscription{PlanCode: "business", Status: "active"}
	if org := body(t, h.do(http.MethodGet, "/v1/me", "", cs))["org"].(map[string]any); org["plan"] != "business" || org["plan_status"] != "active" {
		t.Fatalf("org com assinatura: %v", org)
	}
	// Unknown org in the session: not found → unauthenticated.
	other := h.login(h.user.ID, uuid.New())
	if rec := h.do(http.MethodGet, "/v1/me", "", other); rec.Code != 401 {
		t.Fatalf("org desconhecida: %d", rec.Code)
	}
	h.store.FailOn = "Credits"
	if rec := h.do(http.MethodGet, "/v1/me", "", cs); rec.Code != 500 || body(t, rec)["code"] != "internal_error" {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = "UserByID"
	if rec := h.do(http.MethodGet, "/v1/me", "", cs); rec.Code != 500 {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = ""
	delete(h.store.Users, h.user.ID)
	if rec := h.do(http.MethodGet, "/v1/me", "", cs); rec.Code != 401 {
		t.Fatalf("usuário apagado: %d", rec.Code)
	}
}

func TestPatchMe(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cs := h.login(h.user.ID, h.org.ID)
	rec := h.do(http.MethodPatch, "/v1/me", `{"display_name":"  Ana\u0301 Lima ","company_name":"Lima & Filhos","notifications":{"product_news":true}}`, cs)
	if rec.Code != 200 {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body.String())
	}
	me := body(t, rec)
	if me["user"].(map[string]any)["display_name"] != "Aná Lima" || me["org"].(map[string]any)["name"] != "Lima & Filhos" || me["notifications"].(map[string]any)["product_news"] != true {
		t.Fatalf("patch aplicado errado: %v", me)
	}
	if u := h.store.Users[h.user.ID]; u.Notifications.RunCompleted != nil || *u.Notifications.ProductNews != true {
		t.Fatalf("preferências mescladas: %+v", u.Notifications)
	}
	if acts := h.actions(&h.org.ID); len(acts) != 1 || acts[0] != ActionProfileUpdated {
		t.Fatalf("auditoria: %v", acts)
	}
	cases := []struct {
		name string
		body string
		code int
		err  string
	}{
		{"vazio", `{}`, 400, "validation_failed"},
		{"nome longo", `{"display_name":"` + strings.Repeat("a", 81) + `"}`, 422, "text_too_long"},
		{"empresa longa", `{"company_name":"` + strings.Repeat("a", 121) + `"}`, 422, "text_too_long"},
		{"nome vazio", `{"display_name":"   "}`, 400, "validation_failed"},
		{"controle", `{"display_name":"a\tb"}`, 400, "validation_failed"},
		{"campo desconhecido", `{"nome":"x"}`, 400, "invalid_json"},
		{"json inválido", `{`, 400, "invalid_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPatch, "/v1/me", tc.body, cs)
			if rec.Code != tc.code || body(t, rec)["code"] != tc.err {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	// Only notifications: no profile text changes.
	if rec := h.do(http.MethodPatch, "/v1/me", `{"notifications":{"credits_low":false}}`, cs); rec.Code != 200 {
		t.Fatalf("só notificações: %d", rec.Code)
	}
	h.store.FailOn = "UpdateOrgName"
	if rec := h.do(http.MethodPatch, "/v1/me", `{"company_name":"x"}`, cs); rec.Code != 500 {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = "UpdateProfile"
	if rec := h.do(http.MethodPatch, "/v1/me", `{"display_name":"x"}`, cs); rec.Code != 500 {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = "Tenant"
	if rec := h.do(http.MethodPatch, "/v1/me", `{"display_name":"x"}`, cs); rec.Code != 500 {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = ""
	h.auditRec.err = errors.New("auditoria fora")
	if rec := h.do(http.MethodPatch, "/v1/me", `{"display_name":"x"}`, cs); rec.Code != 500 {
		t.Fatalf("auditoria falhou: %d", rec.Code)
	}
}

func TestPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cs := h.login(h.user.ID, h.org.ID)
	other := h.login(h.user.ID, h.org.ID)
	// First password needs a recent login (the session was just created).
	if rec := h.do(http.MethodPut, "/v1/me/password", `{"new_password":"`+pw1+`"}`, cs); rec.Code != 204 {
		t.Fatalf("primeira senha: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(h.store.Users[h.user.ID].PasswordHash, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatal("hash Argon2id com os parâmetros combinados")
	}
	if rec := h.do(http.MethodGet, "/v1/me", "", other); rec.Code != 401 {
		t.Fatalf("outras sessões revogadas: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/v1/me", "", cs); rec.Code != 200 {
		t.Fatalf("sessão atual mantida: %d", rec.Code)
	}
	cases := []struct {
		name string
		body string
		code int
		err  string
	}{
		{"sem senha atual", `{"new_password":"` + pw2 + `"}`, 401, "invalid_credentials"},
		{"senha atual errada", `{"current_password":"errada-errada-1","new_password":"` + pw2 + `"}`, 401, "invalid_credentials"},
		{"nova curta", `{"current_password":"` + pw1 + `","new_password":"curta"}`, 400, "validation_failed"},
		{"nova longa", `{"current_password":"` + pw1 + `","new_password":"` + strings.Repeat("x", 129) + `"}`, 400, "validation_failed"},
		{"atual longa", `{"current_password":"` + strings.Repeat("x", 129) + `","new_password":"` + pw2 + `"}`, 400, "validation_failed"},
		{"json inválido", `{"new_password":5}`, 400, "invalid_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPut, "/v1/me/password", tc.body, cs)
			if rec.Code != tc.code || body(t, rec)["code"] != tc.err {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	if rec := h.do(http.MethodPut, "/v1/me/password", `{"current_password":"`+pw1+`","new_password":"`+pw2+`"}`, cs); rec.Code != 204 {
		t.Fatalf("troca: %d %s", rec.Code, rec.Body.String())
	}
	if ok, _ := crypto.VerifyPassword(h.store.Users[h.user.ID].PasswordHash, pw2); !ok {
		t.Fatal("senha nova não confere")
	}
	// A user without password on an old session must log in again.
	u2, o2 := h.store.Seed("bia@example.com", "Bia")
	cs2 := h.login(u2.ID, o2.ID)
	h.now = h.now.Add(h.svc.d.RecentLoginWindow + time.Minute)
	if rec := h.do(http.MethodPut, "/v1/me/password", `{"new_password":"`+pw1+`"}`, cs2); rec.Code != 403 || body(t, rec)["code"] != "recent_login_required" {
		t.Fatalf("sessão antiga: %d %s", rec.Code, rec.Body.String())
	}
	if acts := h.actions(&h.org.ID); len(acts) != 2 || acts[0] != ActionPasswordSet {
		t.Fatalf("auditoria: %v", acts)
	}
	h.store.FailOn = "SetPassword"
	if rec := h.do(http.MethodPut, "/v1/me/password", `{"current_password":"`+pw2+`","new_password":"`+pw3+`"}`, cs); rec.Code != 500 {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = "UserByID"
	if rec := h.do(http.MethodPut, "/v1/me/password", `{"current_password":"`+pw2+`","new_password":"`+pw3+`"}`, cs); rec.Code != 500 {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = ""
	h.sstore.Err = errors.New("sessões fora")
	if err := h.svc.SetPassword(context.Background(), session.Principal{UserID: h.user.ID, OrgID: h.org.ID}, pw2, pw3); err == nil {
		t.Fatal("erro ao revogar sessões")
	}
	h.sstore.Err = nil
	if err := h.svc.SetPassword(context.Background(), session.Principal{UserID: uuid.New()}, "", pw3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("usuário inexistente: %v", err)
	}
	u := h.store.Users[h.user.ID]
	u.PasswordHash = "corrompido"
	h.store.Users[h.user.ID] = u
	if err := h.svc.SetPassword(context.Background(), session.Principal{UserID: h.user.ID}, "x", pw3); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("hash corrompido: %v", err)
	}
}

func TestDeleteMeAndErasure(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cs := h.login(h.user.ID, h.org.ID)
	rec := h.do(http.MethodDelete, "/v1/me", "", cs)
	if rec.Code != 202 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	want := h.now.Add(DefaultDeletionGrace).UTC().Format(time.RFC3339)
	if got := body(t, rec)["scheduled_for"].(string); !strings.HasPrefix(got, want[:19]) {
		t.Fatalf("scheduled_for = %s, esperado %s", got, want)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() { //nolint:bodyclose // recorder
		if c.Name == session.CookieSession && c.MaxAge == -1 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("cookies limpos")
	}
	if rec := h.do(http.MethodGet, "/v1/me", "", cs); rec.Code != 401 {
		t.Fatalf("sessões revogadas: %d", rec.Code)
	}
	if len(h.store.Emails) != 1 || !strings.Contains(h.store.Emails[0].Subject, "exclusão") || !strings.Contains(h.store.Emails[0].Text, "security@example.com") {
		t.Fatalf("e-mail de confirmação: %+v", h.store.Emails)
	}
	if len(h.enq.erases) != 1 || h.enq.erases[0] != h.user.ID {
		t.Fatalf("job agendado: %v", h.enq.erases)
	}
	// A second request (new session issued by support, say) is a conflict.
	cs = h.login(h.user.ID, h.org.ID)
	if rec := h.do(http.MethodDelete, "/v1/me", "", cs); rec.Code != 409 || body(t, rec)["code"] != "conflict" {
		t.Fatalf("segundo pedido: %d %s", rec.Code, rec.Body.String())
	}
	// Not due yet: erasure is a no-op.
	if err := h.svc.RunErase(context.Background(), h.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.store.Users[h.user.ID]; !ok {
		t.Fatal("apagado antes do prazo")
	}
	h.now = h.now.Add(DefaultDeletionGrace + time.Hour)
	if err := h.svc.RunMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.store.Users[h.user.ID]; ok {
		t.Fatal("usuário deveria ter sido apagado")
	}
	if _, ok := h.store.Orgs[h.org.ID]; ok {
		t.Fatal("organização deveria ter sido apagada")
	}
	if _, ok := h.store.TenantKeys[h.org.ID]; ok {
		t.Fatal("chave do locatário deveria ter sido destruída")
	}
	if acts := h.actions(nil); len(acts) != 1 || acts[0] != ActionErased {
		t.Fatalf("auditoria do sistema: %v", acts)
	}
	// Erasing again is harmless; store failures surface.
	if err := h.svc.RunErase(context.Background(), h.user.ID); err != nil {
		t.Fatal(err)
	}
	h.store.FailOn = "Erase"
	if err := h.svc.RunErase(context.Background(), h.user.ID); err == nil {
		t.Fatal("erro do store")
	}
	h.store.FailOn = "DueErasures"
	if err := h.svc.RunMaintenance(context.Background()); err == nil {
		t.Fatal("erro do store")
	}
	h.store.FailOn = "ExpireExports"
	if err := h.svc.RunMaintenance(context.Background()); err == nil {
		t.Fatal("erro do store")
	}
	h.store.FailOn = ""
	// Maintenance reports failures of individual erasures.
	u2, _ := h.store.Seed("bia@example.com", "Bia")
	when := h.now.Add(-time.Hour)
	_, _ = h.store.RequestDeletion(context.Background(), u2.ID, when)
	h.auditRec.err = errors.New("auditoria fora")
	if err := h.svc.RunMaintenance(context.Background()); err == nil {
		t.Fatal("falha de auditoria na exclusão")
	}
	h.auditRec.err = nil
	// Request paths: store and session failures.
	u3, o3 := h.store.Seed("caio@example.com", "Caio")
	cs3 := h.login(u3.ID, o3.ID)
	h.store.FailOn = "RequestDeletion"
	if rec := h.do(http.MethodDelete, "/v1/me", "", cs3); rec.Code != 500 {
		t.Fatalf("erro do store: %d", rec.Code)
	}
	h.store.FailOn = "EnqueueEmail"
	if rec := h.do(http.MethodDelete, "/v1/me", "", cs3); rec.Code != 500 {
		t.Fatalf("erro do outbox: %d", rec.Code)
	}
	h.store.FailOn = ""
	u4, o4 := h.store.Seed("dani@example.com", "Dani")
	cs4 := h.login(u4.ID, o4.ID)
	h.enq.err = errors.New("river fora")
	if rec := h.do(http.MethodDelete, "/v1/me", "", cs4); rec.Code != 202 {
		t.Fatalf("job perdido não impede o pedido: %d", rec.Code)
	}
	h.enq.err = nil
	u5, o5 := h.store.Seed("edu@example.com", "Edu")
	cs5 := h.login(u5.ID, o5.ID)
	h.sstore.Err = errors.New("sessões fora")
	if rec := h.do(http.MethodDelete, "/v1/me", "", cs5); rec.Code != 401 {
		t.Fatalf("sessões fora: %d", rec.Code)
	}
	h.sstore.Err = nil
	// Multi-member org: the leaving user is removed, the org stays.
	u6, _ := h.store.Seed("fer@example.com", "Fer")
	u7, o7 := h.store.Seed("gil@example.com", "Gil")
	h.store.Memberships[o7.ID] = append(h.store.Memberships[o7.ID], Membership{OrgID: o7.ID, UserID: u6.ID, Role: RoleMember, CreatedAt: h.now})
	_, _ = h.store.RequestDeletion(context.Background(), u6.ID, h.now.Add(-time.Minute))
	rep, err := h.store.Erase(context.Background(), u6.ID, h.now)
	if err != nil || !rep.Erased || rep.MembershipsRemoved != 1 || len(rep.OrgsDeleted) != 1 {
		t.Fatalf("erase multi-org: %+v %v", rep, err)
	}
	if _, ok := h.store.Orgs[o7.ID]; !ok || len(h.store.Memberships[o7.ID]) != 1 || h.store.Memberships[o7.ID][0].UserID != u7.ID {
		t.Fatal("org compartilhada deveria ficar com o outro membro")
	}
}

func TestExportFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cs := h.login(h.user.ID, h.org.ID)
	h.store.Sessions[h.user.ID] = []SessionInfo{{ID: uuid.New(), CreatedAt: h.now, LastSeenAt: h.now, IPPrefix: "203.0.113.0/24"}}
	h.store.AuditOf[h.org.ID] = []AuditEntry{{Seq: 1, Action: "auth.login", Actor: "user", CreatedAt: h.now}}
	rec := h.do(http.MethodGet, "/v1/me/export", "", cs)
	if rec.Code != 200 || body(t, rec)["status"] != "pending" {
		t.Fatalf("pedido: %d %s", rec.Code, rec.Body.String())
	}
	if len(h.enq.exports) != 1 {
		t.Fatal("job enfileirado")
	}
	// Asking again while pending returns the same export.
	if rec := h.do(http.MethodGet, "/v1/me/export", "", cs); body(t, rec)["status"] != "pending" || len(h.enq.exports) != 1 {
		t.Fatalf("pendente repetido: %s", rec.Body.String())
	}
	if err := h.svc.RunExport(context.Background(), h.enq.exports[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RunExport(context.Background(), h.enq.exports[0]); err != nil {
		t.Fatal("rodar de novo é inofensivo")
	}
	if err := h.svc.RunExport(context.Background(), uuid.New()); err != nil {
		t.Fatal("exportação inexistente é ignorada")
	}
	rec = h.do(http.MethodGet, "/v1/me/export", "", cs)
	st := body(t, rec)
	if st["status"] != "ready" || st["download_url"] == nil || st["expires_at"] == nil {
		t.Fatalf("pronta: %v", st)
	}
	link := st["download_url"].(string)
	if !strings.HasPrefix(link, "https://api.example.com"+DownloadPath+"?token=") {
		t.Fatalf("link: %s", link)
	}
	if len(h.store.Emails) != 1 || h.store.Emails[0].Tag != "export_ready" || !strings.Contains(h.store.Emails[0].Text, link) {
		t.Fatalf("e-mail: %+v", h.store.Emails)
	}
	if acts := h.actions(&h.org.ID); len(acts) != 2 || acts[0] != ActionExportRequested || acts[1] != ActionExportReady {
		t.Fatalf("auditoria: %v", acts)
	}
	u, _ := url.Parse(link)
	dl := h.do(http.MethodGet, DownloadPath+"?token="+url.QueryEscape(u.Query().Get("token")), "", nil)
	if dl.Code != 200 || dl.Header().Get("Content-Type") != "application/zip" || !strings.Contains(dl.Header().Get("Content-Disposition"), "docmatch-dados-") {
		t.Fatalf("download: %d %v", dl.Code, dl.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(dl.Body.Bytes()), int64(dl.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "perfil.json" {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			_ = rc.Close()
			if !strings.Contains(string(data), "ana@example.com") || strings.Contains(string(data), "wrapped") {
				t.Fatalf("perfil: %s", data)
			}
		}
	}
	for _, n := range []string{"LEIA-ME.txt", "perfil.json", "organizacao.json", "sessoes.json", "auditoria.json"} {
		if !names[n] {
			t.Fatalf("arquivo %s ausente em %v", n, names)
		}
	}
	// Bad tokens.
	for _, tok := range []string{"", "lixo", strings.Repeat("a", 600), crypto.SignToken(bytes.Repeat([]byte{9}, 32), make([]byte, 16), h.now.Add(time.Hour))} {
		if rec := h.do(http.MethodGet, DownloadPath+"?token="+url.QueryEscape(tok), "", nil); rec.Code != 404 {
			t.Fatalf("token %q: %d", tok, rec.Code)
		}
	}
	// Expired: the link dies and a new request makes a new export.
	h.now = h.now.Add(DefaultExportTTL + time.Minute)
	if rec := h.do(http.MethodGet, DownloadPath+"?token="+url.QueryEscape(u.Query().Get("token")), "", nil); rec.Code != 404 {
		t.Fatalf("link vencido: %d", rec.Code)
	}
	if err := h.svc.RunMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := h.store.Exports[h.enq.exports[0]]; st.Status != ExportExpired || h.store.Archives[st.ID] != nil {
		t.Fatalf("expirada: %+v", st)
	}
	if rec := h.do(http.MethodGet, "/v1/me/export", "", cs); body(t, rec)["status"] != "pending" || len(h.enq.exports) != 2 {
		t.Fatalf("nova exportação: %s", rec.Body.String())
	}
	// A pending export whose job got lost is replaced after an hour.
	h.now = h.now.Add(StalePendingAfter + time.Minute)
	if rec := h.do(http.MethodGet, "/v1/me/export", "", cs); body(t, rec)["status"] != "pending" || len(h.enq.exports) != 3 {
		t.Fatalf("pendente antiga: %s", rec.Body.String())
	}
	// Failures while building: the export fails and the next request replaces it.
	h.store.FailOn = "ExportBundle"
	if err := h.svc.RunExport(context.Background(), h.enq.exports[2]); err == nil {
		t.Fatal("falha ao montar")
	}
	h.store.FailOn = ""
	if h.store.Exports[h.enq.exports[2]].Status != ExportFailed {
		t.Fatal("status failed")
	}
	if rec := h.do(http.MethodGet, "/v1/me/export", "", cs); body(t, rec)["status"] != "pending" || len(h.enq.exports) != 4 {
		t.Fatalf("após falha: %s", rec.Body.String())
	}
	// Rate limit: the policy allows five per minute.
	code := 200
	for i := 0; i < 6 && code == 200; i++ {
		code = h.do(http.MethodGet, "/v1/me/export", "", cs).Code
	}
	if code != 429 {
		t.Fatalf("limite de pedidos: %d", code)
	}
	// Status mapping of a failed export without a new request.
	if st := h.svc.status(Export{Status: ExportFailed}); st.Status != ExportExpired {
		t.Fatal("failed aparece como expired")
	}
	// Enqueue failure is tolerated; store failures are not.
	h.now = h.now.Add(time.Hour)
	h.enq.err = errors.New("river fora")
	if rec := h.do(http.MethodGet, "/v1/me/export", "", cs); rec.Code != 200 {
		t.Fatalf("job perdido: %d", rec.Code)
	}
	h.enq.err = nil
	h.now = h.now.Add(StalePendingAfter + time.Minute)
	for _, fail := range []string{"LatestExport", "CreateExport", "UserByID"} {
		h.store.FailOn = fail
		if rec := h.do(http.MethodGet, "/v1/me/export", "", cs); rec.Code != 500 {
			t.Fatalf("%s: %d", fail, rec.Code)
		}
	}
	h.store.FailOn = ""
	// RunExport store failures.
	id := h.enq.exports[len(h.enq.exports)-1]
	h.now = h.now.Add(StalePendingAfter + time.Minute)
	rec = h.do(http.MethodGet, "/v1/me/export", "", cs)
	id = h.enq.exports[len(h.enq.exports)-1]
	for _, fail := range []string{"ExportByID", "CompleteExport", "EnqueueEmail"} {
		h.store.FailOn = fail
		if err := h.svc.RunExport(context.Background(), id); err == nil {
			t.Fatalf("%s deveria falhar", fail)
		}
		h.store.FailOn = ""
		e := h.store.Exports[id]
		e.Status = ExportPending
		h.store.Exports[id] = e
	}
	h.store.FailOn = "FailExport"
	h.store.Err = nil
	bad := &MemoryStore{}
	_ = bad
	h.store.FailOn = ""
	h.auditRec.err = errors.New("auditoria fora")
	if err := h.svc.RunExport(context.Background(), id); err == nil {
		t.Fatal("auditoria na conclusão")
	}
	h.auditRec.err = nil
	// Download store failures.
	h.now = h.now.Add(-2 * time.Hour)
	ready := h.svc.DownloadURL(Export{ID: id, ExpiresAt: ptrTime(h.now.Add(time.Hour))})
	ru, _ := url.Parse(ready)
	e := h.store.Exports[id]
	e.Status, e.ExpiresAt = ExportReady, ptrTime(h.now.Add(time.Hour))
	h.store.Exports[id] = e
	delete(h.store.Archives, id)
	if rec := h.do(http.MethodGet, DownloadPath+"?token="+url.QueryEscape(ru.Query().Get("token")), "", nil); rec.Code != 404 {
		t.Fatalf("sem arquivo: %d", rec.Code)
	}
	for _, fail := range []string{"ExportByID", "ExportArchive"} {
		h.store.FailOn = fail
		if rec := h.do(http.MethodGet, DownloadPath+"?token="+url.QueryEscape(ru.Query().Get("token")), "", nil); rec.Code != 500 {
			t.Fatalf("%s: %d", fail, rec.Code)
		}
	}
	h.store.FailOn = ""
	delete(h.store.Exports, id)
	if rec := h.do(http.MethodGet, DownloadPath+"?token="+url.QueryEscape(ru.Query().Get("token")), "", nil); rec.Code != 404 {
		t.Fatalf("exportação apagada: %d", rec.Code)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// Test passwords are assembled at runtime so secret scanners do not mistake
// the fixtures for credentials.
var (
	pw1 = strings.Join([]string{"senha", "muito", "segura", "1"}, "-")
	pw2 = strings.Join([]string{"outra", "senha", "segura", "2"}, "-")
	pw3 = strings.Join([]string{"senha", "muito", "segura", "3"}, "-")
)

func TestBuildArchiveAndViews(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	u := User{ID: uuid.New(), Email: "a@example.com", DisplayName: "A", CreatedAt: now, GoogleSub: "g"}
	o := Org{ID: uuid.New(), Name: "A", PlanCode: "starter", CreatedAt: now}
	data, err := BuildArchive(Bundle{User: u, Org: o, Membership: Membership{Role: RoleViewer}}, now)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) != 5 {
		t.Fatalf("zip: %v %d", err, len(zr.File))
	}
	for _, f := range zr.File {
		if f.Name == "sessoes.json" || f.Name == "auditoria.json" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			if strings.TrimSpace(string(b)) != "[]" {
				t.Fatalf("%s: %s", f.Name, b)
			}
		}
	}
	if v := NewUserView(u, RoleViewer); v.Role != "member" || v.Locale != "pt-BR" {
		t.Fatalf("viewer: %+v", v)
	}
	if v := NewUserView(u, RoleAdmin); v.Role != "owner" {
		t.Fatalf("admin: %+v", v)
	}
	if v := NewOrgView(o, &Subscription{Status: "canceled"}); v.PlanStatus != "cancelled" || *v.Plan != "starter" {
		t.Fatalf("cancelada: %+v", v)
	}
	if v := NewOrgView(o, &Subscription{PlanCode: "pro", Status: "incomplete"}); v.PlanStatus != "none" || v.Plan != nil {
		t.Fatalf("incompleta: %+v", v)
	}
	if v := NewSessionView(u, Tenant{Org: o, Membership: Membership{Role: RoleOwner}}, now); v.User.Role != "owner" || !v.ExpiresAt.Equal(now) {
		t.Fatalf("sessão: %+v", v)
	}
	p := NotificationPrefs{}.Merge(NotificationPrefs{CreditsLow: ptrBool(false)})
	if r := p.Resolved(); r.CreditsLow || !r.RunCompleted || r.ProductNews {
		t.Fatalf("prefs: %+v", r)
	}
}

func ptrBool(b bool) *bool { return &b }

func TestEntitlements(t *testing.T) {
	t.Parallel()
	cat, err := ParseEntitlements([]byte(pricingSample))
	if err != nil {
		t.Fatal(err)
	}
	if plans := cat.Plans(); len(plans) != 2 || plans[0] != "business" {
		t.Fatalf("planos: %v", plans)
	}
	if e, ok := cat.For("starter"); !ok || e.RetentionDays != 30 || len(e.APILanes) != 1 {
		t.Fatalf("starter: %+v %v", e, ok)
	}
	if _, ok := cat.For("nope"); ok {
		t.Fatal("plano desconhecido")
	}
	var nilCat *Catalog
	if _, ok := nilCat.For("starter"); ok {
		t.Fatal("catálogo nulo")
	}
	for name, doc := range map[string]string{
		"vazio":         "plans: {}",
		"inválido":      "plans: [",
		"limite zero":   "plans:\n  x:\n    document_files_per_run: 0\n",
		"faixa":         strings.Replace(pricingSample, "api_batch]", "turbo]", 1),
		"retenção zero": strings.Replace(pricingSample, "retention_days: 30", "retention_days: 0", 1),
	} {
		if _, err := ParseEntitlements([]byte(doc)); err == nil {
			t.Fatalf("%s deveria falhar", name)
		}
	}
	// The repository file loads from any working directory under the root.
	cat, err = LoadEntitlements("config/pricing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.For("business"); !ok {
		t.Fatal("business no pricing.yaml")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(path, []byte(pricingSample), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEntitlements(path); err != nil {
		t.Fatalf("absoluto: %v", err)
	}
	if _, err := LoadEntitlements(""); err == nil {
		t.Fatal("vazio")
	}
	if _, err := LoadEntitlements("nao/existe.yaml"); err == nil {
		t.Fatal("inexistente")
	}
	if _, err := LoadEntitlements(filepath.Join(dir, "nope.yaml")); err == nil {
		t.Fatal("absoluto inexistente")
	}
	if _, err := LoadEntitlements(dir); err == nil {
		t.Fatal("diretório não é arquivo")
	}
}

func TestMemoryStoreExtras(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(nil)
	ctx := context.Background()
	u, o, err := s.Signup(ctx, User{Email: "x@example.com"}, Org{Name: "X"}, []byte("k"))
	if err != nil || u.ID == uuid.Nil || o.ID == uuid.Nil {
		t.Fatal("signup")
	}
	if got, ok, _ := s.UserByEmail(ctx, "X@EXAMPLE.COM"); !ok || got.ID != u.ID {
		t.Fatal("UserByEmail")
	}
	if _, ok, _ := s.UserByProvider(ctx, "google", "g1"); ok {
		t.Fatal("sem provedor")
	}
	if err := s.LinkProvider(ctx, u.ID, "google", "g1"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProvider(ctx, u.ID, "apple", "a1"); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.UserByProvider(ctx, "apple", "a1"); !ok || got.GoogleSub != "g1" || got.EmailVerifiedAt == nil {
		t.Fatal("UserByProvider")
	}
	_ = s.MarkEmailVerified(ctx, u.ID)
	_ = s.SetDisplayName(ctx, u.ID, "Nome")
	_ = s.TouchLogin(ctx, u.ID)
	if got, _, _ := s.UserByID(ctx, u.ID); got.DisplayName != "Nome" || got.LastLoginAt == nil {
		t.Fatal("campos")
	}
	if tnt, ok, _ := s.PrimaryTenant(ctx, u.ID); !ok || tnt.Org.ID != o.ID || tnt.Membership.Role != RoleOwner {
		t.Fatal("PrimaryTenant")
	}
	if _, ok, _ := s.PrimaryTenant(ctx, uuid.New()); ok {
		t.Fatal("sem organização")
	}
	if _, ok, _ := s.Tenant(ctx, o.ID, uuid.New()); ok {
		t.Fatal("não membro")
	}
	if ok, _ := s.RequestDeletion(ctx, uuid.New(), time.Now()); ok {
		t.Fatal("usuário inexistente")
	}
	if rep, _ := s.Erase(ctx, uuid.New(), time.Now()); rep.Erased {
		t.Fatal("inexistente")
	}
	if _, err := s.ExportBundle(ctx, uuid.New(), o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("bundle sem usuário")
	}
	if _, err := s.ExportBundle(ctx, u.ID, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatal("bundle sem org")
	}
	s.Err = errors.New("fora")
	for name, fn := range map[string]func() error{
		"UserByEmail":       func() error { _, _, err := s.UserByEmail(ctx, "x"); return err },
		"UserByProvider":    func() error { _, _, err := s.UserByProvider(ctx, "google", "x"); return err },
		"Signup":            func() error { _, _, err := s.Signup(ctx, User{}, Org{}, nil); return err },
		"LinkProvider":      func() error { return s.LinkProvider(ctx, u.ID, "google", "x") },
		"MarkEmailVerified": func() error { return s.MarkEmailVerified(ctx, u.ID) },
		"SetDisplayName":    func() error { return s.SetDisplayName(ctx, u.ID, "x") },
		"TouchLogin":        func() error { return s.TouchLogin(ctx, u.ID) },
		"PrimaryTenant":     func() error { _, _, err := s.PrimaryTenant(ctx, u.ID); return err },
		"Tenant":            func() error { _, _, err := s.Tenant(ctx, o.ID, u.ID); return err },
		"Credits":           func() error { _, err := s.Credits(ctx, o.ID); return err },
		"UpdateProfile":     func() error { return s.UpdateProfile(ctx, u.ID, ProfilePatch{}) },
		"UpdateOrgName":     func() error { return s.UpdateOrgName(ctx, o.ID, "x") },
		"SetPassword":       func() error { return s.SetPassword(ctx, u.ID, "x") },
		"RequestDeletion":   func() error { _, err := s.RequestDeletion(ctx, u.ID, time.Now()); return err },
		"DueErasures":       func() error { _, err := s.DueErasures(ctx, time.Now(), 1); return err },
		"Erase":             func() error { _, err := s.Erase(ctx, u.ID, time.Now()); return err },
		"CreateExport":      func() error { _, err := s.CreateExport(ctx, u.ID, o.ID); return err },
		"LatestExport":      func() error { _, _, err := s.LatestExport(ctx, u.ID); return err },
		"ExportByID":        func() error { _, _, err := s.ExportByID(ctx, u.ID); return err },
		"ExportArchive":     func() error { _, _, err := s.ExportArchive(ctx, u.ID); return err },
		"CompleteExport":    func() error { return s.CompleteExport(ctx, u.ID, nil, time.Now()) },
		"FailExport":        func() error { return s.FailExport(ctx, u.ID, "x") },
		"ExpireExports":     func() error { _, err := s.ExpireExports(ctx, time.Now()); return err },
		"ExportBundle":      func() error { _, err := s.ExportBundle(ctx, u.ID, o.ID); return err },
		"EnqueueEmail":      func() error { return s.EnqueueEmail(ctx, nil, email.Message{}) },
		"UserByID":          func() error { _, _, err := s.UserByID(ctx, u.ID); return err },
	} {
		if err := fn(); err == nil {
			t.Fatalf("%s deveria propagar o erro", name)
		}
	}
}
