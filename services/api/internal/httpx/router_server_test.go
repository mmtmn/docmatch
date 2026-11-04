package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

func TestRouterEnvelopes(t *testing.T) {
	t.Parallel()
	r := NewRouter(limits.Must())
	r.HandleFunc(http.MethodGet, "/healthz", BodyNone, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.HandleFunc(http.MethodPost, "/v1/items", BodyJSON, func(w http.ResponseWriter, req *http.Request) {
		var p payload
		if err := DecodeJSON(req, &p); err != nil {
			WriteRequestError(w, req, err)
			return
		}
		WriteJSON(w, http.StatusCreated, p)
	})
	r.HandleFunc(http.MethodDelete, "/v1/items", BodyNone, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
		wantAllow  string
	}{
		{"rota existente", http.MethodGet, "/healthz", "", 200, "", ""},
		{"HEAD casa com GET", http.MethodHead, "/healthz", "", 200, "", ""},
		{"não encontrado", http.MethodGet, "/nada", "", 404, CodeNotFound, ""},
		{"raiz não encontrada", http.MethodGet, "/", "", 404, CodeNotFound, ""},
		{"método não permitido", http.MethodPost, "/healthz", "", 405, CodeMethodNotAllowed, "GET"},
		{"método não permitido lista todos", http.MethodPut, "/v1/items", "", 405, CodeMethodNotAllowed, "DELETE, POST"},
		{"post válido", http.MethodPost, "/v1/items", `{"name":"x"}`, 201, "", ""},
		{"post inválido", http.MethodPost, "/v1/items", `{"zzz":1}`, 400, CodeInvalidJSON, ""},
		{"post grande demais", http.MethodPost, "/v1/items", `{"name":"` + strings.Repeat("a", 300*1024) + `"}`, 413, CodePayloadTooLarge, ""},
		{"delete", http.MethodDelete, "/v1/items", "", 204, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			req = req.WithContext(WithRequestID(req.Context(), "rid"))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, esperado %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode != "" {
				env := decodeEnvelope(t, rec)
				if env.Code != tc.wantCode || env.RequestID != "rid" {
					t.Fatalf("envelope = %+v", env)
				}
			}
			if tc.wantAllow != "" && rec.Header().Get("Allow") != tc.wantAllow {
				t.Fatalf("Allow = %q, esperado %q", rec.Header().Get("Allow"), tc.wantAllow)
			}
		})
	}
}

func TestRouterRejectsBadPatterns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ method, pattern string }{{"", "/x"}, {"GET", "x"}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Handle(%q, %q) deveria entrar em pânico", tc.method, tc.pattern)
				}
			}()
			NewRouter(limits.Must()).Handle(tc.method, tc.pattern, BodyNone, okHandler(""))
		}()
	}
}

func TestServerTimeoutsFromLimits(t *testing.T) {
	t.Parallel()
	l := limits.Must()
	s := New(ServerOptions{Addr: "127.0.0.1:0", Handler: okHandler("ok"), Logger: logx.Discard()})
	hs := s.HTTPServer()
	if hs.ReadHeaderTimeout != l.HTTP.ReadHeaderTimeout() || hs.ReadTimeout != l.HTTP.RequestTimeout() ||
		hs.WriteTimeout != l.HTTP.RequestTimeout() || hs.IdleTimeout != l.HTTP.SSEIdleTimeout() || hs.MaxHeaderBytes != l.HTTP.HeaderMax() {
		t.Fatalf("timeouts fora do contrato: %+v", hs)
	}
	if s.shutdownTimeout != l.HTTP.RequestTimeout()+5*time.Second {
		t.Fatalf("shutdown timeout = %v", s.shutdownTimeout)
	}
	if s.Addr() != "127.0.0.1:0" {
		t.Fatalf("Addr antes do Listen = %q", s.Addr())
	}
	custom := New(ServerOptions{Addr: ":0", Limits: l, ShutdownTimeout: time.Second})
	if custom.shutdownTimeout != time.Second || custom.log == nil {
		t.Fatal("opções explícitas ignoradas")
	}
}

func TestServerGracefulShutdown(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		if err := r.Context().Err(); err != nil {
			t.Errorf("contexto da requisição cancelado durante o dreno: %v", err)
		}
		_, _ = w.Write([]byte("terminei"))
	})
	s := New(ServerOptions{Addr: "127.0.0.1:0", Handler: h, Logger: logx.Discard(), ShutdownTimeout: 5 * time.Second})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	type result struct {
		body string
		err  error
	}
	resc := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + s.Addr() + "/slow")
		if err != nil {
			resc <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		resc <- result{body: string(b)}
	}()

	<-started
	cancel() // shutdown begins while the request is in flight
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Serve retornou antes de drenar a requisição em andamento: %v", err)
	default:
	}
	close(release)

	res := <-resc
	if res.err != nil || res.body != "terminei" {
		t.Fatalf("resposta durante o dreno: %q %v", res.body, res.err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve retornou erro: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve não retornou após o dreno")
	}
	// New connections are refused after shutdown.
	if _, err := http.Get("http://" + s.Addr() + "/"); err == nil {
		t.Fatal("servidor deveria estar fechado")
	}
}

func TestServerForcedShutdown(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // only released by the forced shutdown
	})
	s := New(ServerOptions{Addr: "127.0.0.1:0", Handler: h, Logger: logx.Discard(), ShutdownTimeout: 100 * time.Millisecond})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	go func() { _, _ = http.Get("http://" + s.Addr() + "/stuck") }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("esperava erro de encerramento forçado")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve travou no encerramento forçado")
	}
}

func TestServerListenError(t *testing.T) {
	t.Parallel()
	s := New(ServerOptions{Addr: "127.0.0.1:-1", Handler: okHandler("x"), Logger: logx.Discard()})
	if err := s.Serve(context.Background()); err == nil {
		t.Fatal("endereço inválido deveria falhar")
	}
}

func TestServerServeListensWhenNotBound(t *testing.T) {
	t.Parallel()
	s := New(ServerOptions{Addr: "127.0.0.1:0", Handler: okHandler("x"), Logger: logx.Discard()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExtendWriteDeadline(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := ExtendWriteDeadline(w, time.Minute); err != nil {
			t.Errorf("ExtendWriteDeadline: %v", err)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if err := ExtendWriteDeadline(httptest.NewRecorder(), time.Second); err == nil {
		t.Fatal("recorder não suporta deadline; esperava erro")
	}
}
