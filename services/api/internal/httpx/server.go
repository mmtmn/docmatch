// Package httpx is the hardened HTTP toolkit of the API: a server whose
// timeouts come from packages/contracts/limits.yaml, middlewares for request
// ids, structured logs, panic recovery, security headers, strict CORS and
// origin checks, per-route body ceilings, strict JSON decoding and a pt-BR
// error envelope.
package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
)

// ServerOptions configure New.
type ServerOptions struct {
	// Addr is the listen address (host:port). ":0" picks a free port (tests).
	Addr string
	// Handler is the full handler chain (see Chain).
	Handler http.Handler
	// Limits sizes every timeout and the header ceiling.
	Limits *limits.Limits
	// Logger receives server-level events (start, stop, errors).
	Logger *slog.Logger
	// ShutdownTimeout bounds graceful shutdown; defaults to the request
	// timeout plus a small margin.
	ShutdownTimeout time.Duration
}

// Server is an http.Server with the timeouts mandated by limits.yaml:
//
//	ReadHeaderTimeout = http.read_header_timeout_s
//	ReadTimeout       = http.request_timeout_s
//	WriteTimeout      = http.request_timeout_s (streams extend it per response)
//	IdleTimeout       = http.sse_idle_timeout_s
//	MaxHeaderBytes    = http.header_max_kb
type Server struct {
	srv             *http.Server
	log             *slog.Logger
	addr            string
	shutdownTimeout time.Duration
	listener        net.Listener
}

// New builds the server; it does not listen yet.
func New(opts ServerOptions) *Server {
	l := opts.Limits
	if l == nil {
		l = limits.Must()
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	shutdown := opts.ShutdownTimeout
	if shutdown <= 0 {
		shutdown = l.HTTP.RequestTimeout() + 5*time.Second
	}
	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           opts.Handler,
		ReadHeaderTimeout: l.HTTP.ReadHeaderTimeout(),
		ReadTimeout:       l.HTTP.RequestTimeout(),
		WriteTimeout:      l.HTTP.RequestTimeout(),
		IdleTimeout:       l.HTTP.SSEIdleTimeout(),
		MaxHeaderBytes:    l.HTTP.HeaderMax(),
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	return &Server{srv: srv, log: log, addr: opts.Addr, shutdownTimeout: shutdown}
}

// HTTPServer exposes the underlying http.Server (tests inspect timeouts).
func (s *Server) HTTPServer() *http.Server { return s.srv }

// Listen binds the address; call it before Serve to learn the port.
func (s *Server) Listen() error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", s.addr)
	if err != nil {
		return err
	}
	s.listener = ln
	return nil
}

// Addr returns the bound address after Listen.
func (s *Server) Addr() string {
	if s.listener == nil {
		return s.addr
	}
	return s.listener.Addr().String()
}

// Serve accepts connections until ctx is cancelled, then drains in-flight
// requests for up to ShutdownTimeout. It returns nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	// Request contexts derive from baseCtx, which outlives ctx: in-flight
	// handlers keep running during the drain and are canceled only when
	// shutdown gives up.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	s.srv.BaseContext = func(net.Listener) context.Context { return baseCtx }
	errc := make(chan error, 1)
	go func() { errc <- s.srv.Serve(s.listener) }()
	s.log.Info("servidor HTTP ouvindo", slog.String("addr", s.Addr()))

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	s.log.Info("encerrando servidor HTTP", slog.Duration("timeout", s.shutdownTimeout))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	s.srv.SetKeepAlivesEnabled(false)
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		s.log.Warn("encerramento forçado", slog.String("err", err.Error()))
		cancelBase()
		_ = s.srv.Close()
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	s.log.Info("servidor HTTP encerrado")
	return nil
}

// ExtendWriteDeadline pushes the response write deadline for streaming
// handlers (SSE) that legitimately outlive WriteTimeout. Call it before each
// event; d is typically limits.HTTP.SSEIdleTimeout().
func ExtendWriteDeadline(w http.ResponseWriter, d time.Duration) error {
	return http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}

// Middleware is a handler decorator.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so the first one listed is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// StandardOptions configure Standard.
type StandardOptions struct {
	Logger         *slog.Logger
	AllowedOrigins []string
	HSTS           bool
}

// Standard is the middleware stack every docmatch listener uses, outermost
// first: request id, access log, panic recovery, security headers, CORS and
// the Origin check for state-changing requests.
func Standard(opts StandardOptions) []Middleware {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return []Middleware{
		RequestIDMiddleware,
		LoggingMiddleware(log),
		RecoverMiddleware,
		SecurityHeadersMiddleware(opts.HSTS),
		CORSMiddleware(DefaultCORSOptions(opts.AllowedOrigins)),
		OriginCheckMiddleware(opts.AllowedOrigins),
	}
}
