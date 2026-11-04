package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/version"
)

type fakeDB struct {
	err   error
	delay time.Duration
}

func (f fakeDB) Ping(ctx context.Context) error {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

func testConfig(env string) *config.Config {
	pub, _ := url.Parse("https://app.example.com")
	api, _ := url.Parse("https://api.example.com")
	return &config.Config{Env: env, PublicBaseURL: pub, APIBaseURL: api}
}

func TestEndpoints(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		method     string
		path       string
		db         Pinger
		wantStatus int
		wantBody   map[string]any
		wantCode   string
	}{
		{"healthz", http.MethodGet, "/healthz", fakeDB{}, 200, map[string]any{"status": "ok"}, ""},
		{"healthz ignora banco", http.MethodGet, "/healthz", fakeDB{err: errors.New("fora")}, 200, map[string]any{"status": "ok"}, ""},
		{"readyz ok", http.MethodGet, "/readyz", fakeDB{}, 200, map[string]any{"status": "ok"}, ""},
		{"readyz banco fora", http.MethodGet, "/readyz", fakeDB{err: errors.New("conexão recusada")}, 503, nil, httpx.CodeUnavailable},
		{"readyz banco lento", http.MethodGet, "/readyz", fakeDB{delay: time.Second}, 503, nil, httpx.CodeUnavailable},
		{"readyz sem banco", http.MethodGet, "/readyz", nil, 503, nil, httpx.CodeUnavailable},
		{"version", http.MethodGet, "/v1/version", fakeDB{}, 200, map[string]any{"version": version.Version, "git_sha": version.GitSHA}, ""},
		{"rota inexistente", http.MethodGet, "/v1/nada", fakeDB{}, 404, nil, httpx.CodeNotFound},
		{"método errado", http.MethodPost, "/healthz", fakeDB{}, 405, nil, httpx.CodeMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewHandler(Deps{Config: testConfig("prod"), Logger: logx.Discard(), DB: tc.db, ReadyTimeout: 50 * time.Millisecond})
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, esperado %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("corpo não é JSON: %s", rec.Body.String())
			}
			for k, v := range tc.wantBody {
				if body[k] != v {
					t.Fatalf("%s = %v, esperado %v", k, body[k], v)
				}
			}
			if tc.wantCode != "" {
				if body["code"] != tc.wantCode || body["request_id"] == "" {
					t.Fatalf("envelope inesperado: %v", body)
				}
				if msg, _ := body["message"].(string); msg == "" {
					t.Fatal("mensagem ausente")
				}
			}
			if rec.Header().Get(httpx.HeaderRequestID) == "" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("pilha de middlewares ausente: %v", rec.Header())
			}
			if _, ok := rec.Header()["Server"]; ok {
				t.Fatal("header Server presente")
			}
			if rec.Header().Get("Strict-Transport-Security") == "" {
				t.Fatal("HSTS deveria estar ligado fora de dev")
			}
		})
	}
}

func TestDevHasNoHSTSAndDefaults(t *testing.T) {
	t.Parallel()
	h := NewHandler(Deps{Config: testConfig("dev"), DB: fakeDB{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 || rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("dev: %d %v", rec.Code, rec.Header())
	}
	// Without config the handler still serves (no origins allowed, no HSTS).
	h = NewHandler(Deps{DB: fakeDB{}})
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("sem config nenhuma origem deveria ser aceita em POST: %d", rec.Code)
	}
}

func TestCORSUsesConfiguredOrigins(t *testing.T) {
	t.Parallel()
	h := NewHandler(Deps{Config: testConfig("prod"), Logger: logx.Discard(), DB: fakeDB{}})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("CORS não refletiu a origem configurada: %v", rec.Header())
	}
	req.Header.Set("Origin", "https://outro.example.net")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("origem desconhecida não pode receber CORS")
	}
}
