// Package api assembles the public HTTP handler of the API: the route table
// and the middleware stack from httpx. Business endpoints arrive in later
// phases of the plan; this phase ships the operational endpoints.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/version"
)

// Pinger is what readiness needs from the database (pgxpool.Pool satisfies it).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the collaborators of the handler.
type Deps struct {
	Config *config.Config
	Logger *slog.Logger
	Limits *limits.Limits
	// DB answers readiness; nil reports the service as not ready.
	DB Pinger
	// ReadyTimeout bounds the readiness ping (default 2s).
	ReadyTimeout time.Duration
	// Mount lets the feature packages register their route groups.
	Mount []func(*httpx.Router)
	// Middleware runs inside the standard stack, before routing (the
	// session middleware, which puts the principal in the context).
	Middleware []httpx.Middleware
	// OriginExempt marks requests that skip the Origin check (identity
	// provider form posts).
	OriginExempt func(*http.Request) bool
}

// NewHandler builds the full handler chain for the public listener.
func NewHandler(d Deps) http.Handler {
	if d.Limits == nil {
		d.Limits = limits.Must()
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.ReadyTimeout <= 0 {
		d.ReadyTimeout = 2 * time.Second
	}
	var origins []string
	hsts := false
	if d.Config != nil {
		origins = d.Config.AllowedOrigins()
		hsts = !d.Config.IsDev()
	}

	r := httpx.NewRouter(d.Limits)
	r.HandleFunc(http.MethodGet, "/healthz", httpx.BodyNone, healthz)
	r.HandleFunc(http.MethodGet, "/readyz", httpx.BodyNone, readyz(d))
	r.HandleFunc(http.MethodGet, "/v1/version", httpx.BodyNone, versionHandler)
	for _, mount := range d.Mount {
		mount(r)
	}

	mws := httpx.Standard(httpx.StandardOptions{
		Logger:         d.Logger,
		AllowedOrigins: origins,
		HSTS:           hsts,
		OriginExempt:   d.OriginExempt,
	})
	mws = append(mws, d.Middleware...)
	return httpx.Chain(r, mws...)
}

type status struct {
	Status string `json:"status"`
}

// healthz reports liveness: the process is up and serving.
func healthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, status{Status: "ok"})
}

// readyz reports readiness: the database answers a ping within the timeout.
func readyz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			httpx.WriteErrorMessage(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, "Banco de dados não configurado.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), d.ReadyTimeout)
		defer cancel()
		if err := d.DB.Ping(ctx); err != nil {
			logx.From(r.Context(), d.Logger).Warn("readiness falhou", slog.String("err", err.Error()))
			httpx.WriteErrorMessage(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, "Banco de dados indisponível.")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, status{Status: "ok"})
	}
}

// versionHandler exposes the build metadata injected by the Makefile.
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, version.Current())
}
