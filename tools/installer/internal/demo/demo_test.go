package demo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI answers the calls of the site's pipeline like the local API does.
type fakeAPI struct {
	mu        sync.Mutex
	srv       *httptest.Server
	uploads   map[string][]byte
	completes map[string]int
	polls     int
	expireOne bool
	failRun   bool
	origin    string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{uploads: map[string][]byte{}, completes: map[string]int{}}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	authed := func(r *http.Request) bool {
		c, err := r.Cookie("dm_session")
		return err == nil && c.Value == "ok"
	}
	mux.HandleFunc("POST /v1/auth/dev/login", func(w http.ResponseWriter, r *http.Request) {
		f.origin = r.Header.Get("Origin")
		http.SetCookie(w, &http.Cookie{Name: "dm_session", Value: "ok", Path: "/", HttpOnly: true})
		write(w, 200, map[string]any{"user": map[string]any{"email": "dev@docmatch.test"}})
	})
	mux.HandleFunc("POST /v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "dm_session", Value: "ok", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/uploads", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			write(w, 401, map[string]any{"code": "unauthorized"})
			return
		}
		f.mu.Lock()
		expire := f.expireOne
		f.expireOne = false
		f.mu.Unlock()
		if expire {
			// The access window ends once: the client must refresh and retry.
			write(w, 401, map[string]any{"code": "session_expired", "message": "sessão expirada"})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := "upl_" + body["file_name"].(string)[:2]
		write(w, 201, map[string]any{"id": id, "method": "POST", "url": f.srv.URL + "/blob/" + id, "fields": map[string]string{"key": "k/" + id}})
	})
	mux.HandleFunc("POST /blob/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("dm_session"); err == nil {
			http.Error(w, "presigned uploads never carry cookies", http.StatusBadRequest)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("key") == "" {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file", http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(file)
		f.mu.Lock()
		f.uploads[r.PathValue("id")] = data
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/uploads/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.completes[r.PathValue("id")]++
		n := f.completes[r.PathValue("id")]
		f.mu.Unlock()
		status := "scanning"
		if n > 1 {
			status = "ready"
		}
		write(w, 200, map[string]any{"id": r.PathValue("id"), "status": status})
	})
	mux.HandleFunc("POST /v1/runs/estimate", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"id": "q1", "signature": "sig", "expected_credits": 10, "cap_credits": 20})
	})
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Idempotency-Key") == "" || body["quote_signature"] != "sig" || len(body["sources"].([]any)) != 4 {
			write(w, 400, map[string]any{"code": "validation_failed"})
			return
		}
		write(w, 202, map[string]any{"id": "run1", "status": "queued"})
	})
	mux.HandleFunc("GET /v1/runs/run1", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.polls++
		polls, fail := f.polls, f.failRun
		f.mu.Unlock()
		switch {
		case polls < 2:
			write(w, 200, map[string]any{"id": "run1", "status": "running", "progress": map[string]any{"stage": "match", "percent": 40, "message": "conciliando"}})
		case fail:
			write(w, 200, map[string]any{"id": "run1", "status": "failed", "error": map[string]any{"code": "x", "message": "harness indisponível"}})
		default:
			write(w, 200, map[string]any{"id": "run1", "status": "completed", "summary": map[string]any{"findings_total": 3, "amount_at_risk": 5727000, "currency": "BRL"}})
		}
	})
	mux.HandleFunc("GET /v1/runs/run1/findings", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			c := "p2"
			write(w, 200, map[string]any{"items": []any{
				map[string]any{"id": "f1", "title": "Pequeno", "amount_at_risk": 100, "evidence": []any{map[string]any{"label": "Linha 1"}}},
				map[string]any{"id": "f2", "title": "Sem prova", "amount_at_risk": 9999999, "evidence": []any{}},
			}, "next_cursor": c})
			return
		}
		write(w, 200, map[string]any{"items": []any{
			map[string]any{"id": "f3", "title": "Pagamento parcial", "amount_at_risk": 4900000, "currency": "BRL",
				"evidence": []any{map[string]any{"label": "Linha 7 do extrato", "excerpt": "16/07/2026 ·  LIQUIDACAO\n R$ 49.000,00", "locator": map[string]any{"row": 7}}}},
		}, "next_cursor": nil})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestTwin(t *testing.T) {
	f := newFakeAPI(t)
	f.expireOne = true
	c := New(f.srv.URL+"/", "http://localhost:4321")
	c.PollEvery = time.Millisecond
	var phases []string
	res, err := c.Twin(context.Background(), func(p Progress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(phases, ",") != "login,upload,estimate,run,findings" {
		t.Fatalf("fases: %v", phases)
	}
	if f.origin != "http://localhost:4321" {
		t.Fatalf("Origin = %q", f.origin)
	}
	if len(f.uploads) != 4 || len(f.uploads["upl_01"]) == 0 {
		t.Fatalf("envios: %d", len(f.uploads))
	}
	if res.RunID != "run1" || res.Findings != 3 || res.AmountAtRisk != 5727000 || res.Expected != 23 {
		t.Fatalf("%+v", res)
	}
	if res.Top == nil || res.Top.ID != "f3" {
		t.Fatalf("o achado com mais dinheiro e com prova: %+v", res.Top)
	}
	if got := EvidenceLine(res.Top); got != "Linha 7 do extrato: 16/07/2026 · LIQUIDACAO R$ 49.000,00" {
		t.Fatalf("evidência: %q", got)
	}
}

func TestTwinRunFails(t *testing.T) {
	f := newFakeAPI(t)
	f.failRun = true
	c := New(f.srv.URL, "")
	c.PollEvery = time.Millisecond
	_, err := c.Twin(context.Background(), nil)
	if !errors.Is(err, ErrRunFailed) || !strings.Contains(err.Error(), "harness indisponível") {
		t.Fatalf("%v", err)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"rota inexistente"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "").DevLogin(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != "not_found" || !strings.Contains(err.Error(), "rota inexistente") {
		t.Fatalf("%v", err)
	}
}

func TestFormatMoney(t *testing.T) {
	cases := map[int64]string{0: "R$ 0,00", 5: "R$ 0,05", 100: "R$ 1,00", 5727000: "R$ 57.270,00", 18336889: "R$ 183.368,89", 123456789012: "R$ 1.234.567.890,12", -4900: "-R$ 49,00"}
	for in, want := range cases {
		if got := FormatMoney(in, "BRL"); got != want {
			t.Errorf("FormatMoney(%d) = %q; quero %q", in, got, want)
		}
	}
	if got := FormatMoney(100, "USD"); got != "USD 1,00" {
		t.Error(got)
	}
	if Top(nil) != nil || EvidenceLine(nil) != "" {
		t.Error("vazios")
	}
	f := &Finding{Evidence: []Evidence{{Locator: &struct {
		Type string `json:"type"`
		Row  int    `json:"row"`
	}{Row: 3}, Excerpt: "x"}}}
	if EvidenceLine(f) != "linha 3: x" {
		t.Error(EvidenceLine(f))
	}
}
