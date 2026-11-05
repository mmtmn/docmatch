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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

// sourceScheme names the inputs of a job by position inside the agent; the
// opener resolves them against the manifest.
const sourceScheme = "burst://"

// Defaults of Options.
const (
	DefaultNoticeInterval = 5 * time.Second
	DefaultFlushEvery     = 250 * time.Millisecond
	DefaultFlushEvents    = 100
	DefaultAPIDownAfter   = 10 * time.Minute
	// maxPending bounds the events kept while the API does not answer.
	maxPending  = 20_000
	callTimeout = 30 * time.Second
	// finishTimeout bounds the last calls of a job (a Spot notice gives two minutes).
	finishTimeout = 45 * time.Second
)

// Options configure an Agent.
type Options struct {
	// APIURL is the internal endpoint (DM_BURST_API_URL).
	APIURL *url.URL
	Keys   burst.Keys
	// Harness is the local harness (gRPC over its Unix socket).
	Harness harness.Client
	// MediaRoot is DM_MEDIA_LOCAL_DIR, shared with the harness: inputs are
	// staged under it for the duration of a job.
	MediaRoot string
	Identity  Identity
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

	mu     sync.Mutex
	reason string
	cancel context.CancelFunc
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

// Run claims jobs until ctx ends (nil or ctx.Err()), Once/MaxIdle say so or
// the instance drains.
func (a *Agent) Run(ctx context.Context) error {
	if a.o.IMDS != nil {
		go a.watch(ctx)
	}
	a.o.Logger.Info("trabalhador sob demanda pronto", slog.String("instance", a.o.Identity.InstanceID), slog.String("type", a.o.Identity.InstanceType), slog.String("api", a.o.APIURL.Host))
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

// ---------------------------------------------------------------- calls

// errGone is a job the API dropped (cancelled, lost, requeued): stop quietly.
var errGone = errors.New("agent: trabalho encerrado pela API")

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

// do runs a job call and maps its status.
func (a *Agent) do(ctx context.Context, path string, body any, token string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := a.call(ctx, http.MethodPost, path, body, token)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusGone:
		return errGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("agent: %s respondeu %d", path, resp.StatusCode)
	}
	return nil
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

	req, err := a.request(m)
	if err != nil {
		a.finish(tail, m, "failed", err.Error(), log)
		return
	}
	staged := &harness.Staged{Client: a.o.Harness, Root: a.o.MediaRoot, Open: a.opener(m), MaxBytes: maxSize(m)}
	stream, err := staged.Run(jobCtx, req)
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
	var pending [][]byte
	flush := func(c context.Context) error {
		if len(pending) == 0 {
			return nil
		}
		err := a.do(c, burst.JobPath(m.JobID, "events"), burst.EventsRequest{Events: pending}, m.Token)
		if err == nil {
			pending = pending[:0]
		}
		return err
	}
	for {
		select {
		case <-ticker.C:
			if err := flush(jobCtx); errors.Is(err, errGone) {
				log.Info("trabalho encerrado pela API; harness interrompido")
				return
			}
			continue
		case r := <-events:
			if r.err != nil {
				_ = flush(tail)
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
			if err != nil {
				a.finish(tail, m, "failed", "serializar evento: "+err.Error(), log)
				return
			}
			pending = append(pending, raw)
			if terminal(r.ev) {
				if err := flush(tail); err != nil {
					a.finish(tail, m, "failed", "enviar o evento final: "+err.Error(), log)
					return
				}
				a.finish(tail, m, "done", "", log)
				return
			}
			if len(pending) >= a.o.FlushEvents {
				if err := flush(jobCtx); errors.Is(err, errGone) {
					return
				}
			}
			if len(pending) > maxPending {
				a.finish(tail, m, "failed", "a API não recebe os eventos", log)
				return
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
	if outcome == "interrupted" && reason == "" || outcome == "interrupted" && strings.Contains(reason, "context canceled") {
		reason = a.drainReason()
	}
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
		if err := a.do(ctx, burst.JobPath(m.JobID, "heartbeat"), struct{}{}, m.Token); errors.Is(err, errGone) {
			dropped()
			return
		} else if err != nil && ctx.Err() == nil {
			a.o.Logger.Warn("renovar a concessão falhou", slog.String("job_id", m.JobID), slog.String("err", err.Error()))
		}
	}
}

// request decodes the job's request and names its sources by position.
func (a *Agent) request(m burst.Manifest) (*pb.RunRequest, error) {
	req := &pb.RunRequest{}
	if err := proto.Unmarshal(m.Request, req); err != nil {
		return nil, fmt.Errorf("pedido ilegível: %w", err)
	}
	if len(req.GetSources()) != len(m.Sources) {
		return nil, fmt.Errorf("manifesto com %d fontes para %d no pedido", len(m.Sources), len(req.GetSources()))
	}
	for i, src := range req.GetSources() {
		c, ok := proto.Clone(src).(*pb.Source)
		if !ok {
			return nil, errors.New("cópia da fonte falhou")
		}
		c.Uri = sourceScheme + strconv.Itoa(i)
		req.Sources[i] = c
	}
	return req, nil
}

func maxSize(m burst.Manifest) int64 {
	n := int64(harness.DefaultStagingMaxBytes)
	for _, s := range m.Sources {
		n = max(n, s.Size)
	}
	return n
}

func terminal(ev *pb.RunEvent) bool {
	switch ev.GetEvent().(type) {
	case *pb.RunEvent_Completed, *pb.RunEvent_Failed, *pb.RunEvent_NeedsApproval:
		return true
	}
	return false
}

// opener downloads the inputs of m: presigned URLs straight from the store,
// internal-endpoint URLs with the job token. Each copy is checked against
// the SHA-256 the upload was confirmed with.
func (a *Agent) opener(m burst.Manifest) harness.Opener {
	return func(ctx context.Context, uri string) (io.ReadCloser, bool, error) {
		idx, ok := strings.CutPrefix(uri, sourceScheme)
		if !ok {
			return nil, false, nil
		}
		i, err := strconv.Atoi(idx)
		if err != nil || i < 0 || i >= len(m.Sources) {
			return nil, false, fmt.Errorf("fonte %q fora do manifesto", uri)
		}
		src := m.Sources[i]
		u, err := a.o.APIURL.Parse(src.URL)
		if err != nil {
			return nil, false, fmt.Errorf("fonte %d: URL inválida", i)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, false, err
		}
		client := a.dl
		if src.Bearer {
			client = a.api
			a.o.Keys.SignRequest(req, nil, a.o.Identity.InstanceID, time.Now())
			req.Header.Set("Authorization", "Bearer "+m.Token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, false, fmt.Errorf("fonte %d: %w", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, false, fmt.Errorf("fonte %d: download respondeu %d", i, resp.StatusCode)
		}
		return &verified{rc: resp.Body, want: strings.ToLower(src.SHA256), h: sha256.New()}, true, nil
	}
}

// verified hashes what it reads and fails at EOF when the digest differs.
type verified struct {
	rc   io.ReadCloser
	want string
	h    hash.Hash
}

func (v *verified) Read(p []byte) (int, error) {
	n, err := v.rc.Read(p)
	v.h.Write(p[:n])
	if errors.Is(err, io.EOF) && v.want != "" && hex.EncodeToString(v.h.Sum(nil)) != v.want {
		return n, errors.New("agent: SHA-256 da fonte não confere com o upload")
	}
	return n, err
}

func (v *verified) Close() error { return v.rc.Close() }
