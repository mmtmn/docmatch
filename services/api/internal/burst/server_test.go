package burst

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

type serverFixture struct {
	t     *testing.T
	st    *MemStore
	keys  Keys
	srv   *Server
	h     http.Handler
	clock *clock
	run   uuid.UUID
}

func newServerFixture(t *testing.T) *serverFixture {
	t.Helper()
	s := testSettings()
	s.LongPoll = 10 * time.Millisecond
	f := &serverFixture{t: t, st: NewMemStore(), keys: mustKeys(t, testSecret), clock: &clock{now: time.Now().UTC()}, run: uuid.New()}
	f.srv = &Server{
		Store: f.st, Keys: f.keys, Settings: s, Now: f.clock.Now,
		Presign: func(_ context.Context, uri string, _ time.Duration) (string, bool, error) {
			if strings.HasPrefix(uri, "s3://") {
				return "https://s3.example/" + strings.TrimPrefix(uri, "s3://") + "?X-Amz-Signature=x", true, nil
			}
			if strings.HasPrefix(uri, "bad://") {
				return "", false, errors.New("assinar falhou")
			}
			return "", false, nil
		},
		Open: func(_ context.Context, uri string) (io.ReadCloser, bool, error) {
			if uri == "local://t/a.csv" {
				return io.NopCloser(strings.NewReader("a;b\n1;2\n")), true, nil
			}
			return nil, false, nil
		},
	}
	f.h = f.srv.Handler()
	return f
}

func (f *serverFixture) queue(sources ...string) Job {
	f.t.Helper()
	req := &pb.RunRequest{RunId: apix.Encode(apix.PrefixRun, f.run)}
	for _, u := range sources {
		req.Sources = append(req.Sources, &pb.Source{Id: "upl_1", Uri: u, SizeBytes: 8, Sha256: "ab"})
	}
	raw, _ := proto.Marshal(req)
	j, err := f.st.CreateJob(context.Background(), Job{ID: uuid.New(), OrgID: uuid.New(), RunID: f.run, Request: raw, DeadlineSeconds: 7200, CreatedAt: f.clock.Now()})
	if err != nil {
		f.t.Fatal(err)
	}
	return j
}

func (f *serverFixture) do(method, path string, body any, instance, token string) *httptest.ResponseRecorder {
	f.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	f.keys.SignRequest(req, raw, instance, f.clock.Now())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *serverFixture) claim(instance string) Manifest {
	f.t.Helper()
	rec := f.do(http.MethodPost, PathClaim, ClaimRequest{InstanceType: "c7g.large", Lifecycle: "spot"}, instance, "")
	if rec.Code != http.StatusOK {
		f.t.Fatalf("claim = %d %s", rec.Code, rec.Body)
	}
	var sm SignedManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &sm); err != nil {
		f.t.Fatal(err)
	}
	m, err := f.keys.OpenManifest(sm, instance, f.clock.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func TestServerClaimHandsASignedManifest(t *testing.T) {
	t.Parallel()
	f := newServerFixture(t)
	if rec := f.do(http.MethodPost, PathClaim, nil, "i-1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("fila vazia = %d", rec.Code)
	}
	job := f.queue("s3://uploads/quarantine/a", "local://t/a.csv")
	m := f.claim("i-1")
	if m.JobID != job.ID.String() || m.RunID != apix.Encode(apix.PrefixRun, f.run) || m.LeaseSeconds != 90 || m.HeartbeatSeconds != 15 {
		t.Fatalf("manifesto = %+v", m)
	}
	if !strings.HasPrefix(m.Sources[0].URL, "https://s3.example/") || m.Sources[0].Bearer || m.Sources[0].SHA256 != "ab" {
		t.Fatalf("fonte S3 deveria sair pré-assinada: %+v", m.Sources[0])
	}
	if m.Sources[1].URL != JobPath(m.JobID, "sources/1") || !m.Sources[1].Bearer {
		t.Fatalf("fonte local deveria sair pelo endpoint: %+v", m.Sources[1])
	}
	req := &pb.RunRequest{}
	if err := proto.Unmarshal(m.Request, req); err != nil {
		t.Fatal(err)
	}
	// The deadline counts from the claim, not from the queue.
	if d := req.GetOptions().GetDeadline().AsTime().Sub(f.clock.Now()); d != 2*time.Hour {
		t.Fatalf("prazo = %v", d)
	}
	got, _, _ := f.st.GetJob(context.Background(), job.ID)
	if got.State != StateClaimed || got.InstanceID != "i-1" || !SameToken(m.Token, got.TokenHash) {
		t.Fatalf("job = %+v", got)
	}
	status, _ := f.st.Status(context.Background(), f.srv.Settings.Query(f.clock.Now()))
	if status.Live != 1 || status.Busy != 1 || status.Idle != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestServerJobCalls(t *testing.T) {
	t.Parallel()
	f := newServerFixture(t)
	f.queue("local://t/a.csv")
	m := f.claim("i-1")
	runID := m.RunID
	ev := func(seq uint64, run string) []byte {
		raw, _ := proto.Marshal(&pb.RunEvent{RunId: run, Seq: seq, Event: &pb.RunEvent_Progress{Progress: &pb.Progress{}}})
		return raw
	}
	path := func(action string) string { return JobPath(m.JobID, action) }
	if rec := f.do(http.MethodPost, path("heartbeat"), struct{}{}, "i-1", m.Token); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, path("events"), EventsRequest{Events: [][]byte{ev(1, runID), ev(2, runID)}}, "i-1", m.Token); rec.Code != http.StatusOK {
		t.Fatalf("events = %d %s", rec.Code, rec.Body)
	}
	evs, _ := f.st.Events(context.Background(), uuid.MustParse(m.JobID), 0, 10)
	if len(evs) != 2 {
		t.Fatalf("eventos gravados = %d", len(evs))
	}
	for name, body := range map[string]any{
		"outra execução": EventsRequest{Events: [][]byte{ev(3, "run_outra")}},
		"seq zero":       EventsRequest{Events: [][]byte{ev(0, runID)}},
		"lixo":           EventsRequest{Events: [][]byte{{0xff, 0xff}}},
		"json errado":    "x",
	} {
		if rec := f.do(http.MethodPost, path("events"), body, "i-1", m.Token); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
	rec := f.do(http.MethodGet, path("sources/0"), nil, "i-1", m.Token)
	if rec.Code != http.StatusOK || rec.Body.String() != "a;b\n1;2\n" {
		t.Fatalf("fonte = %d %q", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, path("sources/9"), nil, "i-1", m.Token); rec.Code != http.StatusNotFound {
		t.Fatalf("fonte inexistente = %d", rec.Code)
	}
	// Someone else's token, another instance or no token: refused.
	if rec := f.do(http.MethodPost, path("heartbeat"), struct{}{}, "i-1", "token-errado"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token errado = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, path("heartbeat"), struct{}{}, "i-2", m.Token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("outra instância = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, path("heartbeat"), struct{}{}, "i-1", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem token = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, JobPath(uuid.NewString(), "heartbeat"), struct{}{}, "i-1", m.Token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("trabalho inexistente = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, path("finish"), FinishRequest{Outcome: "talvez"}, "i-1", m.Token); rec.Code != http.StatusBadRequest {
		t.Fatalf("outcome inválido = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, path("finish"), FinishRequest{Outcome: "interrupted", Reason: strings.Repeat("x", 900)}, "i-1", m.Token); rec.Code != http.StatusOK {
		t.Fatalf("finish = %d %s", rec.Code, rec.Body)
	}
	got, _, _ := f.st.GetJob(context.Background(), uuid.MustParse(m.JobID))
	if got.State != StateInterrupted || len(got.Reason) != 500 {
		t.Fatalf("job = %s %d", got.State, len(got.Reason))
	}
	// A finished job answers 410: the worker stops.
	if rec := f.do(http.MethodPost, path("heartbeat"), struct{}{}, "i-1", m.Token); rec.Code != http.StatusGone {
		t.Fatalf("depois do fim = %d", rec.Code)
	}
}

func TestServerRefusesUnsignedCalls(t *testing.T) {
	t.Parallel()
	f := newServerFixture(t)
	req := httptest.NewRequest(http.MethodPost, PathClaim, nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem assinatura = %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, PathHealth, nil, "i-1", ""); rec.Code != http.StatusOK {
		t.Fatalf("health = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, PathClaim, "não é objeto", "i-1", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("corpo inválido = %d", rec.Code)
	}
	big := httptest.NewRequest(http.MethodPost, PathClaim, bytes.NewReader(make([]byte, maxSmallBody+1)))
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corpo grande = %d", rec.Code)
	}
}

func TestServerCancelsAJobItCannotDescribe(t *testing.T) {
	t.Parallel()
	f := newServerFixture(t)
	job := f.queue("bad://x/y")
	if rec := f.do(http.MethodPost, PathClaim, nil, "i-1", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("claim = %d", rec.Code)
	}
	got, _, _ := f.st.GetJob(context.Background(), job.ID)
	if got.State != StateCancelled {
		t.Fatalf("o trabalho sem manifesto não pode ficar entregue: %s", got.State)
	}
}

func TestServerServesTLSUntilCancelled(t *testing.T) {
	t.Parallel()
	f := newServerFixture(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.srv.ServeListener(ctx, ln) }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: f.keys.ClientTLS()}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, "https://"+ln.Addr().String()+PathHealth, nil)
	f.keys.SignRequest(req, nil, "i-1", time.Now())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health por TLS = %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("encerrar: %v", err)
	}
	if err := f.srv.Serve(context.Background(), "nao-e-um-endereco"); err == nil {
		t.Fatal("endereço inválido deveria falhar")
	}
	if (&Server{}).now().IsZero() || (&Server{}).log() == nil {
		t.Fatal("padrões")
	}
}
