package uploads

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
)

func repoPricing(t testing.TB) *pricing.Pricing {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "config", "pricing.yaml")
		if _, err := os.Stat(p); err == nil {
			pr, err := pricing.Load(p, pricing.Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			return pr
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("config/pricing.yaml não encontrado")
	return nil
}

func TestSniffAndFamilies(t *testing.T) {
	t.Parallel()
	cases := map[string]Detected{
		"%PDF-1.7 x":                        {"application/pdf", familyPDF},
		"PK\x03\x04rest":                    {"application/zip", familyZip},
		"\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1x": {"application/vnd.ms-excel", familyOLE},
		"\xFF\xD8\xFFxx":                    {"image/jpeg", familyImage},
		"\x89PNG\r\n\x1a\nxx":               {"image/png", familyImage},
		"GIF89a":                            {"image/gif", familyImage},
		"RIFF....WAVEfmt":                   {"audio/wav", familyAudio},
		"RIFF....WEBPx":                     {"image/webp", familyImage},
		"ID3\x03":                           {"audio/mpeg", familyAudio},
		"OggS":                              {"audio/ogg", familyAudio},
		"fLaC":                              {"audio/flac", familyAudio},
		"\x00\x00\x00\x18ftypM4A ":          {"audio/mp4", familyAudio},
		"\x00\x00\x00\x18ftypisom":          {"video/mp4", familyVideo},
		"\x1A\x45\xDF\xA3x":                 {"video/webm", familyVideo},
		"data;valor\n1;2\n":                 {"text/plain", familyText},
		"\x00\x01\x02":                      {"application/octet-stream", familyUnknown},
		"":                                  {"application/octet-stream", familyUnknown},
	}
	for in, want := range cases {
		if got := Sniff([]byte(in)); got != want {
			t.Errorf("Sniff(%q) = %+v, esperado %+v", in, got, want)
		}
	}
	if FamilyOf("text/csv") != familyText || FamilyOf("IMAGE/PNG") != familyImage || FamilyOf("application/x-evil") != familyUnknown {
		t.Fatal("FamilyOf")
	}
	if !AcceptedMime(KindDocument, "application/pdf") || AcceptedMime(KindDocument, "audio/mpeg") || !AcceptedMime(KindMedia, "video/mp4") || AcceptedMime(KindMedia, "text/csv") || AcceptedMime("x", "text/csv") {
		t.Fatal("AcceptedMime")
	}
	if !IsPlaylist([]byte("#EXTM3U\nhttp://x/a.mp3")) || !IsPlaylist([]byte("[playlist]\nFile1=x")) || !IsPlaylist([]byte("https://a/b.mp3\nrtsp://c/d\n")) || IsPlaylist([]byte("data;valor\n1;2")) || IsPlaylist([]byte("")) {
		t.Fatal("IsPlaylist")
	}
	if !compatible(familyAudio, familyVideo) || compatible(familyPDF, familyZip) {
		t.Fatal("compatible")
	}
	// Text cut mid-rune still counts as text.
	if !isText([]byte("olá"[:3])) {
		t.Fatal("utf-8 cortada")
	}
}

func writeZip(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, _ := zw.Create(name)
		_, _ = w.Write(data)
	}
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckZip(t *testing.T) {
	t.Parallel()
	l := limits.Must()
	fine := writeZip(t, map[string][]byte{"a.csv": []byte("1;2\n"), "b.txt": []byte("oi")})
	if r := CheckZip(fine, 100, l); r != "" {
		t.Fatalf("zip comum: %s", r)
	}
	nested := writeZip(t, map[string][]byte{"inner.zip": []byte("PK")})
	if r := CheckZip(nested, 100, l); !strings.Contains(r, "zip dentro") {
		t.Fatalf("aninhado: %s", r)
	}
	traversal := writeZip(t, map[string][]byte{"../evil": []byte("x")})
	if r := CheckZip(traversal, 100, l); !strings.Contains(r, "perigoso") {
		t.Fatalf("traversal: %s", r)
	}
	bomb := writeZip(t, map[string][]byte{"zeros.bin": bytes.Repeat([]byte{0}, 2<<20)})
	st, _ := os.Stat(bomb)
	if r := CheckZip(bomb, st.Size(), l); !strings.Contains(r, "razão") {
		t.Fatalf("bomba: %q", r)
	}
	if r := CheckZip(filepath.Join(t.TempDir(), "nope.zip"), 1, l); r == "" {
		t.Fatal("ilegível")
	}
}

type fixture struct {
	svc    *Service
	store  *MemStore
	local  *storage.LocalStore
	jobs   *jobargs.Recorder
	server *httptest.Server
	now    time.Time
	org    uuid.UUID
}

func newFixture(t *testing.T, scanner ScanRunner) *fixture {
	t.Helper()
	var local *storage.LocalStore
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { local.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	var err error
	local, err = storage.NewLocal(t.TempDir(), "uploads", base)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: NewMemStore(), local: local, jobs: &jobargs.Recorder{}, server: srv, now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC), org: uuid.New()}
	f.svc, err = New(Deps{Store: f.store, Stores: storage.Stores{Documents: local, Media: local}, Pricing: repoPricing(t), Scanner: scanner, Jobs: f.jobs, Logger: logx.Discard(), Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) principal() apix.Principal {
	user := uuid.New()
	return apix.Principal{OrgID: f.org, UserID: &user, Plan: "starter"}
}

func assertCode(t *testing.T, err error, code contracts.ErrorCode) {
	t.Helper()
	var ae *apix.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("erro = %v, esperado %s", err, code)
	}
}

func TestCreateTicketValidation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	p := f.principal()
	ok := TicketRequest{Kind: KindDocument, FileName: "extrato.csv", SizeBytes: 100, Mime: "text/csv"}
	cases := []struct {
		name string
		p    apix.Principal
		req  TicketRequest
		code contracts.ErrorCode
	}{
		{"kind", p, TicketRequest{Kind: "x", FileName: "a", SizeBytes: 1, Mime: "text/csv"}, contracts.ErrValidationFailed},
		{"nome com barra", p, TicketRequest{Kind: KindDocument, FileName: "a/b.csv", SizeBytes: 1, Mime: "text/csv"}, contracts.ErrUploadRejectedName},
		{"nome com controle", p, TicketRequest{Kind: KindDocument, FileName: "a\x00b", SizeBytes: 1, Mime: "text/csv"}, contracts.ErrUploadRejectedName},
		{"nome longo", p, TicketRequest{Kind: KindDocument, FileName: strings.Repeat("é", 200), SizeBytes: 1, Mime: "text/csv"}, contracts.ErrUploadRejectedName},
		{"tamanho zero", p, TicketRequest{Kind: KindDocument, FileName: "a.csv", SizeBytes: 0, Mime: "text/csv"}, contracts.ErrValidationFailed},
		{"acima do plano", p, TicketRequest{Kind: KindDocument, FileName: "a.csv", SizeBytes: 26 << 20, Mime: "text/csv"}, contracts.ErrFileTooLarge},
		{"mime inválido", p, TicketRequest{Kind: KindDocument, FileName: "a.csv", SizeBytes: 1, Mime: "text csv"}, contracts.ErrValidationFailed},
		{"mime não aceito", p, TicketRequest{Kind: KindDocument, FileName: "a.exe", SizeBytes: 1, Mime: "application/x-msdownload"}, contracts.ErrUploadRejectedType},
		{"sha inválido", p, TicketRequest{Kind: KindDocument, FileName: "a.csv", SizeBytes: 1, Mime: "text/csv", SHA256: "zz"}, contracts.ErrValidationFailed},
		{"role inválido", p, TicketRequest{Kind: KindDocument, FileName: "a.csv", SizeBytes: 1, Mime: "text/csv", Role: "Bank"}, contracts.ErrValidationFailed},
		{"plano desconhecido", apix.Principal{OrgID: f.org, Plan: "vip"}, ok, contracts.ErrPlanUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := f.svc.CreateTicket(context.Background(), tc.p, tc.req)
			assertCode(t, err, tc.code)
		})
	}
	ticket, u, err := f.svc.CreateTicket(context.Background(), p, ok)
	if err != nil {
		t.Fatal(err)
	}
	doc, isDoc := ticket.(DocumentTicket)
	if !isDoc || doc.Method != http.MethodPost || doc.ID != u.PublicID() || doc.MaxSizeBytes != 25<<20 || doc.Fields["key"] == "" {
		t.Fatalf("ticket = %+v", ticket)
	}
	if u.Status != StatusPending || u.Store != "local" || !strings.HasPrefix(u.StorageKey, "quarantine/") || u.ExpiresAt == nil || u.ExpiresAt.Sub(f.now) != 30*24*time.Hour {
		t.Fatalf("upload = %+v", u)
	}
	media, mu, err := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindMedia, FileName: "áudio.mp3", SizeBytes: 5000, Mime: "audio/mpeg", Role: "call"})
	if err != nil {
		t.Fatal(err)
	}
	mt, isMedia := media.(MediaTicket)
	if !isMedia || mt.Method != http.MethodPut || mt.Headers["Content-Length"] != "5000" || mu.Kind != KindMedia || mu.Role != "call" {
		t.Fatalf("ticket de mídia = %+v %+v", media, mu)
	}
	view := u.View(f.now)
	if view.Status != APIPending || view.ID != u.PublicID() || view.SHA256 != "" {
		t.Fatalf("view = %+v", view)
	}
	later := f.now.Add(2 * time.Hour)
	if u.View(later).Status != APIExpired {
		t.Fatal("ticket vencido deveria aparecer como expired")
	}
}

// putObject stores bytes through the local store directly (the client's PUT already happened).
func (f *fixture) putObject(t *testing.T, u Upload, data []byte) {
	t.Helper()
	if err := f.local.Put(context.Background(), u.StorageKey, bytes.NewReader(data), int64(len(data)), ""); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteAndScan(t *testing.T) {
	t.Parallel()
	f := newFixture(t, NewLocalScanner("dm-scanner-inexistente", nil, logx.Discard()))
	p := f.principal()
	content := []byte("data;valor\n2026-07-10;24300,00\n")
	sum := sha256.Sum256(content)
	_, u, err := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindDocument, FileName: "extrato.csv", SizeBytes: int64(len(content)), Mime: "text/csv"})
	if err != nil {
		t.Fatal(err)
	}
	// Object not there yet.
	_, _, err = f.svc.Complete(context.Background(), p, u.ID, CompleteRequest{})
	assertCode(t, err, contracts.ErrUploadIncomplete)
	// Expired ticket and still no object.
	f.now = f.now.Add(2 * time.Hour)
	_, _, err = f.svc.Complete(context.Background(), p, u.ID, CompleteRequest{})
	assertCode(t, err, contracts.ErrUploadExpired)
	f.now = f.now.Add(-2 * time.Hour)
	// Wrong checksum.
	f.putObject(t, u, content)
	_, _, err = f.svc.Complete(context.Background(), p, u.ID, CompleteRequest{SHA256: strings.Repeat("0", 64)})
	assertCode(t, err, contracts.ErrUploadChecksumMismatch)
	got, _ := f.svc.Get(context.Background(), f.org, u.ID)
	if got.Status != StatusRejected || got.RejectedRsn != ReasonChecksum {
		t.Fatalf("após checksum errado: %+v", got)
	}
	// Fresh upload with the right checksum: scanning + job.
	_, u2, _ := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindDocument, FileName: "extrato.csv", SizeBytes: int64(len(content)), Mime: "text/csv", SHA256: hex.EncodeToString(sum[:])})
	f.putObject(t, u2, content)
	done, finished, err := f.svc.Complete(context.Background(), p, u2.ID, CompleteRequest{})
	if err != nil || finished || done.Status != StatusScanning || hex.EncodeToString(done.SHA256) != hex.EncodeToString(sum[:]) {
		t.Fatalf("complete = %+v %v %v", done, finished, err)
	}
	if kinds := f.jobs.Kinds(); len(kinds) != 1 || kinds[0] != jobargs.KindScan {
		t.Fatalf("jobs = %v", kinds)
	}
	// Calling complete again while scanning is a no-op 202; the scan job accepts the file.
	if _, finished, err := f.svc.Complete(context.Background(), p, u2.ID, CompleteRequest{}); err != nil || finished {
		t.Fatal("complete repetido")
	}
	if err := f.svc.RunScan(context.Background(), f.org, u2.ID); err != nil {
		t.Fatal(err)
	}
	clean, _ := f.svc.Get(context.Background(), f.org, u2.ID)
	if clean.Status != StatusClean || clean.DetectedMime != "text/csv" || clean.ScannedAt == nil {
		t.Fatalf("após scan: %+v", clean)
	}
	if _, finished, _ := f.svc.Complete(context.Background(), p, u2.ID, CompleteRequest{}); !finished {
		t.Fatal("complete de upload pronto deveria ser final")
	}
	ready, err := f.svc.ListReady(context.Background(), f.org, []uuid.UUID{u2.ID})
	if err != nil || len(ready) != 1 {
		t.Fatalf("ListReady = %v %v", ready, err)
	}
	_, err = f.svc.ListReady(context.Background(), f.org, []uuid.UUID{u.ID})
	assertCode(t, err, contracts.ErrUploadNotReady)
	_, err = f.svc.ListReady(context.Background(), f.org, []uuid.UUID{uuid.New()})
	assertCode(t, err, contracts.ErrUploadNotFound)
	if err := f.svc.RunScan(context.Background(), f.org, u2.ID); err != nil {
		t.Fatal("scan repetido deveria ser inofensivo")
	}
	// Declared PDF with CSV bytes: rejected by type.
	_, u3, _ := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindDocument, FileName: "fake.pdf", SizeBytes: int64(len(content)), Mime: "application/pdf"})
	f.putObject(t, u3, content)
	if _, _, err := f.svc.Complete(context.Background(), p, u3.ID, CompleteRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RunScan(context.Background(), f.org, u3.ID); err != nil {
		t.Fatal(err)
	}
	rejected, _ := f.svc.Get(context.Background(), f.org, u3.ID)
	if rejected.Status != StatusRejected || rejected.RejectedRsn != ReasonType || rejected.View(f.now).RejectedReason != ReasonType {
		t.Fatalf("tipo falso: %+v", rejected)
	}
	// Playlist pretending to be text.
	playlist := []byte("#EXTM3U\nhttps://cdn/x.mp3\n")
	_, u4, _ := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindDocument, FileName: "lista.txt", SizeBytes: int64(len(playlist)), Mime: "text/plain"})
	f.putObject(t, u4, playlist)
	_, _, _ = f.svc.Complete(context.Background(), p, u4.ID, CompleteRequest{})
	_ = f.svc.RunScan(context.Background(), f.org, u4.ID)
	if pl, _ := f.svc.Get(context.Background(), f.org, u4.ID); pl.RejectedRsn != ReasonPlaylist {
		t.Fatalf("playlist: %+v", pl)
	}
	// Object larger than declared is rejected by size at complete.
	_, u5, _ := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindDocument, FileName: "big.csv", SizeBytes: 10, Mime: "text/csv"})
	f.putObject(t, u5, bytes.Repeat([]byte("x"), 26<<20))
	big, finished, err := f.svc.Complete(context.Background(), p, u5.ID, CompleteRequest{})
	if err != nil || !finished || big.RejectedRsn != ReasonSize {
		t.Fatalf("grande = %+v %v %v", big, finished, err)
	}
	// Unknown upload and other tenant.
	_, _, err = f.svc.Complete(context.Background(), p, uuid.New(), CompleteRequest{})
	assertCode(t, err, contracts.ErrUploadNotFound)
	_, _, err = f.svc.Complete(context.Background(), apix.Principal{OrgID: uuid.New(), Plan: "starter"}, u2.ID, CompleteRequest{})
	assertCode(t, err, contracts.ErrUploadNotFound)
	if f.svc.StoreFor(u2) != f.local {
		t.Fatal("StoreFor")
	}
}

func TestVerdicts(t *testing.T) {
	t.Parallel()
	id := apix.Encode(apix.PrefixUpload, uuid.New())
	good := Verdict{Version: 1, UploadID: id, Verdict: VerdictAccept, SHA256: strings.Repeat("a", 64), Mime: "text/csv"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Verdict{
		{Version: 2, UploadID: id, Verdict: VerdictAccept, SHA256: strings.Repeat("a", 64)},
		{Version: 1, UploadID: "x", Verdict: VerdictAccept, SHA256: strings.Repeat("a", 64)},
		{Version: 1, UploadID: id, Verdict: VerdictAccept, SHA256: "short"},
		{Version: 1, UploadID: id, Verdict: VerdictAcceptSanitized, SHA256: strings.Repeat("a", 64)},
		{Version: 1, UploadID: id, Verdict: VerdictReject},
		{Version: 1, UploadID: id, Verdict: "maybe"},
	}
	for i, v := range bad {
		if err := v.Validate(); !errors.Is(err, ErrInvalidVerdict) {
			t.Errorf("caso %d deveria falhar: %v", i, err)
		}
	}
	if (Verdict{ReasonCode: "bomb"}).RejectedReason() != ReasonBomb || (Verdict{ReasonCode: "unreadable"}).RejectedReason() != ReasonType || (Verdict{ReasonCode: "weird"}).RejectedReason() != ReasonMalware {
		t.Fatal("RejectedReason")
	}
	data, _ := json.Marshal(good)
	if _, err := ParseVerdict(data); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseVerdict([]byte("{")); err == nil {
		t.Fatal("json inválido")
	}

	f := newFixture(t, QueueScanner{})
	p := f.principal()
	content := []byte("a;b\n")
	sum := sha256.Sum256(content)
	_, u, _ := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindDocument, FileName: "a.csv", SizeBytes: int64(len(content)), Mime: "text/csv"})
	f.putObject(t, u, content)
	if _, _, err := f.svc.Complete(context.Background(), p, u.ID, CompleteRequest{}); err != nil {
		t.Fatal(err)
	}
	// The queue scanner leaves the upload in scanning: the verdict arrives later.
	if err := f.svc.RunScan(context.Background(), f.org, u.ID); err != nil {
		t.Fatal(err)
	}
	if still, _ := f.svc.Get(context.Background(), f.org, u.ID); still.Status != StatusScanning {
		t.Fatal("deveria continuar em verificação")
	}
	dur := int64(1200)
	applied, err := f.svc.ApplyVerdict(context.Background(), Verdict{Version: 1, UploadID: u.PublicID(), Verdict: VerdictAcceptSanitized, SHA256: hex.EncodeToString(sum[:]), Mime: "text/csv", DurationMs: &dur, Rebuilt: []RebuiltOutput{{Kind: "extract", Bucket: "b", Key: "extracts/x"}}})
	if err != nil || applied.Status != StatusClean || applied.DurationMs == nil || !bytes.Contains(applied.ScanResult, []byte("extracts/x")) {
		t.Fatalf("accept_sanitized = %+v %v", applied, err)
	}
	// Replayed verdicts are harmless; unknown uploads are reported.
	if again, err := f.svc.ApplyVerdict(context.Background(), Verdict{Version: 1, UploadID: u.PublicID(), Verdict: VerdictReject, ReasonCode: "malware"}); err != nil || again.Status != StatusClean {
		t.Fatalf("replay = %+v %v", again, err)
	}
	if _, err := f.svc.ApplyVerdict(context.Background(), Verdict{Version: 1, UploadID: apix.Encode(apix.PrefixUpload, uuid.New()), Verdict: VerdictReject, ReasonCode: "malware"}); !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("desconhecido = %v", err)
	}
	if _, err := f.svc.ApplyVerdict(context.Background(), Verdict{}); err == nil {
		t.Fatal("inválido")
	}
	// Malware verdict → infected.
	_, u2, _ := f.svc.CreateTicket(context.Background(), p, TicketRequest{Kind: KindDocument, FileName: "b.csv", SizeBytes: int64(len(content)), Mime: "text/csv"})
	f.putObject(t, u2, content)
	_, _, _ = f.svc.Complete(context.Background(), p, u2.ID, CompleteRequest{})
	infected, err := f.svc.ApplyVerdict(context.Background(), Verdict{Version: 1, UploadID: u2.PublicID(), Verdict: VerdictReject, ReasonCode: "malware"})
	if err != nil || infected.Status != StatusInfected || infected.View(f.now).Status != APIRejected || infected.View(f.now).RejectedReason != ReasonMalware {
		t.Fatalf("malware = %+v %v", infected, err)
	}
}

type fakeSQS struct {
	msgs    []sqstypes.Message
	deleted []string
	calls   int
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.calls++
	if f.calls > 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &sqs.ReceiveMessageOutput{Messages: f.msgs}, nil
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

type fakeApplier struct {
	err error
}

func (a fakeApplier) ApplyVerdict(context.Context, Verdict) (Upload, error) { return Upload{}, a.err }

func TestSQSConsumer(t *testing.T) {
	t.Parallel()
	id := apix.Encode(apix.PrefixUpload, uuid.New())
	good, _ := json.Marshal(Verdict{Version: 1, UploadID: id, Verdict: VerdictAccept, SHA256: strings.Repeat("a", 64)})
	run := func(applier VerdictApplier) *fakeSQS {
		client := &fakeSQS{msgs: []sqstypes.Message{
			{Body: aws.String(string(good)), ReceiptHandle: aws.String("ok")},
			{Body: aws.String("{nope"), ReceiptHandle: aws.String("poison")},
		}}
		c := &SQSConsumer{Client: client, QueueURL: "q", Applier: applier, Logger: logx.Discard()}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = c.Run(ctx)
		return client
	}
	if c := run(fakeApplier{}); len(c.deleted) != 2 {
		t.Fatalf("aplicado + veneno deveriam ser apagados: %v", c.deleted)
	}
	if c := run(fakeApplier{err: errors.New("banco fora")}); len(c.deleted) != 1 || c.deleted[0] != "poison" {
		t.Fatalf("falha transitória deveria manter a mensagem: %v", c.deleted)
	}
	if c := run(fakeApplier{err: ErrUploadNotFound}); len(c.deleted) != 2 {
		t.Fatalf("upload desconhecido deveria ser apagado: %v", c.deleted)
	}
	if _, err := NewSQSConsumer(context.Background(), "", "", nil, nil); err == nil {
		t.Fatal("fila vazia")
	}
}

func TestHandler(t *testing.T) {
	t.Parallel()
	f := newFixture(t, NewLocalScanner("", nil, logx.Discard()))
	p := f.principal()
	h := NewHandler(f.svc, apix.Static(p))
	r := httpx.NewRouter(limits.Must())
	h.Mount(r)
	srv := httptest.NewServer(httpx.Chain(r, httpx.RequestIDMiddleware))
	t.Cleanup(srv.Close)
	post := func(path, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	resp, out := post("/v1/uploads", `{"kind":"document","file_name":"a.csv","size_bytes":4,"mime":"text/csv"}`)
	if resp.StatusCode != http.StatusCreated || out["method"] != "POST" {
		t.Fatalf("create = %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	if resp, out := post("/v1/uploads", `{"kind":"x","file_name":"a.csv","size_bytes":1,"mime":"text/csv"}`); resp.StatusCode != http.StatusBadRequest || out["code"] != "validation_failed" {
		t.Fatalf("inválido = %d %v", resp.StatusCode, out)
	}
	if resp, out := post("/v1/uploads", `{"kind":"document","file_name":"a.csv","size_bytes":4,"mime":"text/csv","extra":1}`); resp.StatusCode != http.StatusBadRequest || out["code"] != "invalid_json" {
		t.Fatalf("campo extra = %d %v", resp.StatusCode, out)
	}
	if resp, out := post("/v1/uploads/"+id+"/complete", ""); resp.StatusCode != http.StatusConflict || out["code"] != "upload_incomplete" {
		t.Fatalf("complete sem objeto = %d %v", resp.StatusCode, out)
	}
	uid, _ := apix.Decode(apix.PrefixUpload, id)
	u, _ := f.svc.Get(context.Background(), f.org, uid)
	f.putObject(t, u, []byte("a;b\n"))
	if resp, out := post("/v1/uploads/"+id+"/complete", `{}`); resp.StatusCode != http.StatusAccepted || out["status"] != "scanning" {
		t.Fatalf("complete = %d %v", resp.StatusCode, out)
	}
	if resp, out := post("/v1/uploads/upl_nope/complete", ""); resp.StatusCode != http.StatusNotFound || out["code"] != "upload_not_found" {
		t.Fatalf("id inválido = %d %v", resp.StatusCode, out)
	}
	anon := NewHandler(f.svc, nil)
	r2 := httpx.NewRouter(limits.Must())
	anon.Mount(r2)
	rec := httptest.NewRecorder()
	r2.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/uploads", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem auth = %d", rec.Code)
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	if _, err := New(Deps{}); err == nil {
		t.Fatal("deps vazias")
	}
	if ValidateFileNameOK("extrato.csv") != nil {
		t.Fatal("nome válido")
	}
}

// ValidateFileNameOK is a small helper exercising ValidateFileName.
func ValidateFileNameOK(name string) *apix.Error {
	_, err := ValidateFileName(name, 255)
	return err
}
