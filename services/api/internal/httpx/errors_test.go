package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("corpo não é um envelope: %s (%v)", rec.Body.String(), err)
	}
	return env
}

func TestWriteErrorEnvelope(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(WithRequestID(req.Context(), "abc123"))
	rec := httptest.NewRecorder()
	WriteError(rec, req, http.StatusNotFound, CodeNotFound)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env.Code != CodeNotFound || env.Message != Messages[CodeNotFound] || env.RequestID != "abc123" {
		t.Fatalf("envelope = %+v", env)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff ausente")
	}
	// Only the three documented keys are present.
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if len(raw) != 3 {
		t.Fatalf("envelope com chaves extras: %v", raw)
	}
}

func TestWriteErrorMessageFallsBackToDefault(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	WriteErrorMessage(rec, req, http.StatusBadRequest, CodeValidation, "")
	if env := decodeEnvelope(t, rec); env.Message != Messages[CodeValidation] {
		t.Fatalf("mensagem = %q", env.Message)
	}
	rec = httptest.NewRecorder()
	WriteErrorMessage(rec, req, http.StatusBadRequest, CodeValidation, "O campo nome é obrigatório.")
	if env := decodeEnvelope(t, rec); env.Message != "O campo nome é obrigatório." {
		t.Fatalf("mensagem = %q", env.Message)
	}
}

func TestEveryCodeHasMessage(t *testing.T) {
	t.Parallel()
	for _, c := range []string{CodeInvalidJSON, CodeValidation, CodePayloadTooLarge, CodeUnsupportedMedia, CodeNotFound,
		CodeMethodNotAllowed, CodeRateLimited, CodeForbiddenOrigin, CodeUnavailable, CodeInternal} {
		if Messages[c] == "" {
			t.Errorf("código %s sem mensagem", c)
		}
	}
}

func TestWriteRequestErrorKeepsDetailsOutOfResponse(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req = req.WithContext(logx.WithLogger(req.Context(), log))

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantLogged string
	}{
		{"request error", NewRequestError(http.StatusBadRequest, CodeInvalidJSON, errors.New("detalhe interno 42")), http.StatusBadRequest, CodeInvalidJSON, "detalhe interno 42"},
		{"wrapped request error", errors.Join(errors.New("ctx"), &RequestError{Status: 413, Code: CodePayloadTooLarge, Message: "x", Err: errors.New("limite 7")}), 413, CodePayloadTooLarge, "limite 7"},
		{"request error sem detalhe", &RequestError{Status: 422, Code: CodeValidation, Message: "m"}, 422, CodeValidation, ""},
		{"erro genérico", errors.New("falha de banco secreta"), http.StatusInternalServerError, CodeInternal, "falha de banco secreta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			rec := httptest.NewRecorder()
			WriteRequestError(rec, req, tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, esperado %d", rec.Code, tc.wantStatus)
			}
			env := decodeEnvelope(t, rec)
			if env.Code != tc.wantCode {
				t.Fatalf("code = %q", env.Code)
			}
			if tc.wantLogged != "" {
				if strings.Contains(rec.Body.String(), tc.wantLogged) {
					t.Fatalf("detalhe vazou na resposta: %s", rec.Body.String())
				}
				if !strings.Contains(buf.String(), tc.wantLogged) {
					t.Fatalf("detalhe não foi registrado: %s", buf.String())
				}
			}
		})
	}
}

func TestRequestErrorErrorAndUnwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("raiz")
	re := NewRequestError(400, CodeInvalidJSON, inner)
	if !errors.Is(re, inner) || !strings.Contains(re.Error(), "raiz") {
		t.Fatalf("Unwrap/Error incorretos: %v", re)
	}
	if (&RequestError{Code: CodeValidation}).Error() != CodeValidation {
		t.Fatal("Error sem detalhe deveria devolver o código")
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]int{"n": 1})
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != `{"n":1}` {
		t.Fatalf("resposta = %d %s", rec.Code, rec.Body.String())
	}
}
