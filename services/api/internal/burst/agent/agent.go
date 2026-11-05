// Package agent is the burst worker (docmatch-worker burst): the process on
// a pool instance that claims a signed job manifest from the API's internal
// endpoint, downloads the run's inputs through short-lived URLs, runs the
// local harness and streams its events back. It holds no database setting
// and no AWS credential beyond what the instance role gives the release
// installer. A Spot interruption ends the job as interrupted (the main
// worker requeues the run from its last event) and leaves the instance idle
// until it is gone.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

// Defaults of Options.
const (
	DefaultNoticeInterval = 5 * time.Second
	DefaultFlushEvery     = 250 * time.Millisecond
	DefaultFlushEvents    = 100
	DefaultAPIDownAfter   = 10 * time.Minute
	// maxPending and maxPendingBytes bound the events kept while the API does
	// not answer; past them the job fails (the run is requeued).
	maxPending      = 20_000
	maxPendingBytes = 64 << 20
	callTimeout     = 30 * time.Second
	// finishTimeout bounds the last calls of a job (a Spot notice gives two minutes).
	finishTimeout = 45 * time.Second
	// stagingLabel is the bucket label of the staged URIs (the harness ignores it).
	stagingLabel = "staging"
	// LastErrorFile, under StateDir, keeps the error that ended the previous
	// process; the next one reports it (the journal dies with a Spot instance).
	LastErrorFile = "last-error"
)

// Options configure an Agent.
type Options struct {
	// APIURL is the internal endpoint (DM_BURST_API_URL).
	APIURL *url.URL
	Keys   burst.Keys
	// Harness is the local harness (gRPC over its Unix socket).
	Harness harness.Client
	// MediaRoot is the harness's DM_MEDIA_LOCAL_DIR: inputs are staged under
	// it, group-readable, for the duration of a job.
	MediaRoot string
	// StateDir keeps LastErrorFile (empty: nothing is kept).
	StateDir string
	// Version is reported in the sign of life.
	Version  string
	Identity Identity
	// IMDS watches for termination notices; nil (local runs) never drains.
	IMDS           *IMDS
	NoticeInterval time.Duration
	FlushEvery     time.Duration
	FlushEvents    int
	// APIDownAfter ends Run with an error when the API has not answered for
	// this long (systemd restarts the process); zero never gives up.
	APIDownAfter time.Duration
	// Once returns after the first job (tests and the local run).
	Once bool
	// MaxIdle returns after this long without a job (zero: never).
	MaxIdle time.Duration
	Logger  *slog.Logger
	// HTTPClient overrides the client of the internal endpoint (pinned TLS,
	// no proxy); Downloads the one of presigned URLs (proxy from env).
	HTTPClient *http.Client
	Downloads  *http.Client
}

// Agent claims and runs jobs until its context ends.
type Agent struct {
	o        Options
	api      *http.Client
	dl       *http.Client
	draining atomic.Bool

	mu           sync.Mutex
	reason       string
	cancel       context.CancelFunc
	lastErr      string
	reported     string
	reportedOnce bool
}

// New validates the options.
func New(o Options) (*Agent, error) {
	if o.APIURL == nil || o.Harness == nil || o.MediaRoot == "" || o.Identity.InstanceID == "" {
		return nil, errors.New("agent: URL da API, harness, pasta local e identidade são obrigatórios")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.NoticeInterval <= 0 {
		o.NoticeInterval = DefaultNoticeInterval
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = DefaultFlushEvery
	}
	if o.FlushEvents <= 0 {
		o.FlushEvents = DefaultFlushEvents
	}
	a := &Agent{o: o, api: o.HTTPClient, dl: o.Downloads}
	if a.api == nil {
		a.api = &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: o.Keys.ClientTLS(), MaxIdleConns: 4, IdleConnTimeout: time.Minute}}
	}
	if a.dl == nil {
		a.dl = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 4, IdleConnTimeout: time.Minute}}
	}
	if o.StateDir != "" {
		if prev, err := os.ReadFile(filepath.Join(o.StateDir, LastErrorFile)); err == nil { //nolint:gosec // our own state file
			a.lastErr = strings.TrimSpace(string(prev))
		}
	}
	return a, nil
}

// Draining reports whether the instance got a termination notice.
func (a *Agent) Draining() bool { return a.draining.Load() }

// Drain stops the current job as interrupted and takes no more (the notice
// watcher calls it; tests too).
func (a *Agent) Drain(reason string) {
	a.mu.Lock()
	a.reason = reason
	cancel := a.cancel
	a.mu.Unlock()
	if a.draining.Swap(true) {
		return
	}
	a.o.Logger.Warn("aviso de encerramento da instância; o trabalho atual volta à fila", slog.String("reason", reason))
	if cancel != nil {
		cancel()
	}
}

func (a *Agent) drainReason() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reason
}

func (a *Agent) setCancel(c context.CancelFunc) {
	a.mu.Lock()
	a.cancel = c
	a.mu.Unlock()
}

// RecordFailure keeps the error that ends this process, for the next one to
// report (cmd calls it before exiting).
func RecordFailure(stateDir string, err error) {
	if stateDir == "" || err == nil {
		return
	}
	_ = os.WriteFile(filepath.Join(stateDir, LastErrorFile), []byte(burst.Sanitize(err.Error())+"\n"), 0o600)
}

// Run claims jobs until ctx ends (nil or ctx.Err()), Once/MaxIdle say so or
// the instance drains.
func (a *Agent) Run(ctx context.Context) error {
	if a.o.IMDS != nil {
		go a.watch(ctx)
	}
	a.o.Logger.Info("trabalhador sob demanda pronto", slog.String("instance", a.o.Identity.InstanceID), slog.String("type", a.o.Identity.InstanceType), slog.String("api", a.o.APIURL.Host))
	a.hello(ctx)
	var failingSince time.Time
	backoff := time.Second
	lastJob := time.Now()
	for ctx.Err() == nil {
		if a.Draining() {
			a.o.Logger.Info("instância em encerramento: nenhum trabalho novo")
			<-ctx.Done()
			return nil
		}
		m, ok, err := a.claim(ctx)
		if err != nil && ctx.Err() != nil {
			return ctx.Err() // shutting down: the failed call is not the API's fault
		}
		if err != nil {
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			a.mu.Lock()
			a.lastErr = burst.Sanitize("pedir trabalho: " + err.Error())
			a.mu.Unlock()
			if a.o.APIDownAfter > 0 && time.Since(failingSince) > a.o.APIDownAfter {
				return fmt.Errorf("agent: endpoint interno sem resposta desde %s: %w", failingSince.Format(time.RFC3339), err)
			}
			a.o.Logger.Warn("pedir trabalho falhou", slog.String("err", err.Error()), slog.Duration("retry_in", backoff))
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		failingSince, backoff = time.Time{}, time.Second
		a.hello(ctx) // reports an error the API has not heard of yet
		if !ok {
			if a.o.MaxIdle > 0 && time.Since(lastJob) > a.o.MaxIdle {
				return nil
			}
			continue
		}
		a.execute(ctx, m)
		lastJob = time.Now()
		if a.o.Once {
			return nil
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// watch polls the metadata service for a termination notice.
func (a *Agent) watch(ctx context.Context) {
	t := time.NewTicker(a.o.NoticeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		notice, err := a.o.IMDS.Notice(ctx)
		if err != nil {
			a.o.Logger.Debug("ler aviso de encerramento falhou", slog.String("err", err.Error()))
			continue
		}
		if notice != "" {
			a.Drain(notice)
			return
		}
	}
}

// hello sends the sign of life when the last error changed since the last
// report (and once at start). Best effort.
func (a *Agent) hello(ctx context.Context) {
	a.mu.Lock()
	last, skip := a.lastErr, a.reportedOnce && a.lastErr == a.reported
	a.mu.Unlock()
	if skip {
		return
	}
	body := burst.HelloRequest{Version: a.o.Version, InstanceType: a.o.Identity.InstanceType, Lifecycle: a.o.Identity.Lifecycle, LastError: last}
	if err := a.do(ctx, burst.PathHello, body, ""); err != nil {
		a.o.Logger.Debug("aviso de vida não registrado", slog.String("err", err.Error()))
		return
	}
	a.mu.Lock()
	a.reported, a.reportedOnce = last, true
	a.mu.Unlock()
	if a.o.StateDir != "" && last != "" {
		_ = os.Remove(filepath.Join(a.o.StateDir, LastErrorFile))
	}
}

// ---------------------------------------------------------------- calls

var (
	// errGone is a job the API dropped (cancelled, lost, requeued): stop quietly.
	errGone = errors.New("agent: trabalho encerrado pela API")
	// errFatal is a refusal a retry cannot fix (malformed batch, quota, token).
	errFatal = errors.New("agent: a API recusou o pedido")
)

func (a *Agent) call(ctx context.Context, method, path string, body any, token string) (*http.Response, error) {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	u := a.o.APIURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	a.o.Keys.SignRequest(req, raw, a.o.Identity.InstanceID, time.Now())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return a.api.Do(req)
}

// classify maps an answer of the endpoint: 410 drops the job, a 4xx other
// than 408, 409 and 429 cannot be fixed by retrying, the rest is transient.
func classify(path string, status int) error {
	switch {
	case status < 300:
		return nil
	case status == http.StatusGone:
		return errGone
	case status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusConflict && status != http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s respondeu %d", errFatal, path, status)
	}
	return fmt.Errorf("agent: %s respondeu %d", path, status)
}

// do runs a call and maps its status.
func (a *Agent) do(ctx context.Context, path string, body any, token string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := a.call(ctx, http.MethodPost, path, body, token)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return classify(path, resp.StatusCode)
}

// claim long-polls for a job; ok is false when there is none.
func (a *Agent) claim(ctx context.Context) (burst.Manifest, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, burst.DefaultLongPoll+callTimeout)
	defer cancel()
	resp, err := a.call(ctx, http.MethodPost, burst.PathClaim, burst.ClaimRequest{InstanceType: a.o.Identity.InstanceType, Lifecycle: a.o.Identity.Lifecycle}, "")
	if err != nil {
		return burst.Manifest{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return burst.Manifest{}, false, nil
	case http.StatusOK:
	default:
		return burst.Manifest{}, false, fmt.Errorf("agent: claim respondeu %d", resp.StatusCode)
	}
	var sm burst.SignedManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&sm); err != nil {
		return burst.Manifest{}, false, fmt.Errorf("agent: manifesto ilegível: %w", err)
	}
	m, err := a.o.Keys.OpenManifest(sm, a.o.Identity.InstanceID, time.Now())
	if err != nil {
		return burst.Manifest{}, false, err
	}
	return m, true, nil
}

// ------------------------------------------------------------------ job

type recv struct {
	ev  *pb.RunEvent
	err error
}

// batches posts pending events in calls of at most MaxEventsPerCall events
// and MaxBatchBytes bytes; what was sent leaves pending.
type batches struct {
	a       *Agent
	m       burst.Manifest
	pending [][]byte
	size    int
}

func (b *batches) add(raw []byte) error {
	if len(raw) > burst.MaxEventBytes {
		return fmt.Errorf("%w: evento de %d bytes acima do limite de %d", errFatal, len(raw), burst.MaxEventBytes)
	}
	b.pending = append(b.pending, raw)
	b.size += len(raw)
	if len(b.pending) > maxPending || b.size > maxPendingBytes {
		return fmt.Errorf("%w: a API não recebe os eventos", errFatal)
	}
	return nil
}

// flushFinal sends what is left at the end of a job, retrying a transient
// failure (API restart) until finishTimeout: the work is done and must not be
// thrown away for a moment of unavailability.
func (b *batches) flushFinal(ctx context.Context) error {
	deadline := time.Now().Add(finishTimeout)
	wait := 500 * time.Millisecond
	for {
		err := b.flush(ctx)
		if err == nil || errors.Is(err, errGone) || errors.Is(err, errFatal) || time.Now().After(deadline) {
			return err
		}
		if !sleep(ctx, wait) {
			return err
		}
		wait = min(wait*2, 4*time.Second)
	}
}

func (b *batches) flush(ctx context.Context) error {
	for len(b.pending) > 0 {
		n, size := 0, 0
		for n < len(b.pending) && n < burst.MaxEventsPerCall && (n == 0 || size+len(b.pending[n]) <= burst.MaxBatchBytes) {
			size += len(b.pending[n])
			n++
		}
		if err := b.a.do(ctx, burst.JobPath(b.m.JobID, "events"), burst.EventsRequest{Events: b.pending[:n]}, b.m.Token); err != nil {
			return err
		}
		b.pending, b.size = b.pending[n:], b.size-size
	}
	return nil
}

// execute runs one job to its end and reports the outcome.
func (a *Agent) execute(ctx context.Context, m burst.Manifest) {
	log := a.o.Logger.With(slog.String("job_id", m.JobID), slog.String("run_id", m.RunID))
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.setCancel(cancel)
	defer a.setCancel(nil)
	if a.Draining() {
		cancel()
	}
	// The last calls of a job outlive its context (cancelled by a notice).
	tail := context.WithoutCancel(ctx)

	var gone atomic.Bool
	go a.heartbeat(jobCtx, m, func() { gone.Store(true); cancel() })

	req, cleanup, err := a.stage(jobCtx, m)
	if err != nil {
		a.finish(tail, m, a.outcome(), err.Error(), log)
		return
	}
	defer cleanup()
	stream, err := a.o.Harness.Run(jobCtx, req)
	if err != nil {
		a.finish(tail, m, a.outcome(), "harness: "+err.Error(), log)
		return
	}
	log.Info("trabalho iniciado no harness local", slog.Int("sources", len(req.GetSources())))

	// The harness stream is read on its own goroutine so batches go out on
	// time even while the harness is quiet. Every event and the final error
	// reach the loop below; once execute returns (done), the goroutine stops
	// (jobCtx is cancelled, so Recv returns).
	events := make(chan recv, 64)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			ev, err := stream.Recv()
			select {
			case events <- recv{ev, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(a.o.FlushEvery)
	defer ticker.Stop()
	out := &batches{a: a, m: m}
	// stop reports whether a send error ends the job: 410 quietly, a fatal
	// refusal as failed; a transient error keeps the events for later.
	stop := func(err error) bool {
		switch {
		case errors.Is(err, errGone):
			log.Info("trabalho encerrado pela API; harness interrompido")
			return true
		case errors.Is(err, errFatal):
			a.finish(tail, m, "failed", err.Error(), log)
			return true
		}
		return false
	}
	for {
		select {
		case <-ticker.C:
			if err := out.flush(jobCtx); stop(err) {
				return
			}
			continue
		case r := <-events:
			if r.err != nil {
				if err := out.flush(tail); stop(err) {
					return
				}
				switch {
				case gone.Load():
					log.Info("trabalho encerrado pela API; harness interrompido")
				case errors.Is(r.err, io.EOF):
					a.finish(tail, m, "failed", "o harness encerrou o fluxo sem um evento final", log)
				default:
					a.finish(tail, m, a.outcome(), r.err.Error(), log)
				}
				return
			}
			raw, err := proto.Marshal(r.ev)
			if err == nil {
				err = out.add(raw)
			}
			if err != nil {
				a.finish(tail, m, "failed", err.Error(), log)
				return
			}
			if terminal(r.ev) {
				if err := out.flushFinal(tail); err != nil {
					if !stop(err) {
						a.finish(tail, m, "failed", "enviar o evento final: "+err.Error(), log)
					}
					return
				}
				a.finish(tail, m, "done", "", log)
				return
			}
			if len(out.pending) >= a.o.FlushEvents {
				if err := out.flush(jobCtx); stop(err) {
					return
				}
			}
		}
	}
}

// outcome is how a job that broke ends: interrupted when the instance is
// going away, failed otherwise (both requeue the run on the main side).
func (a *Agent) outcome() string {
	if a.Draining() {
		return "interrupted"
	}
	return "failed"
}

func (a *Agent) finish(ctx context.Context, m burst.Manifest, outcome, reason string, log *slog.Logger) {
	if outcome == "interrupted" {
		reason = strings.TrimSpace(a.drainReason() + " " + reason)
	}
	reason = burst.Sanitize(reason)
	ctx, cancel := context.WithTimeout(ctx, finishTimeout)
	defer cancel()
	err := a.do(ctx, burst.JobPath(m.JobID, "finish"), burst.FinishRequest{Outcome: outcome, Reason: reason}, m.Token)
	switch {
	case err == nil:
		log.Info("trabalho terminado", slog.String("outcome", outcome), slog.String("reason", reason))
	case errors.Is(err, errGone):
		log.Info("trabalho já encerrado pela API", slog.String("outcome", outcome))
	default:
		// The lease expires and the main worker requeues the run.
		log.Warn("informar o fim do trabalho falhou", slog.String("outcome", outcome), slog.String("err", err.Error()))
	}
	if outcome == "failed" {
		a.mu.Lock()
		a.lastErr = burst.Sanitize("trabalho " + m.JobID + ": " + reason)
		a.mu.Unlock()
	}
}

// heartbeat renews the lease until the job ends; a 410 drops the job.
func (a *Agent) heartbeat(ctx context.Context, m burst.Manifest, dropped func()) {
	t := time.NewTicker(time.Duration(max(m.HeartbeatSeconds, 1)) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := a.do(ctx, burst.JobPath(m.JobID, "heartbeat"), struct{}{}, m.Token)
		switch {
		case errors.Is(err, errGone), errors.Is(err, errFatal):
			dropped()
			return
		case err != nil && ctx.Err() == nil:
			a.o.Logger.Warn("renovar a concessão falhou", slog.String("job_id", m.JobID), slog.String("err", err.Error()))
		}
	}
}

func terminal(ev *pb.RunEvent) bool {
	switch ev.GetEvent().(type) {
	case *pb.RunEvent_Completed, *pb.RunEvent_Failed, *pb.RunEvent_NeedsApproval:
		return true
	}
	return false
}

// stage downloads every input of m into MediaRoot/job-<id>/ (directory 0750,
// files 0640: the harness may run as another user of the same group),
// checks each against the size and SHA-256 the upload was confirmed with, and
// returns the request with the inputs as local:// URIs and the cleanup.
func (a *Agent) stage(ctx context.Context, m burst.Manifest) (*pb.RunRequest, func(), error) {
	req := &pb.RunRequest{}
	if err := proto.Unmarshal(m.Request, req); err != nil {
		return nil, nil, fmt.Errorf("pedido ilegível: %w", err)
	}
	if len(req.GetSources()) != len(m.Sources) {
		return nil, nil, fmt.Errorf("manifesto com %d fontes para %d no pedido", len(m.Sources), len(req.GetSources()))
	}
	id, err := uuid.Parse(m.JobID)
	if err != nil {
		return nil, nil, errors.New("manifesto com id de trabalho inválido")
	}
	rel := "job-" + id.String()
	dir := filepath.Join(a.o.MediaRoot, rel)
	_ = os.RemoveAll(dir) // leftovers of a process that died mid-job
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("preparar as fontes: %w", err)
	}
	// On a pool instance the staging directory is setgid (group docmatch) and
	// the unit's umask 0027 already gives 0750: a chmod would drop the setgid
	// bit the files need. Only a stricter umask (local runs) is corrected.
	if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o050 != 0o050 {
		_ = os.Chmod(dir, 0o750) //nolint:gosec // the harness reads the inputs through the shared group
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for i, src := range req.GetSources() {
		name := fmt.Sprintf("%03d", i)
		if err := a.download(ctx, m, i, filepath.Join(dir, name)); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("fonte %s: %w", src.GetId(), err)
		}
		c, ok := proto.Clone(src).(*pb.Source)
		if !ok {
			cleanup()
			return nil, nil, errors.New("cópia da fonte falhou")
		}
		c.Uri = "local://" + stagingLabel + "/" + rel + "/" + name
		req.Sources[i] = c
	}
	return req, cleanup, nil
}

// download fetches input i of m into path: presigned URLs straight from the
// store, internal-endpoint URLs with the job token.
func (a *Agent) download(ctx context.Context, m burst.Manifest, i int, path string) error {
	src := m.Sources[i]
	u, err := a.o.APIURL.Parse(src.URL)
	if err != nil {
		return errors.New("URL inválida no manifesto")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("URL inválida no manifesto")
	}
	client := a.dl
	if src.Bearer {
		client = a.api
		a.o.Keys.SignRequest(req, nil, a.o.Identity.InstanceID, time.Now())
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download falhou: %s", redact(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download respondeu %d (%s)", resp.StatusCode, stripQuery(u))
	}
	limit := src.Size
	if limit <= 0 {
		limit = harness.DefaultStagingMaxBytes
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640) //nolint:gosec // path is our staging dir and a counter
	if err != nil {
		return err
	}
	v := &verified{r: resp.Body, want: strings.ToLower(src.SHA256), h: sha256.New()}
	n, err := io.Copy(f, io.LimitReader(v, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return err
	case n > limit:
		return fmt.Errorf("maior que os %d bytes do upload", limit)
	case !v.ok():
		return errors.New("SHA-256 da fonte não confere com o upload")
	}
	return nil
}

// verified hashes what it reads.
type verified struct {
	r    io.Reader
	want string
	h    hash.Hash
}

func (v *verified) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	return n, err
}

func (v *verified) ok() bool { return v.want == "" || hex.EncodeToString(v.h.Sum(nil)) == v.want }

// stripQuery keeps scheme, host and path of a URL: a presigned signature
// never reaches a log, a job reason or an error.
func stripQuery(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// redact renders a client error without the URL's query.
func redact(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		parsed, perr := url.Parse(ue.URL)
		target := "?"
		if perr == nil {
			target = stripQuery(parsed)
		}
		return ue.Op + " " + target + ": " + ue.Err.Error()
	}
	return err.Error()
}
