package apix

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
	"pgregory.net/rapid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

func TestIdentRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		var id uuid.UUID
		copy(id[:], rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(rt, "uuid"))
		s := Encode(PrefixRun, id)
		if !strings.HasPrefix(s, "run_") || len(s) != 30 {
			rt.Fatalf("formato inesperado: %s", s)
		}
		for _, c := range s[4:] {
			if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
				rt.Fatalf("caractere fora do alfabeto: %q", c)
			}
		}
		back, err := Decode(PrefixRun, s)
		if err != nil || back != id {
			rt.Fatalf("decode(%s) = %s, %v; esperado %s", s, back, err, id)
		}
	})
}

func TestIdentOrdering(t *testing.T) {
	t.Parallel()
	a, _ := uuid.NewV7()
	time.Sleep(2 * time.Millisecond)
	b, _ := uuid.NewV7()
	if Encode(PrefixUpload, a) >= Encode(PrefixUpload, b) {
		t.Fatal("uuidv7 mais novo deveria ordenar depois")
	}
}

func TestDecodeRejects(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	ok := Encode(PrefixRun, id)
	cases := []string{"", "run_", "upl_" + ok[4:], ok[:29], ok + "A", "run_" + strings.Repeat("Z", 26), "run_" + strings.Repeat("U", 26), "RUN_" + ok[4:]}
	for _, c := range cases {
		if _, err := Decode(PrefixRun, c); err == nil {
			t.Errorf("Decode(%q) deveria falhar", c)
		}
	}
	if _, err := Decode(PrefixRun, strings.ToLower(ok)); err != nil {
		t.Errorf("minúsculas deveriam decodificar: %v", err)
	}
	if got := EncodePtr(PrefixRun, nil); got != nil {
		t.Fatal("EncodePtr(nil) deveria ser nil")
	}
	if got := EncodePtr(PrefixRun, &id); got == nil || *got != ok {
		t.Fatal("EncodePtr deveria codificar")
	}
}

func TestPrincipalActor(t *testing.T) {
	t.Parallel()
	user, key := uuid.New(), uuid.New()
	if a := (Principal{UserID: &user}).Actor(); a.Type != audit.ActorUser || !strings.HasPrefix(a.ID, "usr_") {
		t.Fatalf("ator de usuário: %+v", a)
	}
	if a := (Principal{APIKeyID: &key}).Actor(); a.Type != audit.ActorAPIKey || !strings.HasPrefix(a.ID, "key_") {
		t.Fatalf("ator de chave: %+v", a)
	}
	if a := (Principal{}).Actor(); a.Type != audit.ActorSystem {
		t.Fatalf("ator anônimo: %+v", a)
	}
	if _, err := Unauthenticated()(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("Unauthenticated deveria recusar")
	}
	p, err := Static(Principal{Plan: "pro"})(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || p.Plan != "pro" {
		t.Fatal("Static deveria devolver o principal")
	}
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope inválido: %v (%s)", err, rec.Body.String())
	}
	return env
}

func TestHandleMapsErrors(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(httpx.WithRequestID(req.Context(), "req-1"))
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"apix", E(contracts.ErrInsufficientCredits, errors.New("saldo")).With(map[string]any{"required": 10}), 402, "insufficient_credits"},
		{"validation", Validation(FieldError{Path: "/x", Code: "max", Max: 3}), 400, "validation_failed"},
		{"httpx", httpx.NewRequestError(http.StatusRequestEntityTooLarge, httpx.CodePayloadTooLarge, errors.New("grande")), 413, "body_too_large"},
		{"httpx desconhecido", httpx.NewRequestError(http.StatusBadRequest, "outro", nil), 400, "validation_failed"},
		{"sessão expirada", ErrSessionExpired, 401, "session_expired"},
		{"chave inválida", ErrInvalidAPIKey, 401, "invalid_api_key"},
		{"escopo", ErrScopeDenied, 403, "api_key_scope_denied"},
		{"proibido", ErrForbidden, 403, "forbidden"},
		{"anônimo", ErrUnauthenticated, 401, "unauthenticated"},
		{"interno", errors.New("boom"), 500, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Handle(rec, req, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, esperado %d", rec.Code, tc.status)
			}
			env := decodeEnvelope(t, rec)
			if env.Code != tc.code || env.RequestID != "req-1" || env.Message == "" {
				t.Fatalf("envelope = %+v", env)
			}
			if tc.name == "apix" && env.Details["required"] != float64(10) {
				t.Fatalf("details = %v", env.Details)
			}
			if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "saldo") {
				t.Fatal("detalhe interno vazou para a resposta")
			}
		})
	}
	ae := Ef(contracts.ErrRunNotFound, "run %d", 7)
	if ae.Status() != 404 || !strings.Contains(ae.Error(), "run 7") || ae.Unwrap() == nil {
		t.Fatalf("Ef: %v", ae)
	}
	if (&Error{Code: contracts.ErrConflict}).Error() != "conflict" {
		t.Fatal("Error sem causa")
	}
}

func TestParsePage(t *testing.T) {
	t.Parallel()
	p, err := ParsePage(httptest.NewRequest(http.MethodGet, "/?limit=10", nil))
	if err != nil || p.Limit != 10 || p.Offset != 0 {
		t.Fatalf("página = %+v, %v", p, err)
	}
	next := EncodeCursor(p, 10)
	if next == nil {
		t.Fatal("página cheia deveria ter cursor")
	}
	if EncodeCursor(p, 3) != nil {
		t.Fatal("página incompleta não deveria ter cursor")
	}
	p2, err := ParsePage(httptest.NewRequest(http.MethodGet, "/?limit=10&cursor="+*next, nil))
	if err != nil || p2.Offset != 10 {
		t.Fatalf("página 2 = %+v, %v", p2, err)
	}
	for _, bad := range []string{"/?limit=0", "/?limit=101", "/?limit=x", "/?cursor=!!!", "/?cursor=bm9wZQ", "/?cursor=bzotMQ", "/?cursor=" + strings.Repeat("a", 200)} {
		if _, err := ParsePage(httptest.NewRequest(http.MethodGet, bad, nil)); err == nil {
			t.Errorf("%s deveria falhar", bad)
		}
	}
	if _, err := ParsePage(httptest.NewRequest(http.MethodGet, "/", nil)); err != nil {
		t.Fatal(err)
	}
}

func TestIdempotent(t *testing.T) {
	t.Parallel()
	store := NewMemIdemStore()
	org := uuid.New()
	now := time.Now()
	calls := 0
	fn := func() (Result, error) {
		calls++
		return Result{Status: 202, Body: map[string]any{"n": calls}}, nil
	}
	hash := []byte("h1")
	first, err := Idempotent(context.Background(), store, org, "k", hash, now, fn)
	if err != nil || first.Status != 202 {
		t.Fatalf("primeira = %+v, %v", first, err)
	}
	second, err := Idempotent(context.Background(), store, org, "k", hash, now, fn)
	if err != nil || second.Status != 202 || string(second.Body.(json.RawMessage)) != string(first.Body.(json.RawMessage)) || calls != 1 {
		t.Fatalf("replay = %+v, %v (calls=%d)", second, err, calls)
	}
	_, err = Idempotent(context.Background(), store, org, "k", []byte("h2"), now, fn)
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != contracts.ErrIdempotencyKeyReused {
		t.Fatalf("hash diferente = %v", err)
	}
	// Another organization may reuse the key.
	if _, err := Idempotent(context.Background(), store, uuid.New(), "k", hash, now, fn); err != nil || calls != 2 {
		t.Fatalf("outra org = %v (calls=%d)", err, calls)
	}
	// In progress: begin without finishing.
	if _, _, err := store.BeginIdem(context.Background(), org, "busy", hash, now); err != nil {
		t.Fatal(err)
	}
	_, err = Idempotent(context.Background(), store, org, "busy", hash, now, fn)
	if !errors.As(err, &ae) || ae.Code != contracts.ErrIdempotencyInProgress {
		t.Fatalf("em andamento = %v", err)
	}
	// A failing operation forgets the key so the client can retry.
	boom := errors.New("boom")
	if _, err := Idempotent(context.Background(), store, org, "fail", hash, now, func() (Result, error) { return Result{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("falha = %v", err)
	}
	if res, err := Idempotent(context.Background(), store, org, "fail", hash, now, fn); err != nil || res.Status != 202 {
		t.Fatalf("retry após falha = %+v, %v", res, err)
	}
}

func TestIdemKeyHeader(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader("{}"))
	if _, err := IdemKey(r); err == nil || err.Code != contracts.ErrMissingIdempotencyKey {
		t.Fatalf("sem header = %v", err)
	}
	r.Header.Set(HeaderIdempotencyKey, strings.Repeat("k", 129))
	if _, err := IdemKey(r); err == nil || err.Code != contracts.ErrValidationFailed {
		t.Fatalf("longa = %v", err)
	}
	r.Header.Set(HeaderIdempotencyKey, "ok\x01")
	if _, err := IdemKey(r); err == nil {
		t.Fatal("controle deveria falhar")
	}
	r.Header.Set(HeaderIdempotencyKey, " abc ")
	key, err := IdemKey(r)
	if err != nil || key != "abc" {
		t.Fatalf("chave = %q, %v", key, err)
	}
	h1 := IdemHash(r, []byte(`{"a":1}`))
	h2 := IdemHash(r, []byte(`{"a":2}`))
	if string(h1) == string(h2) || len(h1) != 32 {
		t.Fatal("hash deveria depender do corpo")
	}
}
