package platforms

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
)

// Notify queues each confirmed notice once, only after the catalog says available.
// Queued mail shares the confirmation budget, even across API/worker processes.
func (s *Service) Notify(ctx context.Context) error {
	if !s.mailReady || s.base == nil {
		return nil
	}
	for _, item := range s.catalog {
		if item.Status != "available" {
			continue
		}
		if err := s.notifyPlatform(ctx, item); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) notifyPlatform(ctx context.Context, item Platform) error {
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockBudget(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,email,expires_at FROM platform_notices WHERE platform=$1 AND status='confirmed' AND notification_queued_at IS NULL AND expires_at>now() ORDER BY created_at,id LIMIT 10 FOR UPDATE SKIP LOCKED`, item.ID)
		if err != nil {
			return err
		}
		type candidate struct {
			id      uuid.UUID
			address string
			expires time.Time
		}
		list := []candidate{}
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.address, &c.expires); err != nil {
				rows.Close()
				return err
			}
			list = append(list, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range list {
			room, err := mailRoom(ctx, tx)
			if err != nil {
				return err
			}
			if !room {
				return nil
			}
			message := email.Message{To: c.address, Subject: "A docmatch já está disponível em " + item.Name, Tag: email.TagPlatformReady, Text: fmt.Sprintf("Olá,\n\nVocê pediu um aviso: a docmatch já está disponível em %s. Confira o destino oficial:\n\n%s\n\nEste é o único aviso deste pedido. Ele não muda sua conta nem inscreve você em notícias. Para cancelar este pedido:\n\n%s\n\n— docmatch\n", item.Name, item.URL, s.unsubscribeLink(c.id, c.expires))}
			outbox, _, err := emailpg.EnqueueOnce(ctx, tx, s.queue, nil, "platform-ready:"+c.id.String(), message)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE platform_notices SET notification_queued_at=now(),notification_outbox=$2,updated_at=now(),expires_at=LEAST(expires_at,now()+interval '30 days') WHERE id=$1`, c.id, outbox); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO platform_notice_sends(id,email_hash) VALUES($1,$2)`, uuid.New(), crypto.MAC(s.privacyKey, []byte("email"), []byte(c.address))); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) Cleanup(ctx context.Context) error {
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		for _, query := range []string{
			`UPDATE outbox SET sent_at=now() WHERE sent_at IS NULL AND id IN(SELECT notification_outbox FROM platform_notices WHERE expires_at<now() OR status='unsubscribed' LIMIT 1000)`,
			`DELETE FROM platform_notices WHERE id IN(SELECT id FROM platform_notices WHERE expires_at<now() LIMIT 1000)`,
			`DELETE FROM platform_notice_sends WHERE id IN(SELECT id FROM platform_notice_sends WHERE created_at<now()-interval '31 days' LIMIT 1000)`,
			`DELETE FROM outbox WHERE id IN(SELECT id FROM outbox WHERE payload->>'tag'='platform_confirmation' AND created_at<now()-interval '7 days' LIMIT 1000)`,
		} {
			if _, err := tx.Exec(ctx, query); err != nil {
				return err
			}
		}
		return nil
	})
}

const noticeJobKind = "platform_notices"

type noticeArgs struct{}

func (noticeArgs) Kind() string { return noticeJobKind }

type noticeWorker struct {
	river.WorkerDefaults[noticeArgs]
	service *Service
}

func (*noticeWorker) Timeout(*river.Job[noticeArgs]) time.Duration { return time.Minute }
func (w *noticeWorker) Work(ctx context.Context, _ *river.Job[noticeArgs]) error {
	if err := w.service.Cleanup(ctx); err != nil {
		return err
	}
	return w.service.Notify(ctx)
}
func AddWorkers(s *Service) func(*river.Workers) {
	return func(w *river.Workers) { river.AddWorker(w, &noticeWorker{service: s}) }
}
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		return noticeArgs{}, &river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: 15 * time.Minute}}
	}, &river.PeriodicJobOpts{ID: noticeJobKind, RunOnStart: true})}
}
