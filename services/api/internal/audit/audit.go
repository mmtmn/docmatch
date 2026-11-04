// Package audit appends hash-chained rows to audit_log and verifies them.
//
// Each organization has its own chain (system events without an org use the
// zero uuid). Rows carry a gapless sequence number and
//
//	hash = SHA-256(HashVersion ‖ chain ‖ seq ‖ created_at ‖ actor ‖ action ‖
//	                target ‖ meta ‖ prev_hash)
//
// where meta is the canonical text of the stored jsonb (the store provides
// it, so Record and Verify hash exactly the bytes PostgreSQL keeps). Record
// takes the chain's advisory lock, reads the tail and appends; Verify
// re-reads a range and recomputes every hash, so an edited, deleted or
// reordered row is detected. The application role has no UPDATE/DELETE on
// the table; the chain catches what a privileged tamperer does.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

// HashVersion is bound into every hash so a future layout change cannot be
// confused with tampering.
const HashVersion = "docmatch-audit-v1"

// ActorType classifies who acted.
type ActorType string

// Actor types (mirror audit_log.actor_type).
const (
	ActorUser   ActorType = "user"
	ActorAPIKey ActorType = "api_key"
	ActorSystem ActorType = "system"
	ActorAdmin  ActorType = "admin"
)

// Valid reports whether the actor type is known.
func (a ActorType) Valid() bool {
	switch a {
	case ActorUser, ActorAPIKey, ActorSystem, ActorAdmin:
		return true
	}
	return false
}

// Actor identifies who acted.
type Actor struct {
	Type ActorType
	ID   string
}

// Target identifies what was acted upon.
type Target struct {
	Type string
	ID   string
}

// Entry is one audit row.
type Entry struct {
	ID        uuid.UUID
	OrgID     *uuid.UUID
	ChainID   uuid.UUID
	Seq       int64
	Actor     Actor
	Action    string
	Target    Target
	MetaText  string // canonical JSON as stored
	PrevHash  []byte
	Hash      []byte
	RequestID string
	CreatedAt time.Time
}

// SystemChain is the chain id of events without an organization.
var SystemChain = uuid.Nil

// ChainID maps an optional org to its chain.
func ChainID(orgID *uuid.UUID) uuid.UUID {
	if orgID == nil {
		return SystemChain
	}
	return *orgID
}

// Errors.
var (
	ErrTampered     = errors.New("audit: chain verification failed")
	ErrInvalidActor = errors.New("audit: invalid actor")
	ErrInvalidEntry = errors.New("audit: action and target type are required")
	ErrNilStore     = errors.New("audit: nil store")
)

// Store is the persistence boundary.
type Store interface {
	// InChainTx runs fn in a transaction scoped to the organization (nil
	// means the system chain, in cross-tenant mode) while holding the chain
	// lock, so appends to one chain are serialized.
	InChainTx(ctx context.Context, orgID *uuid.UUID, fn func(ctx context.Context, tx Tx) error) error
	// Range returns entries of the chain with from ≤ seq ≤ to, ascending.
	Range(ctx context.Context, orgID *uuid.UUID, from, to int64) ([]Entry, error)
}

// Tx is what Record needs inside the transaction.
type Tx interface {
	// Tail returns the last seq and hash of the chain (ok=false when empty).
	Tail(ctx context.Context, chainID uuid.UUID) (seq int64, hash []byte, ok bool, err error)
	// Canonical returns the canonical text the store will keep for meta.
	Canonical(ctx context.Context, meta []byte) (string, error)
	// Append stores the entry as given (ID may be assigned by the store).
	Append(ctx context.Context, e Entry) (Entry, error)
}

// timeLayout fixes microsecond precision in UTC, which is what timestamptz
// stores, so the value hashed before insert equals the value read back.
const timeLayout = "2006-01-02T15:04:05.000000Z"

// Canonical renders the hashed representation of an entry (without Hash).
func Canonical(e Entry) []byte {
	var b bytes.Buffer
	fields := []string{
		HashVersion,
		e.ChainID.String(),
		strconv.FormatInt(e.Seq, 10),
		e.CreatedAt.UTC().Format(timeLayout),
		string(e.Actor.Type),
		e.Actor.ID,
		e.Action,
		e.Target.Type,
		e.Target.ID,
		e.MetaText,
		hex.EncodeToString(e.PrevHash),
	}
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('\n')
		}
		// Length prefix each field so a newline inside a value cannot shift
		// field boundaries.
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.Bytes()
}

// Hash computes the chain hash of an entry.
func Hash(e Entry) []byte {
	sum := sha256.Sum256(Canonical(e))
	return sum[:]
}

// Service records and verifies entries.
type Service struct {
	store Store
	now   func() time.Time
}

// Option configures New.
type Option func(*Service)

// WithClock substitutes the time source (tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// New returns a service over store.
func New(store Store, opts ...Option) *Service {
	s := &Service{store: store, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Record appends an entry to the chain of orgID (nil = system chain). meta
// is any JSON-encodable value (nil becomes {}). The request id, when the
// context carries one, is stored alongside.
func (s *Service) Record(ctx context.Context, orgID *uuid.UUID, actor Actor, action string, target Target, meta any) (Entry, error) {
	if s.store == nil {
		return Entry{}, ErrNilStore
	}
	if !actor.Type.Valid() || strings.TrimSpace(actor.ID) == "" {
		return Entry{}, ErrInvalidActor
	}
	if strings.TrimSpace(action) == "" || strings.TrimSpace(target.Type) == "" {
		return Entry{}, ErrInvalidEntry
	}
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return Entry{}, fmt.Errorf("audit: meta is not JSON-encodable: %w", err)
	}
	if len(raw) == 0 || raw[0] != '{' {
		return Entry{}, errors.New("audit: meta must encode to a JSON object")
	}
	var out Entry
	err = s.store.InChainTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		chain := ChainID(orgID)
		canonical, err := tx.Canonical(ctx, raw)
		if err != nil {
			return err
		}
		seq, prev, ok, err := tx.Tail(ctx, chain)
		if err != nil {
			return err
		}
		if !ok {
			seq, prev = 0, nil
		}
		e := Entry{
			OrgID: orgID, ChainID: chain, Seq: seq + 1, Actor: actor, Action: action, Target: target,
			MetaText: canonical, PrevHash: prev, RequestID: httpx.RequestID(ctx),
			CreatedAt: s.now().UTC().Truncate(time.Microsecond),
		}
		e.Hash = Hash(e)
		stored, err := tx.Append(ctx, e)
		if err != nil {
			return err
		}
		out = stored
		return nil
	})
	return out, err
}

// Report summarizes a verification.
type Report struct {
	Checked  int
	FirstSeq int64
	LastSeq  int64
	// BrokenSeq is the first sequence number that failed (0 when intact).
	BrokenSeq int64
	Reason    string
}

// Verify re-reads seq from..to of the chain and recomputes every hash. to ≤ 0
// means "to the tail". It returns ErrTampered (with the report filled) when
// a row was changed, removed or reordered.
func (s *Service) Verify(ctx context.Context, orgID *uuid.UUID, from, to int64) (Report, error) {
	if s.store == nil {
		return Report{}, ErrNilStore
	}
	if from < 1 {
		from = 1
	}
	if to <= 0 {
		to = math.MaxInt64
	}
	if to < from {
		return Report{}, errors.New("audit: invalid range")
	}
	// Read one row before the range as the anchor for the first prev_hash.
	readFrom := from
	if from > 1 {
		readFrom = from - 1
	}
	rows, err := s.store.Range(ctx, orgID, readFrom, to)
	if err != nil {
		return Report{}, err
	}
	rep := Report{}
	var prev []byte
	expect := from
	if from > 1 {
		if len(rows) == 0 || rows[0].Seq != from-1 {
			rep.BrokenSeq = from - 1
			rep.Reason = "anchor row missing"
			return rep, fmt.Errorf("%w: seq %d: %s", ErrTampered, rep.BrokenSeq, rep.Reason)
		}
		prev = rows[0].Hash
		rows = rows[1:]
	}
	for _, e := range rows {
		if e.Seq != expect {
			rep.BrokenSeq = expect
			rep.Reason = fmt.Sprintf("expected seq %d, found %d", expect, e.Seq)
			return rep, fmt.Errorf("%w: seq %d: %s", ErrTampered, rep.BrokenSeq, rep.Reason)
		}
		if !bytes.Equal(e.PrevHash, prev) {
			rep.BrokenSeq = e.Seq
			rep.Reason = "prev_hash does not match previous row"
			return rep, fmt.Errorf("%w: seq %d: %s", ErrTampered, rep.BrokenSeq, rep.Reason)
		}
		if !bytes.Equal(Hash(e), e.Hash) {
			rep.BrokenSeq = e.Seq
			rep.Reason = "hash does not match row contents"
			return rep, fmt.Errorf("%w: seq %d: %s", ErrTampered, rep.BrokenSeq, rep.Reason)
		}
		if rep.Checked == 0 {
			rep.FirstSeq = e.Seq
		}
		rep.LastSeq = e.Seq
		rep.Checked++
		prev = e.Hash
		expect++
	}
	if to != math.MaxInt64 && expect <= to {
		rep.BrokenSeq = expect
		rep.Reason = "row missing at the end of the range"
		return rep, fmt.Errorf("%w: seq %d: %s", ErrTampered, rep.BrokenSeq, rep.Reason)
	}
	return rep, nil
}

// CanonicalJSON renders raw JSON in the memory store's canonical form:
// objects with sorted keys, no insignificant whitespace, numbers kept as
// written. PostgreSQL has its own canonical form; each store is
// self-consistent, which is all the chain needs.
func CanonicalJSON(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("audit: invalid JSON: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
