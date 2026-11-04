package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
)

type payload struct {
	Name string         `json:"name"`
	Tags []string       `json:"tags"`
	Meta map[string]any `json:"meta"`
}

func jsonRequest(t *testing.T, body, contentType string, class BodyClass) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	var out *http.Request
	BodyLimit(limits.Must(), class)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { out = r })).ServeHTTP(rec, req)
	return out
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()
	deep := strings.Repeat("[", MaxJSONDepth+1) + strings.Repeat("]", MaxJSONDepth+1)
	atLimit := strings.Repeat("[", MaxJSONDepth) + strings.Repeat("]", MaxJSONDepth)
	big := `{"name":"` + strings.Repeat("a", 300*1024) + `"}`
	cases := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantCode    string
		wantDetail  string
	}{
		{"válido", `{"name":"Maria","tags":["a"],"meta":{"k":1}}`, "application/json", 0, "", ""},
		{"válido com charset", `{"name":"Zé"}`, "application/json; charset=UTF-8", 0, "", ""},
		{"profundidade no limite", `{"name":"x","meta":{"a":` + atLimit[:len(atLimit)-1] + `]}}`, "application/json", 400, CodeInvalidJSON, "profundidade"},
		{"sem content-type", `{"name":"x"}`, "", 415, CodeUnsupportedMedia, "ausente"},
		{"content-type errado", `{"name":"x"}`, "text/plain", 415, CodeUnsupportedMedia, "text/plain"},
		{"charset errado", `{"name":"x"}`, "application/json; charset=latin1", 415, CodeUnsupportedMedia, "charset"},
		{"content-type inválido", `{"name":"x"}`, "application/", 415, CodeUnsupportedMedia, ""},
		{"corpo vazio", ``, "application/json", 400, CodeInvalidJSON, "vazio"},
		{"só espaços", `   `, "application/json", 400, CodeInvalidJSON, "vazio"},
		{"json quebrado", `{"name":`, "application/json", 400, CodeInvalidJSON, ""},
		{"campo desconhecido", `{"name":"x","extra":1}`, "application/json", 400, CodeInvalidJSON, "extra"},
		{"tipo errado", `{"name":5}`, "application/json", 400, CodeInvalidJSON, ""},
		{"conteúdo extra", `{"name":"x"} {"name":"y"}`, "application/json", 400, CodeInvalidJSON, "extra"},
		{"profundidade excessiva", `{"name":"x","meta":{"a":` + deep + `}}`, "application/json", 400, CodeInvalidJSON, "profundidade"},
		{"caractere de controle escapado", `{"name":"a\u0000b"}`, "application/json", 400, CodeInvalidJSON, "controle"},
		{"bidi override", "{\"name\":\"a\u202eb\"}", "application/json", 400, CodeInvalidJSON, "controle"},
		{"utf-8 inválido", "{\"name\":\"\xff\"}", "application/json", 400, CodeInvalidJSON, "UTF-8"},
		{"corpo acima do limite", big, "application/json", 413, CodePayloadTooLarge, ""},
		{"quebra de linha permitida", `{"name":"linha1\nlinha2\ttab"}`, "application/json", 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := jsonRequest(t, tc.body, tc.contentType, BodyJSON)
			var p payload
			err := DecodeJSON(req, &p)
			if tc.wantStatus == 0 {
				if err != nil {
					t.Fatalf("erro inesperado: %v", err)
				}
				return
			}
			var re *RequestError
			if !errors.As(err, &re) {
				t.Fatalf("esperava RequestError, veio %v", err)
			}
			if re.Status != tc.wantStatus || re.Code != tc.wantCode {
				t.Fatalf("erro = %d %s (%v), esperado %d %s", re.Status, re.Code, re.Err, tc.wantStatus, tc.wantCode)
			}
			if tc.wantDetail != "" && (re.Err == nil || !strings.Contains(re.Err.Error(), tc.wantDetail)) {
				t.Fatalf("detalhe %v não contém %q", re.Err, tc.wantDetail)
			}
			if re.Message == "" {
				t.Fatal("mensagem pt-BR ausente")
			}
		})
	}
}

func TestDecodeJSONNilBody(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = nil
	var re *RequestError
	if err := DecodeJSON(req, &payload{}); !errors.As(err, &re) || re.Status != 400 {
		t.Fatalf("erro = %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	req.Header.Set("Content-Type", "application/json")
	if err := DecodeJSON(req, &payload{}); !errors.As(err, &re) || re.Status != 400 {
		t.Fatalf("erro = %v", err)
	}
}

func TestDecodeJSONReadError(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", failingReader{})
	req.Header.Set("Content-Type", "application/json")
	var re *RequestError
	if err := DecodeJSON(req, &payload{}); !errors.As(err, &re) || re.Status != 400 || re.Code != CodeInvalidJSON {
		t.Fatalf("erro = %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("conexão caiu") }

func TestNormalizeText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"nfc compõe", "é", "é", nil},
		{"crlf vira lf", "a\r\nb\rc", "a\nb\nc", nil},
		{"trim", "  olá \n", "olá", nil},
		{"tab permitido", "a\tb", "a\tb", nil},
		{"nul rejeitado", "a\x00b", "", ErrControlChars},
		{"escape rejeitado", "a\x1bb", "", ErrControlChars},
		{"del rejeitado", "a\x7fb", "", ErrControlChars},
		{"c1 rejeitado", "a\u0085b", "", ErrControlChars},
		{"bidi rejeitado", "a\u2066b", "", ErrControlChars},
		{"marca de direção rejeitada", "a\u200eb", "", ErrControlChars},
		{"utf-8 inválido", "a\xffb", "", ErrInvalidUTF8},
		{"substituição permitida", "a�b", "a�b", nil},
		{"emoji e acentos", "ação 🚀", "ação 🚀", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeText(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("erro = %v, esperado %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got = %q, esperado %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeLineAndValidateText(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeLine("a\nb"); !errors.Is(err, ErrControlChars) {
		t.Fatalf("linha com quebra deveria falhar: %v", err)
	}
	if _, err := NormalizeLine("a\tb"); !errors.Is(err, ErrControlChars) {
		t.Fatalf("linha com tab deveria falhar: %v", err)
	}
	if _, err := NormalizeLine("a\x00b"); !errors.Is(err, ErrControlChars) {
		t.Fatalf("linha com controle deveria falhar: %v", err)
	}
	if got, err := NormalizeLine("  Nome  "); err != nil || got != "Nome" {
		t.Fatalf("NormalizeLine = %q, %v", got, err)
	}
	if got, err := ValidateText("ação", 4, true); err != nil || got != "ação" {
		t.Fatalf("ValidateText conta code points: %q %v", got, err)
	}
	if _, err := ValidateText("ações", 4, true); !errors.Is(err, ErrTooLong) {
		t.Fatalf("esperava ErrTooLong, veio %v", err)
	}
	if _, err := ValidateText("a\nb", 10, true); !errors.Is(err, ErrControlChars) {
		t.Fatalf("esperava ErrControlChars, veio %v", err)
	}
	if got, err := ValidateText("a\r\nb", 10, false); err != nil || got != "a\nb" {
		t.Fatalf("ValidateText multiline = %q %v", got, err)
	}
	if _, err := ValidateText("\xff", 10, false); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("esperava ErrInvalidUTF8, veio %v", err)
	}
	if CountChars("ação") != 4 {
		t.Fatal("CountChars deveria contar code points")
	}
}

func TestBodyClassLimits(t *testing.T) {
	t.Parallel()
	l := limits.Must()
	cases := []struct {
		class BodyClass
		want  int64
		name  string
	}{
		{BodyJSON, l.HTTP.JSONBodyMax(), "json"},
		{BodyDocument, l.Files.DocumentFileMax(), "document"},
		{BodyMedia, l.Files.MediaFileMax(), "media"},
		{BodyNone, 0, "none"},
	}
	for _, tc := range cases {
		if got := tc.class.Limit(l); got != tc.want {
			t.Errorf("%s: Limit = %d, esperado %d", tc.name, got, tc.want)
		}
		if tc.class.String() != tc.name {
			t.Errorf("String = %q, esperado %q", tc.class.String(), tc.name)
		}
	}
	// BodyNone rejects any body; a GET without body passes untouched.
	req := jsonRequest(t, `{}`, "application/json", BodyNone)
	if err := DecodeJSON(req, &payload{}); err == nil {
		t.Fatal("BodyNone deveria recusar corpo")
	}
	rec := httptest.NewRecorder()
	BodyLimit(l, BodyJSON)(okHandler("ok")).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 {
		t.Fatal("GET sem corpo deveria passar")
	}
}
