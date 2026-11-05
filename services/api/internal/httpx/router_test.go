package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
)

// A literal segment next to a wildcard of another verb used to panic at
// start-up in one registration order (the order the API mounts its groups).
func TestLiteralSegmentNextToWildcardInBothOrders(t *testing.T) {
	get := func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte("get:" + req.PathValue("id"))) }
	post := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("estimate")) }
	orders := map[string]func(r *Router){
		"curinga primeiro": func(r *Router) {
			r.HandleFunc(http.MethodGet, "/v1/runs/{id}", BodyNone, get)
			r.HandleFunc(http.MethodPost, "/v1/runs/estimate", BodyJSON, post)
		},
		"literal primeiro": func(r *Router) {
			r.HandleFunc(http.MethodPost, "/v1/runs/estimate", BodyJSON, post)
			r.HandleFunc(http.MethodGet, "/v1/runs/{id}", BodyNone, get)
		},
	}
	for name, register := range orders {
		t.Run(name, func(t *testing.T) {
			r := NewRouter(limits.Must())
			register(r)
			for _, c := range []struct{ method, path, want string }{
				{http.MethodGet, "/v1/runs/run_1", "get:run_1"},
				{http.MethodPost, "/v1/runs/estimate", "estimate"},
			} {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
				req.Header.Set("Content-Type", "application/json")
				r.ServeHTTP(rec, req)
				if rec.Body.String() != c.want {
					t.Fatalf("%s %s = %d %q, quer %q", c.method, c.path, rec.Code, rec.Body.String(), c.want)
				}
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/runs/run_1", nil))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET" {
				t.Fatalf("DELETE em /v1/runs/{id} = %d Allow=%q, quer 405 GET", rec.Code, rec.Header().Get("Allow"))
			}
			rec = httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/nada", nil))
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code"`) {
				t.Fatalf("caminho desconhecido = %d %s, quer 404 em JSON", rec.Code, rec.Body.String())
			}
		})
	}
}
