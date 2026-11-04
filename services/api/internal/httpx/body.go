package httpx

import (
	"net/http"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
)

// BodyClass selects the request body ceiling for a route. Every route has
// one; the default (BodyJSON) is small so an unexpected upload can never tie
// up memory or disk.
type BodyClass int

const (
	// BodyJSON is for JSON APIs: http.json_body_max_kb.
	BodyJSON BodyClass = iota
	// BodyDocument is for single document uploads: files.document_file_max_mb.
	BodyDocument
	// BodyMedia is for media uploads: files.media_file_max_gb.
	BodyMedia
	// BodyNone is for routes that accept no body at all.
	BodyNone
)

// Limit returns the byte ceiling for the class from the shared limits.
func (c BodyClass) Limit(l *limits.Limits) int64 {
	switch c {
	case BodyDocument:
		return l.Files.DocumentFileMax()
	case BodyMedia:
		return l.Files.MediaFileMax()
	case BodyNone:
		return 0
	default:
		return l.HTTP.JSONBodyMax()
	}
}

// String names the class for logs.
func (c BodyClass) String() string {
	switch c {
	case BodyDocument:
		return "document"
	case BodyMedia:
		return "media"
	case BodyNone:
		return "none"
	default:
		return "json"
	}
}

// BodyLimit wraps the request body with http.MaxBytesReader sized for the
// class. Reads past the ceiling fail with *http.MaxBytesError, which
// DecodeJSON maps to 413, and the server closes the connection.
func BodyLimit(l *limits.Limits, class BodyClass) func(http.Handler) http.Handler {
	max := class.Limit(l)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
