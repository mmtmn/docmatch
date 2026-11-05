package sse

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Listener holds one dedicated connection in LISTEN and forwards every
// notification to the broker. It reconnects with backoff, so a database
// restart only delays wake-ups (streams still heartbeat and resume from
// the table on the next signal).
type Listener struct {
	Pool   *pgxpool.Pool
	Broker *Broker
	Logger *slog.Logger
	// Backoff between reconnections (default 2s).
	Backoff time.Duration
}

// Run blocks until ctx is done.
func (l *Listener) Run(ctx context.Context) error {
	if l.Logger == nil {
		l.Logger = slog.Default()
	}
	backoff := l.Backoff
	if backoff <= 0 {
		backoff = 2 * time.Second
	}
	for {
		err := l.listenOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.Logger.Warn("escuta de eventos interrompida; reconectando", slog.String("err", errString(err)))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *Listener) listenOnce(ctx context.Context) error {
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// Hijack would keep the connection out of the pool for good; releasing
	// it after a LISTEN is fine because pgx resets the session on release.
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	l.Logger.Info("escutando eventos de execução", slog.String("channel", Channel))
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		runID, _, perr := ParseNotification(n.Payload)
		if perr != nil {
			l.Logger.Warn("notificação ignorada", slog.String("err", perr.Error()))
			continue
		}
		l.Broker.Notify(runID)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelado"
	}
	return err.Error()
}
