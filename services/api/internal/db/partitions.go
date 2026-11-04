package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Querier is the subset of pgx used by the partition helpers (pool, conn, tx).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Partition describes one child of a partitioned table.
type Partition struct {
	Name    string
	Parent  string
	Created bool
}

// EnsureMonthPartitions calls the SQL maintenance function that creates the
// monthly partitions of credit_ledger, findings, api_usage and audit_log for
// the current month, monthsBack months behind and monthsAhead months ahead.
// It returns every partition it looked at, flagging the ones created now.
func EnsureMonthPartitions(ctx context.Context, q Querier, monthsAhead, monthsBack int) ([]Partition, error) {
	rows, err := q.Query(ctx, "SELECT partition_name, created FROM ensure_month_partitions($1, $2)", monthsAhead, monthsBack)
	if err != nil {
		return nil, fmt.Errorf("db: ensure_month_partitions: %w", err)
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		var p Partition
		if err := rows.Scan(&p.Name, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPartitions lists every partition in the public schema with its parent.
func ListPartitions(ctx context.Context, q Querier) ([]Partition, error) {
	rows, err := q.Query(ctx, `SELECT c.relname, p.relname
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
ORDER BY p.relname, c.relname`)
	if err != nil {
		return nil, fmt.Errorf("db: listar partições: %w", err)
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		var p Partition
		if err := rows.Scan(&p.Name, &p.Parent); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
