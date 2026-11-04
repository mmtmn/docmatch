package httpx

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
)

// Router wraps http.ServeMux (Go 1.22 method patterns) so that unmatched
// paths and methods answer with the JSON envelope instead of plain text, and
// so every route declares its body class.
type Router struct {
	mux    *http.ServeMux
	limits *limits.Limits

	mu      sync.Mutex
	methods map[string][]string // path pattern -> registered methods
}

// NewRouter returns an empty router using l for body ceilings.
func NewRouter(l *limits.Limits) *Router {
	r := &Router{mux: http.NewServeMux(), limits: l, methods: map[string][]string{}}
	r.mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		WriteError(w, req, http.StatusNotFound, CodeNotFound)
	})
	return r
}

// Handle registers h for "METHOD /path" with the given body class. Patterns
// without a method are not accepted: every route is explicit about its verb.
func (r *Router) Handle(method, pattern string, class BodyClass, h http.Handler) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || !strings.HasPrefix(pattern, "/") {
		panic("httpx: rota precisa de método e caminho iniciado por /")
	}
	r.mu.Lock()
	first := len(r.methods[pattern]) == 0
	r.methods[pattern] = append(r.methods[pattern], method)
	sort.Strings(r.methods[pattern])
	r.mu.Unlock()

	r.mux.Handle(method+" "+pattern, BodyLimit(r.limits, class)(h))
	if first {
		// A method-less pattern catches the other verbs for the same path.
		// The mux prefers the more specific "METHOD path" entry when it matches.
		r.mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			r.mu.Lock()
			allow := strings.Join(r.methods[pattern], ", ")
			r.mu.Unlock()
			w.Header().Set("Allow", allow)
			WriteError(w, req, http.StatusMethodNotAllowed, CodeMethodNotAllowed)
		})
	}
}

// HandleFunc is Handle for plain functions.
func (r *Router) HandleFunc(method, pattern string, class BodyClass, h http.HandlerFunc) {
	r.Handle(method, pattern, class, h)
}

// ServeHTTP dispatches to the mux.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}
