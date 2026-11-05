package wire

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/burst/burstaws"
	"github.com/mmtmn/docmatch/services/api/internal/burst/burstpg"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
)

// Pool is the server side of the pool of on-demand workers (docs/escala.md):
// routing for quotes and runs, the dispatcher of the main worker, the
// internal endpoint of the API and the scaler.
type Pool struct {
	Settings   burst.Settings
	Store      burst.Store
	Keys       burst.Keys
	Router     *burst.Router
	Dispatcher *burst.Dispatcher
	scaler     *burst.Scaler
}

// newPool builds the pool when the configuration needs it: routing on, the
// internal endpoint open, or an Auto Scaling group to look after (with the
// pool off, the scaler drains it). nil otherwise.
func newPool(cfg *config.Config, sys *System) (*Pool, error) {
	b := cfg.Burst
	if b.Off != "" {
		sys.log.Warn(b.Off)
	}
	if !b.Enabled && b.Listen == "" && b.ASGName == "" {
		return nil, nil
	}
	var largeAfter int64
	var boot float64
	if sys.Throughput != nil {
		largeAfter, boot = sys.Throughput.LargeAfterSeconds, sys.Throughput.Burst.BootSeconds
	}
	settings := burst.FromConfig(b, largeAfter, boot)
	store := burstpg.New(sys.pool)
	p := &Pool{Settings: settings, Store: store}
	if b.Enabled || b.Listen != "" {
		keys, err := burst.DeriveKeys(b.PoolSecret.Reveal())
		if err != nil {
			return nil, err
		}
		p.Keys = keys
	}
	p.scaler = burst.NewScaler(store, nil, settings, sys.log)
	if b.Enabled {
		p.Router = &burst.Router{Settings: settings, Store: store}
		p.Dispatcher = &burst.Dispatcher{Store: store, Settings: settings, Wake: p.scaler.Wake, Logger: sys.log}
	}
	return p, nil
}

// deadlines is the deadline policy of the runs: the env bounds, plus the
// wait for a worker when the pool routes runs.
func deadlines(cfg *config.Config) runs.Deadlines {
	d := runs.Deadlines{Min: cfg.Deadline.Min, Max: cfg.Deadline.Max, Factor: cfg.Deadline.Factor}
	if cfg.Burst.Enabled {
		d.Wait = cfg.Burst.ClaimTimeout
	}
	return d
}

// BurstServer returns the internal endpoint the burst workers call, or nil
// when DM_BURST_LISTEN is empty. Sources of S3 and R2 go out as presigned
// URLs; the local store's are streamed by the endpoint.
func (s *System) BurstServer() *burst.Server {
	if s.Pool == nil || s.cfg.Burst.Listen == "" {
		return nil
	}
	return &burst.Server{
		Store: s.Pool.Store, Keys: s.Pool.Keys, Settings: s.Pool.Settings, Logger: s.log,
		Presign: s.presignSource, Open: s.openSource,
	}
}

// ServeBurst serves the internal endpoint until ctx ends (no-op when off).
func (s *System) ServeBurst(ctx context.Context) error {
	srv := s.BurstServer()
	if srv == nil {
		return nil
	}
	return srv.Serve(ctx, s.cfg.Burst.Listen)
}

// getPresigner is a store that can sign download URLs (storage.S3Store).
type getPresigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// presignSource signs a download URL for an s3:// or r2:// source.
func (s *System) presignSource(ctx context.Context, uri string, ttl time.Duration) (string, bool, error) {
	st, key, ok := s.storeFor(uri)
	if !ok {
		return "", false, nil
	}
	p, ok := st.(getPresigner)
	if !ok {
		return "", false, nil
	}
	u, err := p.PresignGet(ctx, key, ttl)
	return u, err == nil, err
}

// openSource opens any configured source, the local store included.
func (s *System) openSource(ctx context.Context, uri string) (io.ReadCloser, bool, error) {
	st, key, ok := s.storeFor(uri)
	if !ok {
		return nil, false, nil
	}
	rc, err := st.Open(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	return rc, err == nil, err
}

// storeFor resolves scheme://bucket/key to one of the configured stores.
func (s *System) storeFor(uri string) (storage.Store, string, bool) {
	scheme, rest, ok := strings.Cut(uri, "://")
	if !ok {
		return nil, "", false
	}
	bucket, key, ok := strings.Cut(rest, "/")
	if !ok || key == "" {
		return nil, "", false
	}
	for _, st := range []storage.Store{s.Stores.Documents, s.Stores.Media} {
		if st != nil && st.Name() == scheme && st.Bucket() == bucket {
			return st, key, true
		}
	}
	return nil, "", false
}

// BurstScaler returns the scaler of the pool for the main worker, bound to
// the Auto Scaling group when DM_BURST_ASG_NAME names one; nil when the pool
// is off.
func (s *System) BurstScaler(ctx context.Context) (*burst.Scaler, error) {
	if s.Pool == nil {
		return nil, nil
	}
	sc := s.Pool.scaler
	if name := s.cfg.Burst.ASGName; name != "" && sc.ASG == nil {
		g, err := burstaws.New(ctx, burstaws.Options{Name: name, Region: s.cfg.Storage.AWSRegion})
		if err != nil {
			return nil, err
		}
		sc.ASG = g
		s.log.Info("pool sob demanda ligado ao grupo de Auto Scaling", slog.String("asg", name), slog.Bool("enabled", s.Pool.Settings.Enabled),
			slog.Int("max", s.Pool.Settings.MaxInstances), slog.Float64("monthly_hours", s.Pool.Settings.MonthlyHours))
	}
	return sc, nil
}
