package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Burst pool and run deadline variables (docs/escala.md). The API and the
// main worker read them through Load; the burst worker process reads the
// smaller set of LoadBurstWorker and never sees the database.
const (
	VarBurstEnabled        = "DM_BURST_ENABLED"
	VarBurstListen         = "DM_BURST_LISTEN"
	VarBurstPoolSecret     = "DM_BURST_POOL_SECRET" //nolint:gosec // the variable name, not a credential
	VarBurstASGName        = "DM_BURST_ASG_NAME"
	VarBurstMinSeconds     = "DM_BURST_MIN_SECONDS"
	VarBurstMemoryHeadroom = "DM_BURST_MEMORY_HEADROOM_MB"
	VarBurstKinds          = "DM_BURST_KINDS"
	VarBurstMaxInstances   = "DM_BURST_MAX_INSTANCES"
	VarBurstMonthlyHours   = "DM_BURST_MONTHLY_HOURS"
	VarBurstIdleMinutes    = "DM_BURST_IDLE_MINUTES"
	VarBurstClaimSeconds   = "DM_BURST_CLAIM_SECONDS"
	VarBurstAPIURL         = "DM_BURST_API_URL"

	VarRunDeadlineMin    = "DM_RUN_DEADLINE_MIN"
	VarRunDeadlineMax    = "DM_RUN_DEADLINE_MAX"
	VarRunDeadlineFactor = "DM_RUN_DEADLINE_FACTOR"
)

// Defaults of the burst pool and of the run deadline. The memory headroom is
// what the harness can still take on the t4g.small (512 MiB cap, ~128 MiB
// resident at rest); 2 h was the fixed deadline before it scaled.
const (
	DefaultBurstMemoryHeadroomMB = 384
	DefaultBurstKinds            = "audio,video"
	DefaultBurstMaxInstances     = 2
	DefaultBurstMonthlyHours     = 60
	DefaultBurstIdleMinutes      = 10
	DefaultBurstClaimSeconds     = 600
	DefaultRunDeadlineMin        = 2 * time.Hour
	DefaultRunDeadlineMax        = 24 * time.Hour
	DefaultRunDeadlineFactor     = 3.0

	// minPoolSecretChars is the minimum length of DM_BURST_POOL_SECRET (it
	// keys HMACs and the pinned TLS key of the internal endpoint).
	minPoolSecretChars = 32
)

// Burst configures the pool of on-demand workers on the server side (API
// internal endpoint, routing, scaling and caps).
type Burst struct {
	// Enabled routes heavy runs to the pool; off, every run stays local.
	Enabled bool
	// Listen is host:port of the internal endpoint the burst workers call
	// (VPC only); empty turns the endpoint off.
	Listen string
	// PoolSecret is shared with the burst workers: it signs their requests
	// and the manifests and derives the pinned TLS key.
	PoolSecret Secret
	// ASGName is the Auto Scaling group of the pool; empty means the workers
	// are managed by hand (local runs, tests) and the scaler leaves AWS alone.
	ASGName string
	// MinSeconds is the main-box expected time from which a run is heavy;
	// 0 uses large_after_seconds of config/throughput.yaml.
	MinSeconds int64
	// MemoryHeadroomMB is the memory the main box can still give one run.
	MemoryHeadroomMB int
	// Kinds are input kinds that always go to the pool (transcription, OCR).
	Kinds []string
	// MaxInstances bounds the pool (the ASG max_size bounds it too).
	MaxInstances int
	// MonthlyHours is the instance-hour budget of a calendar month (UTC).
	MonthlyHours float64
	// IdleAfter is how long the pool stays warm without work before it goes to zero.
	IdleAfter time.Duration
	// ClaimTimeout is how long a queued job waits for a worker before the
	// run falls back to the main box.
	ClaimTimeout time.Duration
}

// Deadline bounds the deadline of a run, scaled with its expected time:
// clamp(expected × Factor, Min, Max).
type Deadline struct {
	Min    time.Duration
	Max    time.Duration
	Factor float64
}

// BurstWorker is the configuration of the burst worker process
// (docmatch-worker burst): where the internal endpoint is, the shared
// secret and the local harness. It holds no database setting.
type BurstWorker struct {
	Env           string
	APIURL        *url.URL
	PoolSecret    Secret
	HarnessSocket string
	MediaLocalDir string
}

// readBurst fills the server-side burst and deadline settings.
func (r *reader) readBurst(cfg *Config) {
	b := &cfg.Burst
	b.Enabled = r.boolean(VarBurstEnabled, false)
	b.Listen = r.listenOptional(VarBurstListen)
	b.PoolSecret = r.secret(VarBurstPoolSecret, b.Enabled || b.Listen != "")
	if v := b.PoolSecret.Reveal(); v != "" && len(v) < minPoolSecretChars {
		r.problem(VarBurstPoolSecret, fmt.Sprintf("precisa ter pelo menos %d caracteres", minPoolSecretChars))
	}
	b.ASGName = r.optional(VarBurstASGName, "")
	b.MinSeconds = int64(r.integer(VarBurstMinSeconds, 0, 0, 30*24*3600))
	b.MemoryHeadroomMB = r.integer(VarBurstMemoryHeadroom, DefaultBurstMemoryHeadroomMB, 0, 1<<20)
	b.Kinds = r.list(VarBurstKinds)
	if _, set := r.get(VarBurstKinds); !set {
		b.Kinds = strings.Split(DefaultBurstKinds, ",")
	}
	b.MaxInstances = r.integer(VarBurstMaxInstances, DefaultBurstMaxInstances, 0, 100)
	b.MonthlyHours = r.number(VarBurstMonthlyHours, DefaultBurstMonthlyHours, 0, 100_000)
	b.IdleAfter = time.Duration(r.integer(VarBurstIdleMinutes, DefaultBurstIdleMinutes, 1, 24*60)) * time.Minute
	b.ClaimTimeout = time.Duration(r.integer(VarBurstClaimSeconds, DefaultBurstClaimSeconds, 10, 24*3600)) * time.Second

	d := &cfg.Deadline
	d.Min = r.duration(VarRunDeadlineMin, DefaultRunDeadlineMin)
	d.Max = r.duration(VarRunDeadlineMax, DefaultRunDeadlineMax)
	d.Factor = r.number(VarRunDeadlineFactor, DefaultRunDeadlineFactor, 1, 100)
	if d.Min > 0 && d.Max > 0 && d.Max < d.Min {
		r.problem(VarRunDeadlineMax, "precisa ser maior ou igual a "+VarRunDeadlineMin)
	}
}

// LoadBurstWorker reads the burst worker configuration from the process environment.
func LoadBurstWorker() (*BurstWorker, error) { return LoadBurstWorkerFrom(os.LookupEnv) }

// LoadBurstWorkerFrom reads the burst worker configuration through lookup.
func LoadBurstWorkerFrom(lookup Lookup) (*BurstWorker, error) {
	r := &reader{lookup: lookup}
	w := &BurstWorker{}
	w.Env = r.enum(VarEnv, DefaultEnv, EnvDev, EnvStaging, EnvProd)
	if v, ok := r.required(VarBurstAPIURL); ok {
		u, err := url.Parse(v)
		switch {
		case err != nil || u.Host == "":
			r.problem(VarBurstAPIURL, "não é uma URL válida")
		case u.Scheme != "https":
			r.problem(VarBurstAPIURL, "precisa usar https:// (TLS com a chave fixada do endpoint interno)")
		default:
			w.APIURL = u
		}
	}
	w.PoolSecret = r.secret(VarBurstPoolSecret, true)
	if v := w.PoolSecret.Reveal(); v != "" && len(v) < minPoolSecretChars {
		r.problem(VarBurstPoolSecret, fmt.Sprintf("precisa ter pelo menos %d caracteres", minPoolSecretChars))
	}
	if v, ok := r.required(VarHarnessSocket); ok {
		w.HarnessSocket = v
	}
	w.MediaLocalDir = r.optional(VarMediaLocalDir, DefaultMediaLocalDir)
	if err := r.err(); err != nil {
		return nil, err
	}
	return w, nil
}

// BurstWorkerVars lists the variables LoadBurstWorker reads.
func BurstWorkerVars() []string {
	return []string{VarEnv, VarBurstAPIURL, VarBurstPoolSecret, VarHarnessSocket, VarMediaLocalDir}
}

// burstVars are the burst and deadline variables Load reads, in .env.example order.
func burstVars() []string {
	return []string{
		VarBurstEnabled, VarBurstListen, VarBurstPoolSecret, VarBurstASGName, VarBurstMinSeconds, VarBurstMemoryHeadroom,
		VarBurstKinds, VarBurstMaxInstances, VarBurstMonthlyHours, VarBurstIdleMinutes, VarBurstClaimSeconds, VarBurstAPIURL,
		VarRunDeadlineMin, VarRunDeadlineMax, VarRunDeadlineFactor,
	}
}

// burstRedacted renders the burst settings for String/LogValue.
func (c *Config) burstRedacted() [][2]string {
	b, d := c.Burst, c.Deadline
	return [][2]string{
		{VarBurstEnabled, strconv.FormatBool(b.Enabled)},
		{VarBurstListen, b.Listen},
		{VarBurstPoolSecret, presence(b.PoolSecret)},
		{VarBurstASGName, b.ASGName},
		{VarBurstMinSeconds, strconv.FormatInt(b.MinSeconds, 10)},
		{VarBurstMemoryHeadroom, strconv.Itoa(b.MemoryHeadroomMB)},
		{VarBurstKinds, strings.Join(b.Kinds, ",")},
		{VarBurstMaxInstances, strconv.Itoa(b.MaxInstances)},
		{VarBurstMonthlyHours, strconv.FormatFloat(b.MonthlyHours, 'f', -1, 64)},
		{VarBurstIdleMinutes, strconv.Itoa(int(b.IdleAfter / time.Minute))},
		{VarBurstClaimSeconds, strconv.Itoa(int(b.ClaimTimeout / time.Second))},
		{VarRunDeadlineMin, d.Min.String()},
		{VarRunDeadlineMax, d.Max.String()},
		{VarRunDeadlineFactor, strconv.FormatFloat(d.Factor, 'f', -1, 64)},
	}
}

// integer reads an optional whole number inside [lo, hi].
func (r *reader) integer(name string, def, lo, hi int) int {
	v, ok := r.get(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		r.problem(name, fmt.Sprintf("precisa ser um número inteiro entre %d e %d", lo, hi))
		return def
	}
	return n
}

// number reads an optional decimal inside [lo, hi].
func (r *reader) number(name string, def, lo, hi float64) float64 {
	v, ok := r.get(name)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f < lo || f > hi {
		r.problem(name, fmt.Sprintf("precisa ser um número entre %g e %g", lo, hi))
		return def
	}
	return f
}

// duration reads an optional positive Go duration (90m, 2h, 24h).
func (r *reader) duration(name string, def time.Duration) time.Duration {
	v, ok := r.get(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		r.problem(name, "precisa ser uma duração positiva (ex.: 2h, 90m)")
		return def
	}
	return d
}

// listenOptional reads an optional host:port to listen on.
func (r *reader) listenOptional(name string) string {
	if _, ok := r.get(name); !ok {
		return ""
	}
	return r.listen(name)
}
