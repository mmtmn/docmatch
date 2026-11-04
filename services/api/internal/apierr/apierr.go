// Package apierr writes the error envelope of the public API with the codes
// and pt-BR messages of packages/contracts/errors.yaml (the generated
// contracts package): {code, message, request_id, details?}. The httpx
// package has its own envelope for transport-level failures (malformed JSON,
// oversized bodies); FromRequestError maps those onto catalog codes so every
// handler built on this package answers with catalog codes only.
package apierr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

// Envelope is the JSON body of an error response.
type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

// Field is one entry of details.fields in a validation_failed response.
type Field struct {
	Path string `json:"path"`
	Code string `json:"code"`
	Max  int    `json:"max,omitempty"`
	Min  int    `json:"min,omitempty"`
}

// Field codes used in details.fields.
const (
	FieldRequired = "required"
	FieldInvalid  = "invalid"
	FieldTooLong  = "text_too_long"
	FieldTooShort = "text_too_short"
)

// Write answers with the catalog code, its status and message.
func Write(w http.ResponseWriter, r *http.Request, code contracts.ErrorCode) {
	WriteDetails(w, r, code, nil)
}

// WriteDetails is Write with a details object.
func WriteDetails(w http.ResponseWriter, r *http.Request, code contracts.ErrorCode, details any) {
	if !code.Known() {
		code = contracts.ErrInternalError
	}
	env := Envelope{Code: string(code), Message: code.Message(), RequestID: httpx.RequestID(r.Context()), Details: details}
	status := code.Status()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// Validation answers validation_failed listing the offending fields.
func Validation(w http.ResponseWriter, r *http.Request, fields ...Field) {
	WriteDetails(w, r, contracts.ErrValidationFailed, map[string]any{"fields": fields})
}

// Internal logs err with the request id and answers internal_error without
// any detail.
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	logx.From(r.Context(), nil).Error("erro interno", slog.String("detail", err.Error()))
	Write(w, r, contracts.ErrInternalError)
}

// FromRequestError maps an httpx.RequestError (strict JSON decoding, body
// ceilings) onto the catalog; any other error is an internal error.
func FromRequestError(w http.ResponseWriter, r *http.Request, err error) {
	var re *httpx.RequestError
	if !errors.As(err, &re) {
		Internal(w, r, err)
		return
	}
	if re.Err != nil {
		logx.From(r.Context(), nil).Warn("requisição recusada", slog.String("code", re.Code), slog.String("detail", re.Err.Error()))
	}
	switch re.Code {
	case httpx.CodeInvalidJSON:
		Write(w, r, contracts.ErrInvalidJSON)
	case httpx.CodePayloadTooLarge:
		Write(w, r, contracts.ErrBodyTooLarge)
	case httpx.CodeUnsupportedMedia:
		Write(w, r, contracts.ErrUnsupportedMediaType)
	case httpx.CodeValidation:
		Write(w, r, contracts.ErrValidationFailed)
	default:
		Write(w, r, contracts.ErrInternalError)
	}
}
