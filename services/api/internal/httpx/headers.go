package httpx

import "net/http"

// SecurityHeadersMiddleware sets the response headers every API answer
// carries. The API serves JSON only, so caching is disabled, framing is
// denied and a restrictive CSP neutralizes any accidental HTML rendering.
// Go does not emit a Server header; the middleware also strips one in case a
// handler sets it. HSTS is added only when the deployment terminates TLS
// (prod/staging behind CloudFront).
func SecurityHeadersMiddleware(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
			}
			h.Del("Server")
			next.ServeHTTP(&serverHeaderStripper{ResponseWriter: w}, r)
		})
	}
}

// serverHeaderStripper removes a Server header a handler may have set before
// the headers are flushed.
type serverHeaderStripper struct {
	http.ResponseWriter
}

func (s *serverHeaderStripper) WriteHeader(status int) {
	s.ResponseWriter.Header().Del("Server")
	s.ResponseWriter.WriteHeader(status)
}

func (s *serverHeaderStripper) Write(b []byte) (int, error) {
	s.ResponseWriter.Header().Del("Server")
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer (SSE).
func (s *serverHeaderStripper) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *serverHeaderStripper) Unwrap() http.ResponseWriter { return s.ResponseWriter }
