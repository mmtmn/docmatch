package apix

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

// Error is a client-facing failure with a stable code from errors.yaml.
// Details are structured data sent to the client; Err is the internal
// cause that only reaches the log line.
type Error struct {
	Code    contracts.ErrorCode
	Details map[string]any
	Err     error
}

// E builds an Error for code with an optional internal cause.
func E(code contracts.ErrorCode, err error) *Error {
	return &Error{Code: code, Err: err}
}

// Ef is E with a formatted internal cause.
func Ef(code contracts.ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)}
}

// With attaches client-visible details.
func (e *Error) With(details map[string]any) *Error {
	e.Details = details
	return e
}

func (e *Error) Error() string {
	if e.Err != nil {
		return string(e.Code) + ": " + e.Err.Error()
	}
	return string(e.Code)
}

// Unwrap exposes the cause to errors.Is/As.
func (e *Error) Unwrap() error { return e.Err }

// Status is the HTTP status of the code.
func (e *Error) Status() int { return e.Code.Status() }

// FieldError is one entry of details.fields in validation_failed.
type FieldError struct {
	Path string `json:"path"`
	Code string `json:"code"`
	Max  int    `json:"max,omitempty"`
}

// Validation builds a validation_failed error listing the offending fields.
func Validation(fields ...FieldError) *Error {
	return &Error{Code: contracts.ErrValidationFailed, Details: map[string]any{"fields": fields}, Err: fmt.Errorf("campos inválidos: %v", fields)}
}

// envelope is the JSON body of every error response (openapi Error schema).
type envelope struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details,omitempty"`
}

// Write sends the envelope for code with the pt-BR message from errors.yaml.
func Write(w http.ResponseWriter, r *http.Request, code contracts.ErrorCode, details map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code.Status())
	_ = json.NewEncoder(w).Encode(envelope{Code: string(code), Message: code.Message(), RequestID: httpx.RequestID(r.Context()), Details: details})
}

// httpxCodes maps the toolkit's request-reading codes to errors.yaml codes.
var httpxCodes = map[string]contracts.ErrorCode{
	httpx.CodeInvalidJSON:      contracts.ErrInvalidJSON,
	httpx.CodeValidation:       contracts.ErrValidationFailed,
	httpx.CodePayloadTooLarge:  contracts.ErrBodyTooLarge,
	httpx.CodeUnsupportedMedia: contracts.ErrUnsupportedMediaType,
	httpx.CodeNotFound:         contracts.ErrNotFound,
	httpx.CodeMethodNotAllowed: contracts.ErrMethodNotAllowed,
	httpx.CodeRateLimited:      contracts.ErrRateLimited,
	httpx.CodeUnavailable:      contracts.ErrServiceUnavailable,
}

// Handle maps any error to a response: *Error keeps its code, a toolkit
// *httpx.RequestError is translated, auth errors become 401/403 and
// anything else is a 500 whose detail goes to the log only.
func Handle(w http.ResponseWriter, r *http.Request, err error) {
	log := logx.From(r.Context(), nil)
	var ae *Error
	if errors.As(err, &ae) {
		if ae.Err != nil {
			log.Warn("requisição recusada", slog.String("code", string(ae.Code)), slog.Int("status", ae.Status()), slog.String("detail", ae.Err.Error()))
		}
		Write(w, r, ae.Code, ae.Details)
		return
	}
	var re *httpx.RequestError
	if errors.As(err, &re) {
		code, ok := httpxCodes[re.Code]
		if !ok {
			code = contracts.ErrValidationFailed
		}
		if re.Err != nil {
			log.Warn("requisição recusada", slog.String("code", string(code)), slog.String("detail", re.Err.Error()))
		}
		Write(w, r, code, nil)
		return
	}
	if code, ok := authCode(err); ok {
		Write(w, r, code, nil)
		return
	}
	log.Error("erro interno", slog.String("detail", err.Error()))
	Write(w, r, contracts.ErrInternalError, nil)
}

// authCode maps Resolver errors to codes.
func authCode(err error) (contracts.ErrorCode, bool) {
	switch {
	case errors.Is(err, ErrSessionExpired):
		return contracts.ErrSessionExpired, true
	case errors.Is(err, ErrInvalidAPIKey):
		return contracts.ErrInvalidAPIKey, true
	case errors.Is(err, ErrScopeDenied):
		return contracts.ErrAPIKeyScopeDenied, true
	case errors.Is(err, ErrForbidden):
		return contracts.ErrForbidden, true
	case errors.Is(err, ErrUnauthenticated):
		return contracts.ErrUnauthenticated, true
	}
	return "", false
}

// WriteJSON sends v with status and the JSON content type.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, v)
}
