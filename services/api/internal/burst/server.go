package burst

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Body limits of the internal endpoint.
const (
	maxSmallBody = 64 << 10
	// presignTTL bounds the source URLs of a manifest (the worker downloads
	// them right after the claim).
	presignTTL = 15 * time.Minute
	// reasonMax bounds a finish reason or a reported error.
	reasonMax = 500
)

// ClaimRequest is the body of a claim.
type ClaimRequest struct {
	InstanceType string `json:"instance_type,omitempty"`
	Lifecycle    string `json:"lifecycle,omitempty"`
}

// EventsRequest is the body of an events post: pb.RunEvent, protobuf.
type EventsRequest struct {
	Events [][]byte `json:"events"`
}

// FinishRequest is the body of a finish.
type FinishRequest struct {
	// Outcome is done, interrupted or failed.
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// HelloRequest is the worker's sign of life: its version and the last error
// it hit (kept in burst_instances: a Spot instance's journal dies with it).
type HelloRequest struct {
	Version      string `json:"version,omitempty"`
	InstanceType string `json:"instance_type,omitempty"`
	Lifecycle    string `json:"lifecycle,omitempty"`
	LastError    string `json:"last_error,omitempty"`
}

// Presigner returns a short-lived download URL for a storage URI; ok is false
// when the store has none (the endpoint then serves the bytes itself).
type Presigner func(ctx context.Context, uri string, ttl time.Duration) (url string, ok bool, err error)

// Opener opens a storage URI (the endpoint streams it to the worker).
type Opener func(ctx context.Context, uri string) (rc io.ReadCloser, ok bool, err error)

// Server is the internal endpoint the burst workers call (DM_BURST_LISTEN,
// reachable from the VPC only). Every call is signed with the pool key, and
// the headers are checked before any body is read; job calls also carry the
// job's bearer token. Claims and event posts run under small semaphores, one
// open claim per instance.
type Server struct {
	Store    Store
	Keys     Keys
	Settings Settings
	Presign  Presigner
	Open     Opener
	Logger   *slog.Logger
	Now      func() time.Time

	once      sync.Once
	claims    chan struct{}
	events    chan struct{}
	mu        sync.Mutex
	claimants map[string]bool
}

func (s *Server) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Server) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

func (s *Server) init() {
	s.once.Do(func() {
		st := s.Settings.WithDefaults()
		s.claims = make(chan struct{}, st.ClaimSlots)
		s.events = make(chan struct{}, st.EventSlots)
		s.claimants = map[string]bool{}
	})
}

// Handler routes the endpoint.
func (s *Server) Handler() http.Handler {
	s.init()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathHealth, s.health)
	mux.HandleFunc("POST "+PathHello, s.hello)
	mux.HandleFunc("POST "+PathClaim, s.claim)
	mux.HandleFunc("POST "+PathPrefix+"jobs/{id}/heartbeat", s.heartbeat)
	mux.HandleFunc("POST "+PathPrefix+"jobs/{id}/events", s.postEvents)
	mux.HandleFunc("POST "+PathPrefix+"jobs/{id}/finish", s.finish)
	mux.HandleFunc("GET "+PathPrefix+"jobs/{id}/sources/{n}", s.source)
	return mux
}

// Serve listens on addr with the pinned TLS key until ctx ends.
func (s *Server) Serve(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("burst: escutar em %s: %w", addr, err)
	}
	return s.ServeListener(ctx, ln)
}

// ServeListener serves on ln (tests pass a loopback listener).
func (s *Server) ServeListener(ctx context.Context, ln net.Listener) error {
	tlsCfg, err := s.Keys.ServerTLS(s.now())
	if err != nil {
		_ = ln.Close()
		return err
	}
	srv := &http.Server{
		Handler: s.Handler(), TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 16 << 10,
		ErrorLog:       slog.NewLogLogger(s.log().Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(stop)
	}()
	s.log().Info("endpoint interno do pool sob demanda pronto", slog.String("addr", ln.Addr().String()))
	if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// authenticate checks the signature headers first (format, instance, date),
// only then reads the body (bounded) and checks the MAC.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, string, bool) {
	h, err := s.Keys.CheckHeaders(r, s.now())
	if err != nil {
		s.refuse(w, r, err)
		return nil, "", false
	}
	if r.ContentLength > limit {
		fail(w, http.StatusRequestEntityTooLarge, "corpo grande demais")
		return nil, "", false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "corpo grande demais")
		return nil, "", false
	}
	if err := s.Keys.VerifyBody(h, r, body); err != nil {
		s.refuse(w, r, err)
		return nil, "", false
	}
	return body, h.Instance, true
}

func (s *Server) refuse(w http.ResponseWriter, r *http.Request, err error) {
	s.log().Warn("pedido ao endpoint interno recusado", slog.String("path", r.URL.Path), slog.String("err", err.Error()), slog.String("remote", r.RemoteAddr))
	fail(w, http.StatusUnauthorized, "assinatura inválida")
}

// known reports whether an instance may use the endpoint: in a managed pool,
// only one the scaler saw in the Auto Scaling group.
func (s *Server) known(ctx context.Context, instance string) (bool, error) {
	if !s.Settings.Managed {
		return true, nil
	}
	return s.Store.InstanceSeen(ctx, instance, s.Settings.LiveWindow)
}

// touch records a manual pool's instance at a claim or a job call (a
// managed pool's instances come from the scaler).
func (s *Server) touch(ctx context.Context, in Instance) {
	if s.Settings.Managed {
		return
	}
	if err := s.Store.TouchInstance(ctx, in); err != nil {
		s.log().Warn("registrar instância do pool falhou", slog.String("instance", in.ID), slog.String("err", err.Error()))
	}
}

// job authenticates a job call: signature, then the bearer token of a job
// still claimed by the calling instance.
func (s *Server) job(w http.ResponseWriter, r *http.Request, limit int64) (Job, []byte, bool) {
	body, instance, ok := s.authenticate(w, r, limit)
	if !ok {
		return Job{}, nil, false
	}
	id, err := uuid.Parse(r.PathValue("id"))
	token, hasToken := BearerToken(r)
	if err != nil || !hasToken {
		fail(w, http.StatusUnauthorized, "trabalho ou token ausente")
		return Job{}, nil, false
	}
	j, found, err := s.Store.GetJob(r.Context(), id)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "banco indisponível")
		return Job{}, nil, false
	}
	if !found || !SameToken(token, j.TokenHash) || j.InstanceID != instance {
		fail(w, http.StatusUnauthorized, "token inválido para este trabalho")
		return Job{}, nil, false
	}
	if j.State != StateClaimed {
		// Dropped (cancelled, lost, requeued): the worker stops.
		fail(w, http.StatusGone, "trabalho encerrado: "+j.State)
		return Job{}, nil, false
	}
	s.touch(r.Context(), Instance{ID: instance})
	return j, body, true
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authenticate(w, r, maxSmallBody); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": ProtocolVersion})
}

// hello records the worker's sign of life (version, last error).
func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	body, instance, ok := s.authenticate(w, r, maxSmallBody)
	if !ok {
		return
	}
	var req HelloRequest
	if len(body) > 0 && json.Unmarshal(body, &req) != nil {
		fail(w, http.StatusBadRequest, "corpo inválido")
		return
	}
	if known, err := s.known(r.Context(), instance); err != nil || !known {
		fail(w, http.StatusForbidden, "instância fora do grupo do pool")
		return
	}
	c := Contact{InstanceID: instance, Version: req.Version, LastError: Sanitize(req.LastError)}
	if err := s.Store.NoteContact(r.Context(), c, !s.Settings.Managed); err != nil {
		fail(w, http.StatusServiceUnavailable, "banco indisponível")
		return
	}
	if c.LastError != "" {
		s.log().Warn("trabalhador do pool relatou um erro", slog.String("instance", instance), slog.String("err", c.LastError))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// enter takes one slot of a semaphore without waiting; false means busy.
func enter(sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func leave(sem chan struct{}) { <-sem }

// claim long-polls for a queued job and answers its signed manifest, or 204.
func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	body, instance, ok := s.authenticate(w, r, maxSmallBody)
	if !ok {
		return
	}
	var req ClaimRequest
	if len(body) > 0 && json.Unmarshal(body, &req) != nil {
		fail(w, http.StatusBadRequest, "corpo inválido")
		return
	}
	if known, err := s.known(r.Context(), instance); err != nil || !known {
		fail(w, http.StatusForbidden, "instância fora do grupo do pool")
		return
	}
	s.mu.Lock()
	open := s.claimants[instance]
	if !open {
		s.claimants[instance] = true
	}
	s.mu.Unlock()
	if open {
		fail(w, http.StatusConflict, "já há um pedido aberto desta instância")
		return
	}
	defer func() {
		s.mu.Lock()
		delete(s.claimants, instance)
		s.mu.Unlock()
	}()
	if !enter(s.claims) {
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "endpoint ocupado")
		return
	}
	defer leave(s.claims)
	// The long poll is a transport timeout: wall clock, not the business clock.
	deadline := time.Now().Add(s.Settings.LongPoll)
	for {
		sm, found, err := s.tryClaim(r.Context(), Instance{ID: instance, Type: req.InstanceType, Lifecycle: req.Lifecycle})
		if err != nil {
			s.log().Error("entregar trabalho do pool falhou", slog.String("instance", instance), slog.String("err", err.Error()))
			fail(w, http.StatusServiceUnavailable, "não foi possível entregar o trabalho")
			return
		}
		if found {
			writeJSON(w, http.StatusOK, sm)
			return
		}
		if !time.Now().Before(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(min(time.Second, s.Settings.LongPoll)):
		}
	}
}

// tryClaim claims the oldest queued job for the instance and builds its
// manifest. A job whose manifest cannot be built is cancelled, never left
// claimed.
func (s *Server) tryClaim(ctx context.Context, in Instance) (SignedManifest, bool, error) {
	token, hash, err := NewToken()
	if err != nil {
		return SignedManifest{}, false, err
	}
	job, found, err := s.Store.Claim(ctx, in.ID, hash, s.Settings.Lease)
	if err != nil || !found {
		return SignedManifest{}, false, err
	}
	s.touch(ctx, in)
	if err := s.Store.NoteClaim(ctx); err != nil {
		s.log().Warn("zerar falhas do pool falhou", slog.String("err", err.Error()))
	}
	sm, err := s.manifest(ctx, job, token, s.now())
	if err != nil {
		_, _ = s.Store.Cancel(ctx, job.ID, Sanitize("manifesto inválido: "+err.Error()))
		return SignedManifest{}, false, err
	}
	s.log().Info("trabalho do pool entregue", slog.String("job_id", job.ID.String()), slog.String("instance", in.ID), slog.String("run_id", apix.Encode(apix.PrefixRun, job.RunID)))
	return sm, true, nil
}

// manifest stamps the deadline (from the claim, so the wait for a worker does
// not eat the run's time) and resolves where each source is downloaded from.
func (s *Server) manifest(ctx context.Context, job Job, token string, now time.Time) (SignedManifest, error) {
	req := &pb.RunRequest{}
	if err := proto.Unmarshal(job.Request, req); err != nil {
		return SignedManifest{}, fmt.Errorf("pedido ilegível: %w", err)
	}
	if req.Options == nil {
		req.Options = &pb.RunOptions{}
	}
	req.Options.Deadline = timestamppb.New(now.Add(time.Duration(job.DeadlineSeconds) * time.Second))
	sources := make([]Source, 0, len(req.GetSources()))
	for i, src := range req.GetSources() {
		out := Source{URI: src.GetUri(), Size: int64(min(src.GetSizeBytes(), 1<<62)), SHA256: src.GetSha256()} //nolint:gosec // bounded
		u, ok, err := s.presign(ctx, src.GetUri())
		if err != nil {
			return SignedManifest{}, fmt.Errorf("fonte %d: %w", i, err)
		}
		if ok {
			out.URL = u
		} else {
			out.URL, out.Bearer = JobPath(job.ID.String(), "sources/"+strconv.Itoa(i)), true
		}
		sources = append(sources, out)
	}
	data, err := proto.Marshal(req)
	if err != nil {
		return SignedManifest{}, err
	}
	return s.Keys.SignManifest(Manifest{
		Version: ProtocolVersion, JobID: job.ID.String(), RunID: apix.Encode(apix.PrefixRun, job.RunID), InstanceID: job.InstanceID,
		IssuedAt: now, ExpiresAt: now.Add(s.Settings.ManifestTTL),
		LeaseSeconds: int(s.Settings.Lease / time.Second), HeartbeatSeconds: max(int(s.Settings.Heartbeat/time.Second), 1),
		Token: token, Request: data, Sources: sources,
	})
}

func (s *Server) presign(ctx context.Context, uri string) (string, bool, error) {
	if s.Presign == nil {
		return "", false, nil
	}
	return s.Presign(ctx, uri, presignTTL)
}

// heartbeat renews the lease.
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	j, _, ok := s.job(w, r, maxSmallBody)
	if !ok {
		return
	}
	s.extend(w, r, j)
}

func (s *Server) extend(w http.ResponseWriter, r *http.Request, j Job) {
	ok, err := s.Store.Extend(r.Context(), j.ID, j.TokenHash, s.Settings.Lease)
	switch {
	case err != nil:
		fail(w, http.StatusServiceUnavailable, "banco indisponível")
	case !ok:
		fail(w, http.StatusGone, "trabalho encerrado")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"state": StateClaimed, "lease_seconds": int(s.Settings.Lease / time.Second)})
	}
}

// postEvents stores a batch of harness events (idempotent by seq, within the
// job's quota) and renews the lease. At most EventSlots posts are processed
// at once; the rest get 503 and are retried by the worker.
func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) {
	if !enter(s.events) {
		w.Header().Set("Retry-After", "1")
		fail(w, http.StatusServiceUnavailable, "endpoint ocupado")
		return
	}
	defer leave(s.events)
	j, body, ok := s.job(w, r, MaxEventsBody)
	if !ok {
		return
	}
	var req EventsRequest
	if err := json.Unmarshal(body, &req); err != nil || len(req.Events) > MaxEventsPerCall {
		fail(w, http.StatusBadRequest, "lote de eventos inválido")
		return
	}
	runID := apix.Encode(apix.PrefixRun, j.RunID)
	evs := make([]Event, 0, len(req.Events))
	for _, raw := range req.Events {
		if len(raw) > MaxEventBytes {
			fail(w, http.StatusBadRequest, "evento grande demais")
			return
		}
		ev := &pb.RunEvent{}
		if err := proto.Unmarshal(raw, ev); err != nil || ev.GetSeq() == 0 || ev.GetSeq() > 1<<62 || ev.GetRunId() != runID {
			fail(w, http.StatusBadRequest, "evento inválido")
			return
		}
		evs = append(evs, Event{Seq: int64(ev.GetSeq()), Data: raw}) //nolint:gosec // bounded above
	}
	if err := s.Store.AppendEvents(r.Context(), j, evs, s.Settings.Quota); err != nil {
		if errors.Is(err, ErrEventQuota) {
			fail(w, http.StatusRequestEntityTooLarge, "o trabalho passou da cota de eventos")
			return
		}
		s.log().Error("gravar eventos do pool falhou", slog.String("job_id", j.ID.String()), slog.String("err", err.Error()))
		fail(w, http.StatusServiceUnavailable, "banco indisponível")
		return
	}
	s.extend(w, r, j)
}

// finish ends the job with the worker's outcome.
func (s *Server) finish(w http.ResponseWriter, r *http.Request) {
	j, body, ok := s.job(w, r, maxSmallBody)
	if !ok {
		return
	}
	var req FinishRequest
	if json.Unmarshal(body, &req) != nil {
		fail(w, http.StatusBadRequest, "corpo inválido")
		return
	}
	state := map[string]string{"done": StateDone, "interrupted": StateInterrupted, "failed": StateFailed}[req.Outcome]
	if state == "" {
		fail(w, http.StatusBadRequest, "outcome precisa ser done, interrupted ou failed")
		return
	}
	reason := Sanitize(req.Reason)
	done, err := s.Store.Finish(r.Context(), j.ID, j.TokenHash, state, reason)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "banco indisponível")
		return
	}
	if !done {
		fail(w, http.StatusGone, "trabalho encerrado")
		return
	}
	s.log().Info("trabalho do pool terminou", slog.String("job_id", j.ID.String()), slog.String("state", state), slog.String("reason", reason))
	writeJSON(w, http.StatusOK, map[string]string{"state": state})
}

// source streams one input of the job (stores without presigned URLs: the
// local store of dev and tests).
func (s *Server) source(w http.ResponseWriter, r *http.Request) {
	j, _, ok := s.job(w, r, maxSmallBody)
	if !ok {
		return
	}
	req := &pb.RunRequest{}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || proto.Unmarshal(j.Request, req) != nil || n < 0 || n >= len(req.GetSources()) || s.Open == nil {
		fail(w, http.StatusNotFound, "fonte não encontrada")
		return
	}
	rc, found, err := s.Open(r.Context(), req.GetSources()[n].GetUri())
	if err != nil || !found {
		fail(w, http.StatusNotFound, "fonte não encontrada")
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, rc); err != nil {
		s.log().Warn("enviar fonte ao trabalhador falhou", slog.String("job_id", j.ID.String()), slog.String("err", err.Error()))
	}
}

// queryRe matches the query of a URL: presigned URLs carry their signature
// there, and an error message must never keep it.
var queryRe = regexp.MustCompile(`(https?://[^\s"'?]+)\?[^\s"']*`)

// Sanitize strips URL queries (presigned signatures) and cuts a reason to
// reasonMax bytes on a rune boundary.
func Sanitize(reason string) string {
	return Truncate(queryRe.ReplaceAllString(reason, "$1"), reasonMax)
}
