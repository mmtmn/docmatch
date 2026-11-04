package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

// RecoverMiddleware turns panics into a 500 envelope and logs the stack with
// the request id. http.ErrAbortHandler is re-raised, as net/http expects.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			logx.From(r.Context(), nil).Error("pânico no handler",
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())))
			if headersWritten(w) {
				return // headers already sent; nothing safe to add
			}
			WriteError(w, r, http.StatusInternalServerError, CodeInternal)
		}()
		next.ServeHTTP(w, r)
	})
}

// headersWritten walks the Unwrap chain looking for the statusWriter installed
// by LoggingMiddleware and reports whether the handler already wrote.
func headersWritten(w http.ResponseWriter) bool {
	for w != nil {
		if sw, ok := w.(*statusWriter); ok {
			return sw.wrote
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
	return false
}
