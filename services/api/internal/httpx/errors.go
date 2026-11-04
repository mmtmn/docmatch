package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

// Error codes are stable identifiers clients can match on; messages are the
// pt-BR text shown to people. Details never leave the server: they go to the
// log line that carries the same request_id.
const (
	CodeInvalidJSON      = "json_invalido"
	CodeValidation       = "dados_invalidos"
	CodePayloadTooLarge  = "corpo_grande_demais"
	CodeUnsupportedMedia = "tipo_de_conteudo_invalido"
	CodeNotFound         = "nao_encontrado"
	CodeMethodNotAllowed = "metodo_nao_permitido"
	CodeRateLimited      = "limite_excedido"
	CodeForbiddenOrigin  = "origem_nao_permitida"
	CodeUnavailable      = "indisponivel"
	CodeInternal         = "erro_interno"
)

// Messages are the default pt-BR texts for each code.
var Messages = map[string]string{
	CodeInvalidJSON:      "O corpo da requisição não é um JSON válido.",
	CodeValidation:       "Os dados enviados são inválidos.",
	CodePayloadTooLarge:  "O corpo da requisição excede o tamanho permitido.",
	CodeUnsupportedMedia: "O tipo de conteúdo da requisição não é aceito.",
	CodeNotFound:         "Recurso não encontrado.",
	CodeMethodNotAllowed: "Método não permitido para este recurso.",
	CodeRateLimited:      "Muitas requisições. Tente novamente em instantes.",
	CodeForbiddenOrigin:  "A origem desta requisição não é permitida.",
	CodeUnavailable:      "Serviço temporariamente indisponível. Tente novamente.",
	CodeInternal:         "Erro interno. Tente novamente; se persistir, informe o identificador da requisição.",
}

// Envelope is the JSON body of every error response.
type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// RequestError is a client-facing error produced while reading a request.
// Err carries the internal detail for the log line only.
type RequestError struct {
	Status  int
	Code    string
	Message string
	Err     error
}

func (e *RequestError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code
}

// Unwrap exposes the internal detail to errors.Is/As.
func (e *RequestError) Unwrap() error { return e.Err }

// NewRequestError builds a RequestError with the default message for code.
func NewRequestError(status int, code string, err error) *RequestError {
	return &RequestError{Status: status, Code: code, Message: Messages[code], Err: err}
}

// WriteError writes the envelope for status/code with the default message.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code string) {
	WriteErrorMessage(w, r, status, code, Messages[code])
}

// WriteErrorMessage writes the envelope with a custom pt-BR message.
func WriteErrorMessage(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if message == "" {
		message = Messages[code]
	}
	env := Envelope{Code: code, Message: message, RequestID: RequestID(r.Context())}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// WriteRequestError maps err to an envelope. A *RequestError keeps its status
// and code and logs its detail; anything else becomes a 500 with the detail
// logged at error level. The response never contains the detail.
func WriteRequestError(w http.ResponseWriter, r *http.Request, err error) {
	var re *RequestError
	if errors.As(err, &re) {
		if re.Err != nil {
			logx.From(r.Context(), nil).Warn("requisição recusada",
				slog.String("code", re.Code), slog.Int("status", re.Status), slog.String("detail", re.Err.Error()))
		}
		WriteErrorMessage(w, r, re.Status, re.Code, re.Message)
		return
	}
	logx.From(r.Context(), nil).Error("erro interno", slog.String("detail", err.Error()))
	WriteError(w, r, http.StatusInternalServerError, CodeInternal)
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
