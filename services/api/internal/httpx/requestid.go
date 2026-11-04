package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// HeaderRequestID carries the id generated for each request back to clients.
const HeaderRequestID = "X-Request-Id"

type requestIDKey struct{}

// RequestID returns the id stored by the RequestID middleware ("" when absent).
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithRequestID stores id in ctx (tests and background jobs).
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// newRequestID returns 96 random bits as 24 hex characters. Inbound ids are
// never trusted: a client could reuse one to confuse log correlation.
func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand indisponível: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RequestIDMiddleware generates an id per request, exposes it in the response
// header and stores it in the context for logs and error envelopes.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}
