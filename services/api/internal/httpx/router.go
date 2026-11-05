package httpx

import (
	"net/http"
	"sort"
	"strings"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
)

// probeMethods are the verbs tried when a path matched no route for the
// request's method, to tell 405 (with Allow) from 404.
var probeMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// Router wraps http.ServeMux (Go 1.22 method patterns) so that unmatched
// paths and methods answer with the JSON envelope instead of plain text, and
// so every route declares its body class.
//
// Only "METHOD /path" patterns are registered. A method-less catch-all per
// path (the obvious way to answer 405) conflicts in Go's mux with wildcard
// routes of other verbs (POST /v1/runs/estimate next to GET /v1/runs/{id})
// and panics at start-up, depending on registration order. Instead the root
// handler, reached only when nothing matched, probes the mux with the other
// verbs and answers 405 when one of them has a route for the path.
type Router struct {
	mux    *http.ServeMux
	limits *limits.Limits
}

// NewRouter returns an empty router using l for body ceilings.
func NewRouter(l *limits.Limits) *Router {
	r := &Router{mux: http.NewServeMux(), limits: l}
	r.mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if allow := r.allowedMethods(req); len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			WriteError(w, req, http.StatusMethodNotAllowed, CodeMethodNotAllowed)
			return
		}
		WriteError(w, req, http.StatusNotFound, CodeNotFound)
	})
	return r
}

// allowedMethods lists the verbs that have a route for req's path.
func (r *Router) allowedMethods(req *http.Request) []string {
	var allow []string
	for _, m := range probeMethods {
		if m == req.Method {
			continue
		}
		probe := *req
		probe.Method = m
		if _, pattern := r.mux.Handler(&probe); pattern != "" && pattern != "/" {
			allow = append(allow, m)
		}
	}
	sort.Strings(allow)
	return allow
}

// Handle registers h for "METHOD /path" with the given body class. Patterns
// without a method are not accepted: every route is explicit about its verb.
func (r *Router) Handle(method, pattern string, class BodyClass, h http.Handler) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || !strings.HasPrefix(pattern, "/") {
		panic("httpx: rota precisa de método e caminho iniciado por /")
	}
	r.mux.Handle(method+" "+pattern, BodyLimit(r.limits, class)(h))
}

// HandleFunc is Handle for plain functions.
func (r *Router) HandleFunc(method, pattern string, class BodyClass, h http.HandlerFunc) {
	r.Handle(method, pattern, class, h)
}

// ServeHTTP dispatches to the mux.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}
