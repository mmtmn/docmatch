package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

func okHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

func TestRequestIDMiddleware(t *testing.T) {
	t.Parallel()
	var seen string
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "forjado")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	got := rec.Header().Get(HeaderRequestID)
	if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(got) {
		t.Fatalf("id = %q, esperado 24 hex", got)
	}
	if got != seen {
		t.Fatalf("header %q difere do contexto %q", got, seen)
	}
	if got == "forjado" {
		t.Fatal("id enviado pelo cliente não pode ser reaproveitado")
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Header().Get(HeaderRequestID) == got {
		t.Fatal("ids deveriam ser únicos")
	}
	if RequestID(req.Context()) != "" {
		t.Fatal("contexto sem id deveria devolver vazio")
	}
}

func TestLoggingMiddlewareFields(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		if logx.From(r.Context(), nil) == slog.Default() {
			t.Error("logger de requisição ausente no contexto")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("abc"))
	})
	h := RequestIDMiddleware(LoggingMiddleware(log)(mux))
	req := httptest.NewRequest(http.MethodGet, "/v1/items/42?token=segredo-na-query", nil)
	req.Header.Set("Authorization", "Bearer segredo-bearer")
	req.Header.Set("Cookie", "sessao=segredo-cookie")
	req.Header.Set("User-Agent", "teste/1.0\nX-Injected: 1")
	req.Header.Set(HeaderEdgeID, "cf-\x01id")
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log não é JSON: %s", buf.String())
	}
	if line["method"] != "GET" || line["path"] != "/v1/items/42" || line["status"] != float64(202) || line["bytes"] != float64(3) {
		t.Fatalf("campos inesperados: %v", line)
	}
	if line["route"] != "GET /v1/items/{id}" {
		t.Fatalf("route = %v", line["route"])
	}
	if line["request_id"] != rec.Header().Get(HeaderRequestID) {
		t.Fatalf("request_id = %v", line["request_id"])
	}
	if line["edge_id"] != "cf-?id" || line["user_agent"] != "teste/1.0?X-Injected: 1" {
		t.Fatalf("sanitização falhou: %v", line)
	}
	for _, leak := range []string{"segredo-na-query", "segredo-bearer", "segredo-cookie"} {
		if strings.Contains(buf.String(), leak) {
			t.Fatalf("log vazou %q: %s", leak, buf.String())
		}
	}
	if _, ok := line["duration"]; !ok {
		t.Fatal("duration ausente")
	}
}

func TestLoggingMiddlewareLevels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		level  string
	}{{200, "INFO"}, {404, "INFO"}, {500, "ERROR"}, {503, "ERROR"}} {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		h := LoggingMiddleware(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if !strings.Contains(buf.String(), `"level":"`+tc.level+`"`) {
			t.Fatalf("status %d: nível inesperado: %s", tc.status, buf.String())
		}
	}
	// A handler that writes nothing is logged as 200 with 0 bytes.
	var buf bytes.Buffer
	LoggingMiddleware(slog.New(slog.NewJSONHandler(&buf, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(buf.String(), `"status":0`) {
		t.Fatalf("handler silencioso: %s", buf.String())
	}
}

func TestStatusWriterPassThroughs(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	sw.Flush()
	if !rec.Flushed {
		t.Fatal("Flush não propagou")
	}
	if _, _, err := sw.Hijack(); err == nil {
		t.Fatal("Hijack em recorder deveria falhar")
	}
	if sw.Unwrap() != rec || sw.Status() != 0 {
		t.Fatal("Unwrap/Status incorretos")
	}
	sw.WriteHeader(201)
	sw.WriteHeader(500) // second call must not change the recorded status
	if sw.Status() != 201 {
		t.Fatalf("Status = %d", sw.Status())
	}
	if sanitizeLogValue(strings.Repeat("a", 10)+"é\x7f", 11) != "aaaaaaaaaa?" {
		t.Fatalf("sanitizeLogValue = %q", sanitizeLogValue(strings.Repeat("a", 10)+"é\x7f", 11))
	}
}

func TestRecoverMiddleware(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("explodiu com segredo-interno") })
	h := RequestIDMiddleware(LoggingMiddleware(log)(RecoverMiddleware(boom)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env.Code != CodeInternal || env.RequestID == "" {
		t.Fatalf("envelope = %+v", env)
	}
	if strings.Contains(rec.Body.String(), "segredo-interno") {
		t.Fatal("pânico vazou na resposta")
	}
	if !strings.Contains(buf.String(), "segredo-interno") || !strings.Contains(buf.String(), "stack") {
		t.Fatalf("pânico não registrado com stack: %s", buf.String())
	}
}

func TestRecoverMiddlewareAfterHeadersSent(t *testing.T) {
	t.Parallel()
	partial := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("parcial"))
		panic("tarde demais")
	})
	h := LoggingMiddleware(logx.Discard())(RecoverMiddleware(SecurityHeadersMiddleware(false)(partial)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "parcial" {
		t.Fatalf("resposta parcial alterada: %d %q", rec.Code, rec.Body.String())
	}
}

func TestRecoverMiddlewareRethrowsAbort(t *testing.T) {
	t.Parallel()
	h := RecoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if r := recover(); !errors.Is(r.(error), http.ErrAbortHandler) {
			t.Fatalf("esperava ErrAbortHandler, veio %v", r)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestHeadersWrittenWalksUnwrap(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if headersWritten(rec) {
		t.Fatal("recorder puro não deveria reportar escrita")
	}
	sw := &statusWriter{ResponseWriter: rec}
	wrapped := &serverHeaderStripper{ResponseWriter: sw}
	if headersWritten(wrapped) {
		t.Fatal("nada escrito ainda")
	}
	_, _ = wrapped.Write([]byte("x"))
	if !headersWritten(wrapped) {
		t.Fatal("escrita deveria ser detectada através do Unwrap")
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	leaky := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "vazou/1.0")
		w.WriteHeader(http.StatusOK)
	})
	for _, hsts := range []bool{false, true} {
		rec := httptest.NewRecorder()
		SecurityHeadersMiddleware(hsts)(leaky).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		want := map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"Referrer-Policy":              "no-referrer",
			"Cache-Control":                "no-store",
			"X-Frame-Options":              "DENY",
			"Cross-Origin-Resource-Policy": "same-site",
		}
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s = %q, esperado %q", k, got, v)
			}
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("CSP = %q", rec.Header().Get("Content-Security-Policy"))
		}
		if _, ok := rec.Header()["Server"]; ok {
			t.Error("header Server não foi removido")
		}
		if (rec.Header().Get("Strict-Transport-Security") != "") != hsts {
			t.Errorf("HSTS presente=%v, esperado %v", rec.Header().Get("Strict-Transport-Security") != "", hsts)
		}
	}
	// Writing without WriteHeader also strips Server and flushes.
	rec := httptest.NewRecorder()
	SecurityHeadersMiddleware(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "x")
		_, _ = w.Write([]byte("ok"))
		w.(http.Flusher).Flush()
		if u, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok || u.Unwrap() == nil {
			t.Error("Unwrap ausente")
		}
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if _, ok := rec.Header()["Server"]; ok || !rec.Flushed {
		t.Fatal("Server deveria sumir no Write e Flush propagar")
	}
}

func TestCORSMiddleware(t *testing.T) {
	t.Parallel()
	opts := DefaultCORSOptions([]string{"https://app.example.com", "http://localhost:4321"})
	h := CORSMiddleware(opts)(okHandler("ok"))
	cases := []struct {
		name       string
		method     string
		origin     string
		preflight  bool
		wantStatus int
		wantACAO   string
	}{
		{"sem origin", http.MethodGet, "", false, 200, ""},
		{"origin permitida", http.MethodGet, "https://app.example.com", false, 200, "https://app.example.com"},
		{"origin permitida maiúsculas", http.MethodGet, "HTTPS://APP.EXAMPLE.COM", false, 200, "HTTPS://APP.EXAMPLE.COM"},
		{"origin negada", http.MethodGet, "https://evil.example.net", false, 200, ""},
		{"origin null", http.MethodGet, "null", false, 200, ""},
		{"origin com caminho", http.MethodGet, "https://app.example.com/x", false, 200, ""},
		{"subdomínio não listado", http.MethodGet, "https://sub.app.example.com", false, 200, ""},
		{"preflight permitido", http.MethodOptions, "http://localhost:4321", true, 204, "http://localhost:4321"},
		{"preflight negado", http.MethodOptions, "https://evil.example.net", true, 403, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, "/v1/x", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.preflight {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, esperado %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantACAO {
				t.Fatalf("ACAO = %q, esperado %q", got, tc.wantACAO)
			}
			if tc.origin != "" && !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
				t.Fatal("Vary: Origin ausente")
			}
			if tc.wantACAO != "" {
				if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
					t.Fatal("credenciais deveriam ser permitidas")
				}
				if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), HeaderRequestID) {
					t.Fatal("X-Request-Id deveria ser exposto")
				}
			}
			if tc.preflight && tc.wantStatus == 204 {
				if rec.Header().Get("Access-Control-Allow-Methods") == "" || rec.Header().Get("Access-Control-Allow-Headers") == "" || rec.Header().Get("Access-Control-Max-Age") != "600" {
					t.Fatalf("headers de preflight incompletos: %v", rec.Header())
				}
				if rec.Body.Len() != 0 {
					t.Fatal("preflight não deveria ter corpo")
				}
			}
			if tc.wantStatus == 403 {
				if env := decodeEnvelope(t, rec); env.Code != CodeForbiddenOrigin {
					t.Fatalf("code = %q", env.Code)
				}
			}
		})
	}
}

func TestOriginPolicyNormalization(t *testing.T) {
	t.Parallel()
	p := NewOriginPolicy([]string{" HTTPS://App.Example.com ", "ftp://x", "https://user:pw@h", "null", "", "https://ok.example.com?x=1", "http://localhost:4321"})
	if p.Empty() {
		t.Fatal("política não deveria estar vazia")
	}
	if !p.Allowed("https://app.example.com") || !p.Allowed("http://localhost:4321") {
		t.Fatal("origens válidas deveriam ser aceitas")
	}
	for _, bad := range []string{"ftp://x", "https://user:pw@h", "null", "", "https://ok.example.com", "https://app.example.com:443"} {
		if p.Allowed(bad) {
			t.Errorf("%q não deveria ser aceita", bad)
		}
	}
	if !NewOriginPolicy(nil).Empty() {
		t.Fatal("política vazia")
	}
}

func TestOriginCheckMiddleware(t *testing.T) {
	t.Parallel()
	h := OriginCheckMiddleware([]string{"https://app.example.com"})(okHandler("ok"))
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"GET ignora origem", http.MethodGet, map[string]string{"Origin": "https://evil.example.net"}, 200},
		{"HEAD ignora origem", http.MethodHead, map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"POST origem permitida", http.MethodPost, map[string]string{"Origin": "https://app.example.com"}, 200},
		{"POST origem permitida mesmo com sec-fetch cross-site", http.MethodPost, map[string]string{"Origin": "https://app.example.com", "Sec-Fetch-Site": "cross-site"}, 200},
		{"POST origem negada", http.MethodPost, map[string]string{"Origin": "https://evil.example.net"}, 403},
		{"PUT origem negada", http.MethodPut, map[string]string{"Origin": "null"}, 403},
		{"PATCH sec-fetch cross-site", http.MethodPatch, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"DELETE sec-fetch same-origin", http.MethodDelete, map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"POST referer estranho", http.MethodPost, map[string]string{"Referer": "https://evil.example.net/page"}, 403},
		{"POST referer inválido", http.MethodPost, map[string]string{"Referer": "://quebrado"}, 403},
		{"POST referer permitido", http.MethodPost, map[string]string{"Referer": "https://app.example.com/login"}, 200},
		{"POST sem cabeçalhos (cliente não navegador)", http.MethodPost, nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, "/v1/x", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, esperado %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 403 {
				if env := decodeEnvelope(t, rec); env.Code != CodeForbiddenOrigin {
					t.Fatalf("code = %q", env.Code)
				}
			}
		})
	}
}

func TestChainOrder(t *testing.T) {
	t.Parallel()
	var order []string
	mk := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(okHandler("x"), mk("a"), mk("b"), mk("c"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Join(order, "") != "abc" {
		t.Fatalf("ordem = %v", order)
	}
}

func TestStandardStack(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Chain(okHandler("ok"), Standard(StandardOptions{Logger: log, AllowedOrigins: []string{"https://app.example.com"}, HSTS: true})...)

	req := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get(HeaderRequestID) == "" || rec.Header().Get("Strict-Transport-Security") == "" ||
		rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("pilha padrão incompleta: %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(buf.String(), `"status":200`) {
		t.Fatalf("log ausente: %s", buf.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req.Header.Set("Origin", "https://evil.example.net")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("origem estranha deveria ser bloqueada: %d", rec.Code)
	}
	// Default logger when none is given.
	if len(Standard(StandardOptions{})) != 6 {
		t.Fatal("pilha padrão deveria ter 6 middlewares")
	}
}
