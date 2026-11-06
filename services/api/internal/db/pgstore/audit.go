package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
)

// AuditStore is the pgx implementation of audit.Store.
type AuditStore struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

// NewAuditStore wraps the pool.
func NewAuditStore(pool *pgxpool.Pool) *AuditStore { return &AuditStore{pool: pool} }

// NewAuditStoreTx binds the audit append to an existing tenant transaction.
func NewAuditStoreTx(tx pgx.Tx) *AuditStore { return &AuditStore{tx: tx} }

// chainTx runs fn in the right tenant scope: the organization's transaction
// for org chains, system mode for the system chain.
func (s *AuditStore) chainTx(ctx context.Context, orgID *uuid.UUID, fn db.TxFunc) error {
	if s.tx != nil {
		return fn(ctx, s.tx)
	}
	if orgID == nil {
		return db.WithSystemTx(ctx, s.pool, fn)
	}
	return db.WithOrgTx(ctx, s.pool, *orgID, fn)
}

// InChainTx implements audit.Store: it takes the chain's transaction-level
// advisory lock so concurrent appends to one chain serialize.
func (s *AuditStore) InChainTx(ctx context.Context, orgID *uuid.UUID, fn func(ctx context.Context, tx audit.Tx) error) error {
	return s.chainTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		chain := audit.ChainID(orgID)
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "audit:"+chain.String()); err != nil {
			return fmt.Errorf("audit: lock da cadeia: %w", err)
		}
		return fn(ctx, &auditTx{q: sqlc.New(tx)})
	})
}

// Range implements audit.Store.
func (s *AuditStore) Range(ctx context.Context, orgID *uuid.UUID, from, to int64) ([]audit.Entry, error) {
	var out []audit.Entry
	err := s.chainTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListAuditRange(ctx, sqlc.ListAuditRangeParams{ChainID: audit.ChainID(orgID), Seq: from, Seq_2: to})
		if err != nil {
			return err
		}
		out = make([]audit.Entry, 0, len(rows))
		for _, r := range rows {
			out = append(out, audit.Entry{
				ID: r.ID, OrgID: r.OrgID, ChainID: r.ChainID, Seq: r.Seq,
				Actor: audit.Actor{Type: audit.ActorType(r.ActorType), ID: r.ActorID}, Action: r.Action,
				Target: audit.Target{Type: r.TargetType, ID: strVal(r.TargetID)}, MetaText: r.MetaText,
				PrevHash: r.PrevHash, Hash: r.Hash, RequestID: strVal(r.RequestID), CreatedAt: r.CreatedAt,
			})
		}
		return nil
	})
	return out, err
}

type auditTx struct {
	q *sqlc.Queries
}

func (t *auditTx) Tail(ctx context.Context, chainID uuid.UUID) (int64, []byte, bool, error) {
	row, err := t.q.GetAuditChainTail(ctx, chainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	return row.Seq, row.Hash, true, nil
}

func (t *auditTx) Canonical(ctx context.Context, meta []byte) (string, error) {
	return t.q.CanonicalJSON(ctx, meta)
}

func (t *auditTx) Append(ctx context.Context, e audit.Entry) (audit.Entry, error) {
	row, err := t.q.AppendAudit(ctx, sqlc.AppendAuditParams{
		OrgID: e.OrgID, Seq: e.Seq, ActorType: string(e.Actor.Type), ActorID: e.Actor.ID, Action: e.Action,
		TargetType: e.Target.Type, TargetID: strPtr(e.Target.ID), Meta: []byte(e.MetaText), PrevHash: e.PrevHash,
		Hash: e.Hash, RequestID: strPtr(e.RequestID), CreatedAt: e.CreatedAt,
	})
	if err != nil {
		return audit.Entry{}, err
	}
	e.ID = row.ID
	e.ChainID = row.ChainID
	e.CreatedAt = row.CreatedAt
	return e, nil
}
