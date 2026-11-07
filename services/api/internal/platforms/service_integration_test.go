//go:build integration

package platforms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }
func fixture(t *testing.T) *Service {
	t.Helper()
	d := testdb.Get(t)
	if _, err := d.Admin.Exec(t.Context(), `TRUNCATE platform_notices,platform_notice_sends; DELETE FROM outbox WHERE payload->>'tag' IN ('platform_confirmation','platform_ready')`); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://docmatch.example.test")
	cfg := &config.Config{Env: "dev", PublicBaseURL: base}
	cfg.Auth.SessionSecret = config.SecretBytes(bytes.Repeat([]byte{9}, 32))
	cfg.Email.From = &mail.Address{Address: "no-reply@example.test"}
	cfg.Email.SMTPAddr = "localhost:1025"
	s, err := New(cfg, d.App)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func message(t *testing.T, address, tag string) (uuid.UUID, email.Message) {
	t.Helper()
	var id uuid.UUID
	var payload []byte
	if err := testdb.Get(t).Admin.QueryRow(t.Context(), `SELECT id,payload FROM outbox WHERE payload->>'to'=$1 AND payload->>'tag'=$2 ORDER BY created_at DESC,id DESC LIMIT 1`, address, tag).Scan(&id, &payload); err != nil {
		t.Fatal(err)
	}
	var m email.Message
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	return id, m
}
func linkToken(t *testing.T, m email.Message, action string) string {
	t.Helper()
	for _, line := range strings.Split(m.Text, "\n") {
		if strings.Contains(line, "/avisos#"+action+"=") {
			u, err := url.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimPrefix(u.Fragment, action+"=")
		}
	}
	t.Fatal("no token link")
	return ""
}
func count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testdb.Get(t).Admin.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func available(s *Service) {
	for i := range s.catalog {
		if s.catalog[i].ID == "android" {
			s.catalog[i].Status = "available"
			s.catalog[i].URL = "https://play.google.com/store/apps/details?id=br.com.docmatch"
		}
	}
}

type recorder struct {
	messages []email.Message
	failure  error
}

func (r *recorder) Send(_ context.Context, _ string, m email.Message) error {
	r.messages = append(r.messages, m)
	return r.failure
}

func TestConfirmationCancellationExpiryAndProviderFailure(t *testing.T) {
	for _, scenario := range []string{"cancelled", "expired", "retry"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixture(t)
			ctx := t.Context()
			address := "pending@example.test"
			must(t, s.Subscribe(ctx, "android", address, "192.0.2.11", true))
			queued, m := message(t, address, email.TagPlatformConfirmation)
			sender := &recorder{}
			deliverer := emailpg.NewDeliverer(testdb.Get(t).App, sender, nil)
			switch scenario {
			case "cancelled":
				must(t, s.Unsubscribe(ctx, linkToken(t, m, "cancelar")))
				_, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE outbox SET sent_at=NULL WHERE id=$1`, queued)
				must(t, err)
			case "expired":
				_, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE platform_notices SET confirmation_expires_at=now()-interval '1 second'`)
				must(t, err)
			case "retry":
				sender.failure = errors.New("temporary provider failure")
				if deliverer.SendOne(ctx, queued) == nil {
					t.Fatal("provider failure lost")
				}
				if count(t, `SELECT count(*) FROM outbox WHERE id=$1 AND sent_at IS NULL AND last_error IS NOT NULL`, queued) != 1 {
					t.Fatal("failed mail was not retained for retry")
				}
				sender.failure = nil
			}
			must(t, deliverer.SendOne(ctx, queued))
			must(t, deliverer.SendOne(ctx, queued))
			want := 0
			if scenario == "retry" {
				want = 2
			}
			if len(sender.messages) != want {
				t.Fatalf("delivery attempts=%d, want %d", len(sender.messages), want)
			}
		})
	}
}

func TestMonthlyBudgetAlsoBoundsReleaseMail(t *testing.T) {
	s := fixture(t)
	ctx := t.Context()
	must(t, s.Subscribe(ctx, "android", "monthly@example.test", "192.0.2.12", true))
	_, confirmation := message(t, "monthly@example.test", email.TagPlatformConfirmation)
	_, err := s.Confirm(ctx, linkToken(t, confirmation, "confirmar"))
	must(t, err)
	_, err = testdb.Get(t).Admin.Exec(ctx, `INSERT INTO platform_notice_sends(id,email_hash,created_at) SELECT gen_random_uuid(),decode(repeat('00',32),'hex'),now()-interval '2 days' FROM generate_series(1,$1)`, MonthlyMailLimit-1)
	must(t, err)
	if s.Subscribe(ctx, "ios", "over-budget@example.test", "192.0.2.13", true) == nil {
		t.Fatal("monthly budget ignored")
	}
	available(s)
	must(t, s.Notify(ctx))
	if count(t, `SELECT count(*) FROM outbox WHERE payload->>'tag'='platform_ready'`) != 0 {
		t.Fatal("release mail bypassed shared budget")
	}
	_, err = testdb.Get(t).Admin.Exec(ctx, `UPDATE platform_notice_sends SET created_at=now()-interval '31 days' WHERE ip_hash IS NULL`)
	must(t, err)
	must(t, s.Notify(ctx))
	if count(t, `SELECT count(*) FROM outbox WHERE payload->>'tag'='platform_ready'`) != 1 {
		t.Fatal("waiting notice never resumed")
	}
}

func TestConfirmationIsSeparateIdempotentAndDeliveryRespectsConsent(t *testing.T) {
	s := fixture(t)
	ctx := t.Context()
	address := "reader@example.test"
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.Subscribe(ctx, "android", address, "192.0.2.1", true) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	if count(t, `SELECT count(*) FROM platform_notices`) != 1 || count(t, `SELECT count(*) FROM platform_notice_sends`) != 1 {
		t.Fatal("duplicate signup")
	}
	_, mail := message(t, address, email.TagPlatformConfirmation)
	token := linkToken(t, mail, "confirmar")
	unsubscribe := linkToken(t, mail, "cancelar")
	var storedHash []byte
	must(t, testdb.Get(t).Admin.QueryRow(ctx, `SELECT confirmation_hash FROM platform_notices`).Scan(&storedHash))
	if len(storedHash) != 32 || !bytes.Equal(storedHash, crypto.Digest(token)) {
		t.Fatal("token was not stored as a digest")
	}
	var status string
	must(t, testdb.Get(t).Admin.QueryRow(ctx, `SELECT status FROM platform_notices`).Scan(&status))
	if status != "pending" {
		t.Fatal("email alone confirmed consent")
	}
	var visible int
	must(t, testdb.Get(t).App.QueryRow(ctx, `SELECT count(*) FROM platform_notices`).Scan(&visible))
	if visible != 0 {
		t.Fatal("notice addresses bypassed RLS")
	}
	available(s)
	must(t, s.Notify(ctx))
	if count(t, `SELECT count(*) FROM outbox WHERE payload->>'tag'='platform_ready'`) != 0 {
		t.Fatal("unconfirmed notice sent")
	}
	for range 2 {
		p, err := s.Confirm(ctx, token)
		must(t, err)
		if p != "android" {
			t.Fatal(p)
		}
	}
	must(t, s.Notify(ctx))
	must(t, s.Notify(ctx))
	queued, _ := message(t, address, email.TagPlatformReady)
	if count(t, `SELECT count(*) FROM outbox WHERE payload->>'tag'='platform_ready'`) != 1 {
		t.Fatal("notice repeated")
	}
	// A worker may have claimed the message before cancellation; delivery still checks live consent.
	must(t, s.Unsubscribe(ctx, unsubscribe))
	must(t, s.Unsubscribe(ctx, unsubscribe))
	_, err := s.Confirm(ctx, token)
	if err == nil {
		t.Fatal("cancelled request reconfirmed")
	}
	_, err = testdb.Get(t).Admin.Exec(ctx, `UPDATE outbox SET sent_at=NULL WHERE id=$1`, queued)
	must(t, err)
	sender := &recorder{}
	deliverer := emailpg.NewDeliverer(testdb.Get(t).App, sender, nil)
	must(t, deliverer.SendOne(ctx, queued))
	if len(sender.messages) != 0 {
		t.Fatal("queued email survived unsubscribe")
	}
	if _, err := s.Confirm(ctx, "invalid"); err == nil {
		t.Fatal("bad token")
	}
	if err := s.Unsubscribe(ctx, token); err == nil {
		t.Fatal("confirmation token accepted for unsubscribe")
	}
	if err := s.Unsubscribe(ctx, strings.Repeat("x", 257)); err == nil {
		t.Fatal("oversized token")
	}
}
func TestNoticeDeliveryOnceAndRetention(t *testing.T) {
	s := fixture(t)
	ctx := t.Context()
	address := "ready@example.test"
	must(t, s.Subscribe(ctx, "android", address, "192.0.2.2", true))
	_, m := message(t, address, email.TagPlatformConfirmation)
	_, err := s.Confirm(ctx, linkToken(t, m, "confirmar"))
	must(t, err)
	available(s)
	must(t, s.Notify(ctx))
	queued, m := message(t, address, email.TagPlatformReady)
	if !strings.Contains(m.Text, s.catalog[0].URL) {
		t.Fatal("missing official destination")
	}
	sender := &recorder{}
	deliverer := emailpg.NewDeliverer(testdb.Get(t).App, sender, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- deliverer.SendOne(ctx, queued) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	must(t, deliverer.SendOne(ctx, queued))
	if len(sender.messages) != 1 {
		t.Fatal("delivery repeated")
	}
	// Outbox retention cannot make a previously queued notice eligible again.
	_, err = testdb.Get(t).Admin.Exec(ctx, `DELETE FROM outbox WHERE id=$1`, queued)
	must(t, err)
	must(t, s.Notify(ctx))
	if count(t, `SELECT count(*) FROM outbox WHERE payload->>'tag'='platform_ready'`) != 0 {
		t.Fatal("retention caused another notice")
	}
	_, err = testdb.Get(t).Admin.Exec(ctx, `UPDATE platform_notices SET expires_at=now()-interval '1 second'; UPDATE platform_notice_sends SET created_at=now()-interval '32 days'; UPDATE outbox SET created_at=now()-interval '8 days' WHERE payload->>'tag'='platform_confirmation'`)
	must(t, err)
	must(t, s.Cleanup(ctx))
	if count(t, `SELECT count(*) FROM platform_notices`)+count(t, `SELECT count(*) FROM platform_notice_sends`)+count(t, `SELECT count(*) FROM outbox WHERE payload->>'tag'='platform_confirmation'`) != 0 {
		t.Fatal("expired personal data retained")
	}
	must(t, s.Unsubscribe(ctx, linkToken(t, m, "cancelar")))
	workers := river.NewWorkers()
	AddWorkers(s)(workers)
	if len(PeriodicJobs()) != 1 {
		t.Fatal("job registration")
	}
	must(t, (&noticeWorker{service: s}).Work(ctx, nil))
}
func TestResendInvalidatesOldTokenAndBoundsEmail(t *testing.T) {
	s := fixture(t)
	ctx := t.Context()
	address := "retry@example.test"
	must(t, s.Subscribe(ctx, "android", address, "192.0.2.3", true))
	_, first := message(t, address, email.TagPlatformConfirmation)
	must(t, s.Subscribe(ctx, "android", address, "192.0.2.3", true))
	if count(t, `SELECT count(*) FROM platform_notice_sends`) != 1 {
		t.Fatal("cooldown ignored")
	}
	_, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE platform_notices SET updated_at=now()-interval '16 minutes'`)
	must(t, err)
	must(t, s.Subscribe(ctx, "android", address, "192.0.2.3", true))
	_, second := message(t, address, email.TagPlatformConfirmation)
	if _, err := s.Confirm(ctx, linkToken(t, first, "confirmar")); err == nil {
		t.Fatal("resend retained old token")
	}
	_, err = testdb.Get(t).Admin.Exec(ctx, `UPDATE platform_notices SET updated_at=now()-interval '16 minutes'`)
	must(t, err)
	must(t, s.Subscribe(ctx, "android", address, "192.0.2.3", true))
	if count(t, `SELECT count(*) FROM platform_notice_sends`) != 2 {
		t.Fatal("per-address cap ignored")
	}
	_, err = testdb.Get(t).Admin.Exec(ctx, `UPDATE platform_notices SET confirmation_expires_at=now()-interval '1 second'`)
	must(t, err)
	if _, err := s.Confirm(ctx, linkToken(t, second, "confirmar")); err == nil {
		t.Fatal("expired confirmation")
	}
	for i := range 7 {
		must(t, s.Subscribe(ctx, "ios", fmt.Sprintf("person%d@example.test", i), "192.0.2.4", true))
	}
	if count(t, `SELECT count(*) FROM platform_notice_sends`) != 7 {
		t.Fatal("per-IP cap ignored")
	}
	for i := range DailyMailLimit {
		err := s.Subscribe(ctx, "chrome", fmt.Sprintf("budget%d@example.test", i), fmt.Sprintf("192.0.2.%d", i+10), true)
		if i < DailyMailLimit-7 {
			must(t, err)
		}
	}
	if count(t, `SELECT count(*) FROM platform_notice_sends`) != DailyMailLimit {
		t.Fatal("global cap ignored")
	}
	if err := s.Subscribe(ctx, "chrome", "last@example.test", "192.0.2.200", true); err == nil {
		t.Fatal("global cap not reported")
	}
}
func TestPublicHTTPDoesNotConfirmFromGETOrForeignOrigin(t *testing.T) {
	s := fixture(t)
	router := httpx.NewRouter(limits.Must())
	s.Mount(router)
	handler := httpx.OriginCheckMiddleware([]string{"https://docmatch.example.test"})(router)
	call := func(method, path, body, origin string, want int) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.90:1234"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s = %d; want %d: %s", method, path, res.Code, want, res.Body.String())
		}
	}
	origin := "https://docmatch.example.test"
	call("GET", "/v1/platforms", "", "", 200)
	call("GET", "/v1/platform-notices/confirm?token=ignored", "", "", 405)
	call("POST", "/v1/platform-notices", `{"platform":"android","email":"http@example.test","consent":true}`, "https://foreign.example", 403)
	call("POST", "/v1/platform-notices", "{broken", origin, 400)
	call("POST", "/v1/platform-notices", `{"platform":"android","email":"http@example.test","consent":false}`, origin, 400)
	call("POST", "/v1/platform-notices", `{"platform":"unknown","email":"http@example.test","consent":true}`, origin, 400)
	call("POST", "/v1/platform-notices", `{"platform":"android","email":"http@example.test","consent":true}`, origin, 202)
	_, m := message(t, "http@example.test", email.TagPlatformConfirmation)
	confirm, _ := json.Marshal(map[string]string{"token": linkToken(t, m, "confirmar")})
	cancel, _ := json.Marshal(map[string]string{"token": linkToken(t, m, "cancelar")})
	call("POST", "/v1/platform-notices/confirm", string(confirm), origin, 200)
	call("POST", "/v1/platform-notices/unsubscribe", string(cancel), origin, 204)
	s.mailReady = false
	if err := s.Subscribe(t.Context(), "android", "new@example.test", "192.0.2.91", true); err == nil {
		t.Fatal("disabled mail accepted")
	}
	must(t, s.Notify(t.Context()))
	if _, err := New(&config.Config{}, nil); err == nil {
		t.Fatal("missing key accepted")
	}
	if err := s.Unsubscribe(t.Context(), crypto.SignToken(s.tokenKey, []byte("bad uuid"), time.Now().Add(time.Hour))); err == nil {
		t.Fatal("invalid payload accepted")
	}
}
