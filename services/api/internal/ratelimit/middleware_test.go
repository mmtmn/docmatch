package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

func TestMiddlewareHeadersAnd429(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	m := NewMemory(WithClock(clock.Now))
	policy := Policy{Limit: 2, Period: time.Minute}
	keyer := func(r *http.Request) (string, Policy, bool) {
		if r.URL.Path == "/livre" {
			return "", Policy{}, false
		}
		return "k:" + r.Header.Get("X-Client"), policy, true
	}
	h := Middleware(m, keyer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	do := func(path, client string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Client", client)
		req = req.WithContext(httpx.WithRequestID(req.Context(), "rid"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do("/v1/x", "a")
	if rec.Code != 200 || rec.Header().Get(HeaderLimit) != "2" || rec.Header().Get(HeaderRemaining) != "1" ||
		rec.Header().Get(HeaderPolicy) != "2;w=60" || rec.Header().Get(HeaderReset) != "30" {
		t.Fatalf("primeira resposta: %d %v", rec.Code, rec.Header())
	}
	rec = do("/v1/x", "a")
	if rec.Code != 200 || rec.Header().Get(HeaderRemaining) != "0" || rec.Header().Get(HeaderReset) != "60" {
		t.Fatalf("segunda resposta: %d %v", rec.Code, rec.Header())
	}
	rec = do("/v1/x", "a")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("terceira deveria ser 429: %d", rec.Code)
	}
	if rec.Header().Get(HeaderRetry) != "30" || rec.Header().Get(HeaderRemaining) != "0" {
		t.Fatalf("headers do 429: %v", rec.Header())
	}
	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != httpx.CodeRateLimited || env.RequestID != "rid" || env.Message == "" {
		t.Fatalf("envelope = %+v (%v)", env, err)
	}
	// Other clients are unaffected; unlimited routes carry no headers.
	if rec = do("/v1/x", "b"); rec.Code != 200 {
		t.Fatal("cliente b deveria passar")
	}
	if rec = do("/livre", "a"); rec.Code != 200 || rec.Header().Get(HeaderLimit) != "" {
		t.Fatalf("rota livre: %d %v", rec.Code, rec.Header())
	}
	// Retry-After rounds up and never reports 0 for a positive wait.
	clock.Advance(29*time.Second + 500*time.Millisecond)
	if rec = do("/v1/x", "a"); rec.Code != 429 || rec.Header().Get(HeaderRetry) != "1" {
		t.Fatalf("Retry-After deveria arredondar para cima: %d %v", rec.Code, rec.Header())
	}
}

func TestMiddlewareFailsOpenOnLimiterError(t *testing.T) {
	t.Parallel()
	h := Middleware(failingLimiter{}, func(*http.Request) (string, Policy, bool) {
		return "k", Policy{Limit: 1, Period: time.Minute}, true
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || rec.Header().Get(HeaderLimit) != "" {
		t.Fatalf("limitador quebrado deveria deixar passar sem headers: %d %v", rec.Code, rec.Header())
	}
}

func TestCeilSeconds(t *testing.T) {
	t.Parallel()
	cases := map[time.Duration]int{0: 0, -time.Second: 0, time.Millisecond: 1, time.Second: 1, 1500 * time.Millisecond: 2, 61 * time.Second: 61}
	for d, want := range cases {
		if got := ceilSeconds(d); got != want {
			t.Errorf("ceilSeconds(%v) = %d, esperado %d", d, got, want)
		}
	}
}
