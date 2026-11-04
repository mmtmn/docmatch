package httpx

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

// statusWriter records the status and byte count written by a handler.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush forwards to the underlying writer (SSE).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer (websockets); unsupported otherwise.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack não suportado")
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Status returns the status written so far (0 before any write).
func (w *statusWriter) Status() int { return w.status }

// HeaderEdgeID is CloudFront's request id; logged (sanitized) for correlation.
const HeaderEdgeID = "X-Amz-Cf-Id"

// sanitizeLogValue keeps a header value safe for logs: printable ASCII only,
// bounded length, so a client cannot inject newlines or control characters.
func sanitizeLogValue(v string, max int) string {
	v = strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, v)
	if len(v) > max {
		v = v[:max]
	}
	return v
}

// LoggingMiddleware writes one structured line per request. It logs method,
// route pattern, status, bytes, duration, request id, client address and
// user agent. It never logs bodies, query strings, cookies or authorization
// headers.
func LoggingMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqLog := log.With(slog.String("request_id", RequestID(r.Context())))
			if edge := r.Header.Get(HeaderEdgeID); edge != "" {
				reqLog = reqLog.With(slog.String("edge_id", sanitizeLogValue(edge, 64)))
			}
			sw := &statusWriter{ResponseWriter: w}
			ctx := logx.WithLogger(r.Context(), reqLog)
			// Keep the derived request: the mux records the matched pattern on it.
			r = r.WithContext(ctx)
			next.ServeHTTP(sw, r)
			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", sanitizeLogValue(r.URL.Path, 256)),
				slog.Int("status", sw.status),
				slog.Int64("bytes", sw.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote", sanitizeLogValue(r.RemoteAddr, 64)),
				slog.String("user_agent", sanitizeLogValue(r.UserAgent(), 128)),
			}
			if r.Pattern != "" {
				attrs = append(attrs, slog.String("route", r.Pattern))
			}
			level := slog.LevelInfo
			if sw.status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			reqLog.LogAttrs(ctx, level, "requisição", attrs...)
		})
	}
}
