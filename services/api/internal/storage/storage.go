// Package storage abstracts the object stores of docmatch: S3 for documents
// (quarantine prefix, SSE-KMS in production, MinIO/RustFS in dev), Cloudflare
// R2 for media (S3-compatible API) and a local filesystem store used when R2
// is not configured (dev and tests). Uploads never pass through the API: the
// client receives a presigned POST (documents) or PUT (media) and the API
// only confirms the object exists and verifies its checksum afterwards.
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Store names, as persisted in uploads.store.
const (
	NameS3    = "s3"
	NameR2    = "r2"
	NameLocal = "local"
)

// Key prefixes inside the uploads bucket (docs/arquitetura.md §7).
const (
	PrefixQuarantine = "quarantine"
	PrefixExtracts   = "extracts"
	PrefixRuns       = "runs"
	PrefixMedia      = "media"
)

// Errors.
var (
	ErrNotFound   = errors.New("storage: objeto não encontrado")
	ErrInvalidKey = errors.New("storage: chave inválida")
)

// Object describes a stored object.
type Object struct {
	Key         string
	Size        int64
	ETag        string
	ContentType string
}

// PresignedPost is a browser-friendly multipart/form-data upload ticket.
type PresignedPost struct {
	URL       string
	Fields    map[string]string
	ExpiresAt time.Time
}

// PresignedPut is a raw-body upload ticket.
type PresignedPut struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Store is one bucket of one object store.
type Store interface {
	// Name is one of NameS3, NameR2, NameLocal.
	Name() string
	// Bucket is the bucket (or local root) name, for URIs and logs.
	Bucket() string
	// PresignPost issues a POST policy that accepts only key, sizes in
	// [1, maxSize] and the given content type.
	PresignPost(ctx context.Context, key string, maxSize int64, contentType string, ttl time.Duration) (PresignedPost, error)
	// PresignPut issues a PUT URL bound to key, size and content type.
	PresignPut(ctx context.Context, key string, size int64, contentType string, ttl time.Duration) (PresignedPut, error)
	// Head returns the object's metadata; ok=false when it does not exist.
	Head(ctx context.Context, key string) (obj Object, ok bool, err error)
	// Open streams the object.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Put writes an object (tests, rebuilt outputs).
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Delete removes an object; missing objects are not an error.
	Delete(ctx context.Context, key string) error
	// URI renders the storage URI the harness receives (s3://, r2://, local://).
	URI(key string) string
}

// keyRe bounds object keys: path segments of safe characters, no "..".
var keyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

// ValidateKey rejects keys that could escape a prefix or confuse a URL.
func ValidateKey(key string) error {
	if len(key) == 0 || len(key) > 512 || !keyRe.MatchString(key) || strings.Contains(key, "..") {
		return ErrInvalidKey
	}
	return nil
}

// QuarantineKey builds the key of a fresh upload: the tenant and upload ids
// plus 128 random bits, so keys are unguessable even with a known upload id.
func QuarantineKey(orgID, uploadID uuid.UUID) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand indisponível: " + err.Error())
	}
	return PrefixQuarantine + "/" + orgID.String() + "/" + uploadID.String() + "/" + hex.EncodeToString(b[:])
}

// MediaKey builds the key of a media upload in the media bucket.
func MediaKey(orgID, uploadID uuid.UUID) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand indisponível: " + err.Error())
	}
	return PrefixMedia + "/" + orgID.String() + "/" + uploadID.String() + "/" + hex.EncodeToString(b[:])
}

// Stores groups the stores the API uses.
type Stores struct {
	// Documents is the S3 uploads bucket.
	Documents Store
	// Media is R2, or the local fallback.
	Media Store
}
