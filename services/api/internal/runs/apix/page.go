package apix

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
)

// Pagination defaults from openapi.yaml (parameters.Limit).
const (
	DefaultLimit = 25
	MaxLimit     = 100
	// maxCursorLen mirrors ids.idempotency_key_max, the ceiling of the cursor parameter.
	maxCursorLen = contracts.IDsIdempotencyKeyMax
)

// Page is a decoded cursor/limit pair. Cursors are opaque offsets: stable
// enough for result lists that change rarely, and never trusted beyond
// bounds checks.
type Page struct {
	Limit  int
	Offset int
}

// EncodeCursor renders the cursor for the next page (nil when the page is
// not full, which is how the client learns it reached the end).
func EncodeCursor(p Page, got int) *string {
	if got < p.Limit {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(p.Offset+got)))
	return &s
}

// decodeCursor parses a cursor produced by EncodeCursor.
func decodeCursor(s string) (int, error) {
	if len(s) > maxCursorLen {
		return 0, errors.New("cursor longo demais")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, err
	}
	n, ok := strings.CutPrefix(string(raw), "o:")
	if !ok {
		return 0, errors.New("cursor inválido")
	}
	off, err := strconv.Atoi(n)
	if err != nil || off < 0 {
		return 0, errors.New("cursor inválido")
	}
	return off, nil
}

// ParsePage reads cursor and limit from the query string.
func ParsePage(r *http.Request) (Page, *Error) {
	p := Page{Limit: DefaultLimit}
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxLimit {
			return p, Validation(FieldError{Path: "/limit", Code: "out_of_range", Max: MaxLimit})
		}
		p.Limit = n
	}
	if v := q.Get("cursor"); v != "" {
		off, err := decodeCursor(v)
		if err != nil {
			return p, Validation(FieldError{Path: "/cursor", Code: "invalid"})
		}
		p.Offset = off
	}
	return p, nil
}
