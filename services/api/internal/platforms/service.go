package platforms

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/geo"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Small shared budgets cover confirmations AND release notices. No new sender,
// service or capacity is provisioned; launch mail waits behind the same budget.
const DailyMailLimit = 20
const MonthlyMailLimit = 200
const pendingRetention = 7 * 24 * time.Hour
const confirmedRetention = 365 * 24 * time.Hour
const resendCooldown = 15 * time.Minute
const consentVersion = "platform-ready-v1"

type Service struct {
	edge       *geo.Edge
	pool       *pgxpool.Pool
	queue      *emailpg.Client
	catalog    []Platform
	base       *url.URL
	tokenKey   []byte
	privacyKey []byte
	mailReady  bool
}

func New(cfg *config.Config, pool *pgxpool.Pool) (*Service, error) {
	catalog, err := ParseCatalog(CatalogJSON)
	if err != nil {
		return nil, err
	}
	keys, err := crypto.NewKeyring(cfg.Auth.SessionSecret.Reveal())
	if err != nil {
		return nil, err
	}
	queue, err := emailpg.NewInserter(pool)
	if err != nil {
		return nil, err
	}
	_, mailErr := email.NewSender(cfg)
	return &Service{edge: geo.New(cfg.OriginSecret.Reveal(), geo.WithHeaderName(cfg.OriginSecretHeader)), pool: pool, queue: queue, catalog: catalog, base: cfg.PublicBaseURL,
		tokenKey: keys.Key("docmatch/platform-unsubscribe/v1"), privacyKey: keys.Key("docmatch/platform-rate-limit/v1"), mailReady: mailErr == nil}, nil
}

func (s *Service) platform(id string) (Platform, bool) {
	for _, item := range s.catalog {
		if item.ID == id {
			return item, true
		}
	}
	return Platform{}, false
}
func validation(field string) error {
	return apix.Validation(apix.FieldError{Path: "/" + field, Code: "invalid"})
}
func normalizeAddress(raw string) (string, error) {
	value, err := httpx.ValidateText(raw, contracts.TextEmail, true)
	if err != nil {
		return "", validation("email")
	}
	value = strings.ToLower(value)
	address, err := mail.ParseAddress(value)
	at := strings.LastIndex(value, "@")
	if err != nil || address.Address != value || address.Name != "" || at < 1 || !strings.Contains(value[at+1:], ".") || strings.ContainsAny(value, " <>[](),;:\"\\") {
		return "", validation("email")
	}
	return value, nil
}
func lockBudget(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('docmatch-platform-mail-budget',0))`)
	return err
}
func mailRoom(ctx context.Context, tx pgx.Tx) (bool, error) {
	var day, month int
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE created_at>now()-interval '1 day'),count(*) FROM platform_notice_sends WHERE created_at>now()-interval '30 days'`).Scan(&day, &month)
	return day < DailyMailLimit && month < MonthlyMailLimit, err
}
func (s *Service) link(action, token string) string {
	u := *s.base
	u.Path = "/avisos"
	u.RawQuery = ""
	u.Fragment = action + "=" + token
	return u.String()
}
func (s *Service) unsubscribeLink(id uuid.UUID, expires time.Time) string {
	return s.link("cancelar", crypto.SignToken(s.tokenKey, id[:], expires.Add(30*24*time.Hour)))
}

// Subscribe always gives the same accepted response for existing and new addresses.
// A per-address/IP cooldown and a durable shared budget run under one database lock.
func (s *Service) Subscribe(ctx context.Context, platform, address, ip string, consent bool) error {
	item, ok := s.platform(platform)
	if !ok || item.Status == "available" {
		return validation("platform")
	}
	if !consent {
		return validation("consent")
	}
	address, err := normalizeAddress(address)
	if err != nil {
		return err
	}
	if !s.mailReady || s.base == nil {
		return apix.E(contracts.ErrServiceUnavailable, nil)
	}
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockBudget(ctx, tx); err != nil {
			return err
		}
		emailHash := crypto.MAC(s.privacyKey, []byte("email"), []byte(address))
		ipHash := crypto.MAC(s.privacyKey, []byte("ip"), []byte(ip))
		var emailCount, ipCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE email_hash=$1 AND created_at>now()-interval '1 day'),count(*) FILTER(WHERE ip_hash=$2 AND created_at>now()-interval '1 hour') FROM platform_notice_sends WHERE created_at>now()-interval '1 day'`, emailHash, ipHash).Scan(&emailCount, &ipCount); err != nil {
			return err
		}
		if emailCount >= 2 || ipCount >= 5 {
			return nil
		}
		var id uuid.UUID
		var status string
		var updated time.Time
		err := tx.QueryRow(ctx, `SELECT id,status,updated_at FROM platform_notices WHERE platform=$1 AND email=$2 FOR UPDATE`, platform, address).Scan(&id, &status, &updated)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && (status == "confirmed" || time.Since(updated) < resendCooldown) {
			return nil
		}
		room, err := mailRoom(ctx, tx)
		if err != nil {
			return err
		}
		if !room {
			return apix.E(contracts.ErrRateLimited, nil)
		}
		id = uuid.New()
		token := crypto.RandomToken(32)
		expires := time.Now().Add(pendingRetention)
		// A new ID also invalidates an older unsubscribe link after an explicit new signup.
		_, err = tx.Exec(ctx, `INSERT INTO platform_notices(id,platform,email,confirmation_hash,confirmation_expires_at,consent_version,expires_at) VALUES($1,$2,$3,$4,now()+interval '24 hours',$5,$6)
   ON CONFLICT(platform,email) DO UPDATE SET id=EXCLUDED.id,status='pending',confirmation_hash=EXCLUDED.confirmation_hash,confirmation_expires_at=EXCLUDED.confirmation_expires_at,consent_version=EXCLUDED.consent_version,confirmed_at=NULL,unsubscribed_at=NULL,notification_queued_at=NULL,notification_outbox=NULL,created_at=now(),updated_at=now(),expires_at=EXCLUDED.expires_at`, id, platform, address, crypto.Digest(token), consentVersion, expires)
		if err != nil {
			return err
		}
		message := email.Message{To: address, Subject: "Confirme seu aviso da docmatch para " + item.Name, Tag: email.TagPlatformConfirmation, Text: fmt.Sprintf("Olá,\n\nVocê pediu um aviso quando a docmatch estiver disponível em %s. Confirme seu endereço pelo link abaixo e pelo botão da página:\n\n%s\n\nO link vale por 24 horas. Apenas abrir a página não confirma o pedido. Você receberá somente o aviso desta plataforma, sem inscrição em notícias ou mudanças na sua conta. Guardamos pedidos não confirmados por 7 dias e confirmados por até 1 ano.\n\nPara cancelar o pedido:\n%s\n\nSe não pediu este aviso, ignore esta mensagem.\n\n— docmatch\n", item.Name, s.link("confirmar", token), s.unsubscribeLink(id, time.Now().Add(confirmedRetention)))}
		confirmationOutbox, err := emailpg.Enqueue(ctx, tx, s.queue, nil, message)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_notices SET confirmation_outbox=$2 WHERE id=$1`, id, confirmationOutbox); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform_notice_sends(id,email_hash,ip_hash) VALUES($1,$2,$3)`, uuid.New(), emailHash, ipHash)
		return err
	})
}

func (s *Service) Confirm(ctx context.Context, token string) (string, error) {
	if !crypto.ValidToken(token, 32) {
		return "", validation("token")
	}
	var platform string
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE platform_notices SET status='confirmed',confirmed_at=COALESCE(confirmed_at,now()),updated_at=CASE WHEN status='pending' THEN now() ELSE updated_at END,expires_at=CASE WHEN status='pending' THEN now()+interval '365 days' ELSE expires_at END
   WHERE confirmation_hash=$1 AND confirmation_expires_at>now() AND status IN ('pending','confirmed') RETURNING platform`, crypto.Digest(token)).Scan(&platform)
		if errors.Is(err, pgx.ErrNoRows) {
			return validation("token")
		}
		return err
	})
	return platform, err
}
func (s *Service) Unsubscribe(ctx context.Context, token string) error {
	if len(token) > 256 {
		return validation("token")
	}
	payload, err := crypto.VerifyToken(s.tokenKey, token, time.Now())
	if err != nil {
		return validation("token")
	}
	id, err := uuid.FromBytes(payload)
	if err != nil {
		return validation("token")
	}
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var queued, confirmation *uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE platform_notices SET status='unsubscribed',confirmation_hash=NULL,confirmation_expires_at=NULL,unsubscribed_at=now(),updated_at=now(),expires_at=now()+interval '7 days' WHERE id=$1 RETURNING notification_outbox,confirmation_outbox`, id).Scan(&queued, &confirmation)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if queued != nil || confirmation != nil {
			// Cancel anything still waiting in the outbox. Already delivered mail cannot be recalled.
			_, err = tx.Exec(ctx, `UPDATE outbox SET sent_at=now() WHERE (id=$1 OR id=$2) AND sent_at IS NULL AND payload->>'tag' IN ('platform_ready','platform_confirmation')`, queued, confirmation)
		}
		return err
	})
}
