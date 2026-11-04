package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxJSONDepth bounds nesting so a tiny body cannot exhaust the stack of the
// decoder or of anything that walks the result recursively.
const MaxJSONDepth = 32

// Text validation errors returned by NormalizeText and ValidateText.
var (
	ErrInvalidUTF8  = errors.New("texto não é UTF-8 válido")
	ErrControlChars = errors.New("texto contém caracteres de controle")
	ErrTooLong      = errors.New("texto excede o tamanho máximo")
)

// DecodeJSON reads the request body as strict JSON into dst:
//   - Content-Type must be application/json (optionally charset=utf-8);
//   - the body is bounded by BodyLimit (413 on overflow);
//   - the document must be valid UTF-8, nested at most MaxJSONDepth levels,
//     free of control characters in strings, and contain exactly one value;
//   - unknown object keys are rejected.
//
// Failures come back as *RequestError with a 4xx status and a pt-BR message.
func DecodeJSON(r *http.Request, dst any) error {
	if err := checkJSONContentType(r); err != nil {
		return err
	}
	if r.Body == nil || r.Body == http.NoBody {
		return &RequestError{Status: http.StatusBadRequest, Code: CodeInvalidJSON, Message: Messages[CodeInvalidJSON], Err: errors.New("corpo vazio")}
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return NewRequestError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, err)
		}
		return NewRequestError(http.StatusBadRequest, CodeInvalidJSON, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return NewRequestError(http.StatusBadRequest, CodeInvalidJSON, errors.New("corpo vazio"))
	}
	if !utf8.Valid(data) {
		return NewRequestError(http.StatusBadRequest, CodeInvalidJSON, ErrInvalidUTF8)
	}
	if err := scanJSON(data, MaxJSONDepth); err != nil {
		return NewRequestError(http.StatusBadRequest, CodeInvalidJSON, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return NewRequestError(http.StatusBadRequest, CodeInvalidJSON, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return NewRequestError(http.StatusBadRequest, CodeInvalidJSON, errors.New("conteúdo extra após o JSON"))
	}
	return nil
}

func checkJSONContentType(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return NewRequestError(http.StatusUnsupportedMediaType, CodeUnsupportedMedia, errors.New("Content-Type ausente"))
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != "application/json" {
		return NewRequestError(http.StatusUnsupportedMediaType, CodeUnsupportedMedia, fmt.Errorf("Content-Type %q", ct))
	}
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return NewRequestError(http.StatusUnsupportedMediaType, CodeUnsupportedMedia, fmt.Errorf("charset %q", cs))
	}
	return nil
}

// scanJSON walks the token stream once to enforce depth and string hygiene
// before the typed decode. It also guarantees the document is well formed.
func scanJSON(data []byte, maxDepth int) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
				if depth > maxDepth {
					return fmt.Errorf("profundidade acima de %d níveis", maxDepth)
				}
			case '}', ']':
				depth--
			}
		case string:
			if hasForbiddenChars(v, true) {
				return ErrControlChars
			}
		}
	}
}

// hasForbiddenChars reports control characters (C0/C1) and Unicode bidi
// controls. Tabs and newlines are allowed when allowWhitespace is true;
// carriage return is accepted there too (NormalizeText folds CRLF).
func hasForbiddenChars(s string, allowWhitespace bool) bool {
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			if !allowWhitespace {
				return true
			}
		case unicode.IsControl(r):
			return true
		case isBidiControl(r):
			return true
		}
	}
	return false
}

// isBidiControl flags the explicit bidirectional formatting characters that
// enable "Trojan Source" style rendering tricks.
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F || r == 0x061C
}

// NormalizeText prepares free text from a client: validates UTF-8, applies
// NFC, folds CRLF/CR into LF, trims surrounding whitespace and rejects
// control characters other than tab and newline.
func NormalizeText(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", ErrInvalidUTF8
	}
	s = norm.NFC.String(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	if hasForbiddenChars(s, true) {
		return "", ErrControlChars
	}
	return s, nil
}

// NormalizeLine is NormalizeText for single-line fields (names, titles,
// search terms): newlines and tabs are rejected too.
func NormalizeLine(s string) (string, error) {
	s, err := NormalizeText(s)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(s, "\n\t") {
		return "", ErrControlChars
	}
	return s, nil
}

// CountChars returns the length in code points, the unit limits.yaml uses.
func CountChars(s string) int { return utf8.RuneCountInString(s) }

// ValidateText normalizes s and enforces a maximum length in code points.
// Use NormalizeLine semantics by passing singleLine.
func ValidateText(s string, max int, singleLine bool) (string, error) {
	var (
		out string
		err error
	)
	if singleLine {
		out, err = NormalizeLine(s)
	} else {
		out, err = NormalizeText(s)
	}
	if err != nil {
		return "", err
	}
	if CountChars(out) > max {
		return "", ErrTooLong
	}
	return out, nil
}
