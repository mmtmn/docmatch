// Package logx builds the process logger: JSON on stdout, UTC timestamps,
// level from the environment name. Request bodies and secrets never reach it;
// callers log identifiers, counts and durations only.
package logx

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// New returns a JSON logger writing to w. Debug level is enabled only in dev.
func New(w io.Writer, env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "dev" {
		level = slog.LevelDebug
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.String(slog.TimeKey, t.UTC().Format(time.RFC3339Nano))
				}
			}
			return a
		},
	})
	return slog.New(h)
}

// Discard returns a logger that drops everything (tests).
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type ctxKey struct{}

// WithLogger stores a request-scoped logger in ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the request-scoped logger, or fallback when none is stored.
func From(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	if fallback != nil {
		return fallback
	}
	return slog.Default()
}
