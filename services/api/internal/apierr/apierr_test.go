package apierr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("corpo não é JSON: %s", rec.Body.String())
	}
	return env
}

func TestWrite(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(httpx.WithRequestID(req.Context(), "req1"))
	rec := httptest.NewRecorder()
	Write(rec, req, contracts.ErrUnauthenticated)
	env := decode(t, rec)
	if rec.Code != 401 || env.Code != "unauthenticated" || env.Message != contracts.ErrUnauthenticated.Message() || env.RequestID != "req1" || env.Details != nil {
		t.Fatalf("%d %+v", rec.Code, env)
	}
	if rec.Header().Get("Content-Type") != "application/json; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", rec.Header())
	}
	rec = httptest.NewRecorder()
	Write(rec, req, contracts.ErrorCode("nao_existe"))
	if env := decode(t, rec); rec.Code != 500 || env.Code != "internal_error" {
		t.Fatalf("código desconhecido vira erro interno: %d %+v", rec.Code, env)
	}
}

func TestValidationAndInternal(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	Validation(rec, req, Field{Path: "/email", Code: FieldTooLong, Max: 254})
	env := decode(t, rec)
	fields := env.Details.(map[string]any)["fields"].([]any)
	if rec.Code != 400 || env.Code != "validation_failed" || len(fields) != 1 || fields[0].(map[string]any)["max"] != float64(254) {
		t.Fatalf("%d %+v", rec.Code, env)
	}
	rec = httptest.NewRecorder()
	Internal(rec, req, errors.New("detalhe secreto"))
	if env := decode(t, rec); rec.Code != 500 || env.Code != "internal_error" || rec.Body.String() == "" || containsSecret(rec.Body.String()) {
		t.Fatalf("%d %+v", rec.Code, env)
	}
}

func containsSecret(s string) bool {
	return len(s) > 0 && (s == "detalhe secreto" || contains(s, "detalhe secreto"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestFromRequestError(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	cases := map[string]struct {
		err    error
		status int
		code   string
	}{
		"json":      {httpx.NewRequestError(400, httpx.CodeInvalidJSON, errors.New("x")), 400, "invalid_json"},
		"grande":    {httpx.NewRequestError(413, httpx.CodePayloadTooLarge, nil), 413, "body_too_large"},
		"tipo":      {httpx.NewRequestError(415, httpx.CodeUnsupportedMedia, nil), 415, "unsupported_media_type"},
		"validacao": {httpx.NewRequestError(400, httpx.CodeValidation, nil), 400, "validation_failed"},
		"outro":     {httpx.NewRequestError(404, httpx.CodeNotFound, nil), 500, "internal_error"},
		"generico":  {errors.New("boom"), 500, "internal_error"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			FromRequestError(rec, req, tc.err)
			if env := decode(t, rec); rec.Code != tc.status || env.Code != tc.code {
				t.Fatalf("%d %+v", rec.Code, env)
			}
		})
	}
}
