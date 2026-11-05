//go:build integration

package emailpg

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func getDB(t *testing.T) *testdb.DB {
	t.Helper()
	if os.Getenv(testdb.EnvDatabaseURL) == "" && os.Getenv("DOCKER_HOST") == "" {
		if _, err := os.Stat("/var/run/docker.sock"); err != nil {
			t.Skip("Docker indisponível (sem /var/run/docker.sock e sem DOCKER_HOST): defina DM_TEST_DATABASE_URL ou suba o Docker")
		}
	}
	return testdb.Get(t)
}

type fakeSender struct {
	mu   sync.Mutex
	sent []string // idempotency keys
	err  error
}

func (f *fakeSender) Send(_ context.Context, key string, m email.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, key+":"+m.To)
	return nil
}

func outboxRow(t *testing.T, d *testdb.DB, id uuid.UUID) sqlc.Outbox {
	t.Helper()
	row, err := sqlc.New(d.Admin).AuthOutboxByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestEnqueueAndDeliver(t *testing.T) {
	d := getDB(t)
	ctx := context.Background()
	inserter, err := NewInserter(d.App)
	if err != nil {
		t.Fatal(err)
	}
	msg := email.Message{To: "a@example.com", Subject: "Oi", Text: "corpo", Tag: "login"}
	var id uuid.UUID
	err = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = Enqueue(ctx, tx, inserter, nil, msg)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if row := outboxRow(t, d, id); row.Kind != email.OutboxKind || row.SentAt != nil {
		t.Fatalf("outbox: %+v", row)
	}
	var jobs int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'outbox_id' = $2", email.KindSend, id.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("job do River: %d %v", jobs, err)
	}
	// Invalid messages never reach the outbox.
	err = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		_, err := Enqueue(ctx, tx, nil, nil, email.Message{To: "x"})
		return err
	})
	if !errors.Is(err, email.ErrInvalidTo) {
		t.Fatalf("mensagem inválida: %v", err)
	}
	sender := &fakeSender{}
	deliverer := NewDeliverer(d.App, sender, logx.Discard())
	if err := deliverer.SendOne(ctx, id); err != nil {
		t.Fatal(err)
	}
	if row := outboxRow(t, d, id); row.SentAt == nil || row.Attempts != 1 {
		t.Fatalf("enviado: %+v", row)
	}
	if len(sender.sent) != 1 || sender.sent[0] != id.String()+":a@example.com" {
		t.Fatalf("envios: %v", sender.sent)
	}
	// Sending again is a no-op (idempotent), as is an unknown id.
	if err := deliverer.SendOne(ctx, id); err != nil || len(sender.sent) != 1 {
		t.Fatalf("reenvio: %v %v", err, sender.sent)
	}
	if err := deliverer.SendOne(ctx, uuid.New()); err != nil {
		t.Fatal(err)
	}
	// A failing sender schedules a retry with backoff.
	sender.err = errors.New("provedor fora")
	var id2 uuid.UUID
	_ = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		id2, err = Enqueue(ctx, tx, nil, nil, msg)
		return err
	})
	if err := deliverer.SendOne(ctx, id2); err == nil {
		t.Fatal("falha do provedor deveria propagar")
	}
	row := outboxRow(t, d, id2)
	if row.SentAt != nil || row.LastError == nil || !row.AvailableAt.After(time.Now().Add(20*time.Second)) {
		t.Fatalf("retry: %+v", row)
	}
	// The sweep picks rows that waited long enough.
	sender.err = nil
	if _, err := d.Admin.Exec(ctx, "UPDATE outbox SET available_at = now() - interval '10 minutes' WHERE id = $1", id2); err != nil {
		t.Fatal(err)
	}
	sent, failed, err := deliverer.Sweep(ctx, email.SweepMinAge, email.SweepBatch)
	if err != nil || sent != 1 || failed != 0 {
		t.Fatalf("sweep: %d %d %v", sent, failed, err)
	}
	if row := outboxRow(t, d, id2); row.SentAt == nil {
		t.Fatal("varredura deveria enviar")
	}
	// Without a sender the row stays and the error is clear.
	none := NewDeliverer(d.App, nil, nil)
	var id3 uuid.UUID
	_ = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		id3, err = Enqueue(ctx, tx, nil, nil, msg)
		return err
	})
	if err := none.SendOne(ctx, id3); !errors.Is(err, email.ErrNotConfigured) {
		t.Fatalf("sem remetente: %v", err)
	}
	// A corrupt payload is retried later instead of crashing the worker.
	if _, err := d.Admin.Exec(ctx, "UPDATE outbox SET payload = '{\"to\":5}'::jsonb, available_at = now() - interval '10 minutes', sent_at = NULL WHERE id = $1", id3); err != nil {
		t.Fatal(err)
	}
	if err := deliverer.SendOne(ctx, id3); err == nil {
		t.Fatal("payload inválido")
	}
	if (email.SendArgs{}).Kind() != email.KindSend || len(PeriodicJobs()) != 1 {
		t.Fatal("jobs")
	}
	if _, err := NewInserter(nil); err == nil {
		t.Fatal("pool nulo")
	}
}

func TestEnqueueOnceAndTxMailer(t *testing.T) {
	d := getDB(t)
	ctx := context.Background()
	inserter, err := NewInserter(d.App)
	if err != nil {
		t.Fatal(err)
	}
	org := uuid.New()
	if err := db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, 'Org', $2)", org, "org-"+org.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	user := uuid.New()
	if _, err := d.Admin.Exec(ctx, "INSERT INTO users (id, email) VALUES ($1, $2)", user, "fim-"+user.String()[:8]+"@example.com"); err != nil {
		t.Fatal(err)
	}
	mailer := NewTxMailer(inserter)
	key := "run_finished:run_" + user.String()[:8]
	msg := email.Message{To: "fim@example.com", Subject: "Terminou", Text: "corpo", Tag: email.TagRunFinished}
	// Twice in the org transaction a finishing run uses: one row, one job.
	for i := 0; i < 2; i++ {
		err := db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
			to, err := mailer.UserEmail(ctx, tx, user)
			if err != nil || to != "fim-"+user.String()[:8]+"@example.com" {
				t.Fatalf("UserEmail = %q %v", to, err)
			}
			return mailer.EnqueueOnce(ctx, tx, org, key, msg)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows, jobs int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE dedupe_key = $1 AND org_id = $2 AND kind = $3", key, org, email.OutboxKind).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("linhas no outbox: %d %v", rows, err)
	}
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM river_job j JOIN outbox o ON j.args->>'outbox_id' = o.id::text WHERE o.dedupe_key = $1", key).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs de envio: %d %v", jobs, err)
	}
	// The plain Enqueue keeps working beside keyed rows.
	err = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := Enqueue(ctx, tx, nil, nil, msg); err != nil {
			return err
		}
		_, err := Enqueue(ctx, tx, nil, nil, msg)
		return err
	})
	if err != nil {
		t.Fatalf("Enqueue sem chave: %v", err)
	}
	// A user that is gone has no address; bad input is refused before SQL.
	err = db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		to, err := mailer.UserEmail(ctx, tx, uuid.New())
		if err != nil || to != "" {
			t.Fatalf("usuário inexistente: %q %v", to, err)
		}
		if _, _, err := EnqueueOnce(ctx, tx, nil, &org, "", msg); err == nil {
			t.Fatal("chave vazia deveria falhar")
		}
		if _, _, err := EnqueueOnce(ctx, tx, nil, &org, "k", email.Message{To: "x"}); err == nil {
			t.Fatal("mensagem inválida deveria falhar")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
