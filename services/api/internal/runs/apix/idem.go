package apix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
)

// HeaderIdempotencyKey is the header of operations that spend credits.
const HeaderIdempotencyKey = "Idempotency-Key"

// IdemTTL is how long a key is remembered (openapi: "guardada por 24 h").
const IdemTTL = 24 * time.Hour

// IdemRecord is one remembered key.
type IdemRecord struct {
	Key            string
	RequestHash    []byte
	Done           bool
	ResponseStatus int
	ResponseBody   []byte
}

// IdemStore persists keys per organization.
type IdemStore interface {
	// BeginIdem records key with hash unless it exists; existing reports
	// which case happened and returns the stored record.
	BeginIdem(ctx context.Context, orgID uuid.UUID, key string, hash []byte, expiresAt time.Time) (rec IdemRecord, existing bool, err error)
	// FinishIdem stores the response to replay.
	FinishIdem(ctx context.Context, orgID uuid.UUID, key string, status int, body []byte) error
	// AbortIdem forgets the key so the client may retry after a failure.
	AbortIdem(ctx context.Context, orgID uuid.UUID, key string) error
}

// IdemKey reads and validates the header.
func IdemKey(r *http.Request) (string, *Error) {
	key := strings.TrimSpace(r.Header.Get(HeaderIdempotencyKey))
	if key == "" {
		return "", E(contracts.ErrMissingIdempotencyKey, nil)
	}
	if len(key) > contracts.IDsIdempotencyKeyMax {
		return "", Validation(FieldError{Path: "/" + HeaderIdempotencyKey, Code: "text_too_long", Max: contracts.IDsIdempotencyKeyMax})
	}
	for _, c := range key {
		if c > unicode.MaxASCII || !unicode.IsPrint(c) {
			return "", Validation(FieldError{Path: "/" + HeaderIdempotencyKey, Code: "invalid"})
		}
	}
	return key, nil
}

// IdemHash fingerprints the request so a reused key with a different body
// is detected: method, path and the raw body.
func IdemHash(r *http.Request, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

// Result is what an idempotent operation produces on success.
type Result struct {
	Status  int
	Body    any
	Headers map[string]string
}

// Idempotent runs fn at most once per (organization, key, body): a replay
// with the same body gets the stored response, a replay with another body
// gets idempotency_key_reused, and a concurrent replay gets
// idempotency_in_progress. A failed fn forgets the key so the client can
// correct the problem (insufficient credits, for example) and retry.
func Idempotent(ctx context.Context, store IdemStore, orgID uuid.UUID, key string, hash []byte, now time.Time, fn func() (Result, error)) (Result, error) {
	rec, existing, err := store.BeginIdem(ctx, orgID, key, hash, now.Add(IdemTTL))
	if err != nil {
		return Result{}, fmt.Errorf("idempotência: %w", err)
	}
	if existing {
		if !bytes.Equal(rec.RequestHash, hash) {
			return Result{}, E(contracts.ErrIdempotencyKeyReused, nil)
		}
		if !rec.Done {
			return Result{}, E(contracts.ErrIdempotencyInProgress, nil)
		}
		return Result{Status: rec.ResponseStatus, Body: json.RawMessage(rec.ResponseBody)}, nil
	}
	res, err := fn()
	if err != nil {
		if abortErr := store.AbortIdem(ctx, orgID, key); abortErr != nil {
			err = errors.Join(err, abortErr)
		}
		return Result{}, err
	}
	body, err := json.Marshal(res.Body)
	if err != nil {
		return Result{}, fmt.Errorf("idempotência: serializar resposta: %w", err)
	}
	if err := store.FinishIdem(ctx, orgID, key, res.Status, body); err != nil {
		return Result{}, fmt.Errorf("idempotência: concluir: %w", err)
	}
	res.Body = json.RawMessage(body)
	return res, nil
}

// MemIdemStore is the in-memory IdemStore (tests).
type MemIdemStore struct {
	recs map[string]IdemRecord
}

// NewMemIdemStore returns an empty store.
func NewMemIdemStore() *MemIdemStore { return &MemIdemStore{recs: map[string]IdemRecord{}} }

func idemMapKey(orgID uuid.UUID, key string) string { return orgID.String() + "/" + key }

// BeginIdem implements IdemStore.
func (m *MemIdemStore) BeginIdem(_ context.Context, orgID uuid.UUID, key string, hash []byte, _ time.Time) (IdemRecord, bool, error) {
	if rec, ok := m.recs[idemMapKey(orgID, key)]; ok {
		return rec, true, nil
	}
	rec := IdemRecord{Key: key, RequestHash: hash}
	m.recs[idemMapKey(orgID, key)] = rec
	return rec, false, nil
}

// FinishIdem implements IdemStore.
func (m *MemIdemStore) FinishIdem(_ context.Context, orgID uuid.UUID, key string, status int, body []byte) error {
	rec := m.recs[idemMapKey(orgID, key)]
	rec.Done, rec.ResponseStatus, rec.ResponseBody = true, status, body
	m.recs[idemMapKey(orgID, key)] = rec
	return nil
}

// AbortIdem implements IdemStore.
func (m *MemIdemStore) AbortIdem(_ context.Context, orgID uuid.UUID, key string) error {
	delete(m.recs, idemMapKey(orgID, key))
	return nil
}
