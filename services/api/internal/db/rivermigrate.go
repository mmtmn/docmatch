package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// MigrateRiver applies River's queue schema with its own migrator (versions
// are tracked in river_migration). The pool must connect as the owner; call
// GrantAppRole afterwards so the application role can use the tables.
func MigrateRiver(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) ([]int, error) {
	if log == nil {
		log = slog.Default()
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: log})
	if err != nil {
		return nil, fmt.Errorf("river: migrador: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return nil, fmt.Errorf("river: migrar: %w", err)
	}
	versions := make([]int, 0, len(res.Versions))
	for _, v := range res.Versions {
		versions = append(versions, v.Version)
	}
	return versions, nil
}
