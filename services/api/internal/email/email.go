// Package email sends transactional e-mail. Nothing is sent inline: the
// application writes a Message to the outbox table in the same transaction
// as the data that justifies it and the worker delivers it through a Sender
// (Resend in production, SMTP to Mailpit in dev), retrying with backoff and
// using the outbox id as the idempotency key so a retry never sends twice.
// Templates are pt-BR.
package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/config"
)

// OutboxKind is the outbox.kind of e-mail rows.
const OutboxKind = "email"

// Job kinds and schedule (the worker lives in emailpg).
const (
	KindSend          = "email_send"
	KindSweep         = "email_outbox_sweep"
	SweepInterval     = time.Minute
	SweepMinAge       = 2 * time.Minute
	SweepBatch        = 50
	MaxDeliveryTries  = 10
	DeliveryTimeout   = 20 * time.Second
	resendDefaultBase = "https://api.resend.com"
)

// Errors.
var (
	ErrNotConfigured = errors.New("email: no sender configured (RESEND_API_KEY or DM_SMTP_ADDR)")
	ErrInvalidTo     = errors.New("email: invalid recipient")
)

// Message is one e-mail; it is stored as JSON in outbox.payload.
type Message struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html,omitempty"`
	// Tag labels the template (login, export) for logs and provider analytics.
	Tag string `json:"tag,omitempty"`
}

// Validate checks the message is deliverable.
func (m Message) Validate() error {
	if _, err := mail.ParseAddress(m.To); err != nil {
		return ErrInvalidTo
	}
	if strings.TrimSpace(m.Subject) == "" || strings.TrimSpace(m.Text) == "" {
		return errors.New("email: subject and text are required")
	}
	return nil
}

// Sender delivers messages. idempotencyKey (the outbox id) must make a
// repeated call for the same message a no-op where the provider supports it.
type Sender interface {
	Send(ctx context.Context, idempotencyKey string, m Message) error
}

// NewSender picks the implementation from the configuration: Resend when
// RESEND_API_KEY is set, else SMTP when DM_SMTP_ADDR is set (Mailpit from
// tools/dev), else ErrNotConfigured. DM_EMAIL_FROM is required either way.
func NewSender(cfg *config.Config) (Sender, error) {
	if cfg == nil || cfg.Email.From == nil {
		return nil, errors.New("email: DM_EMAIL_FROM is required")
	}
	from := cfg.Email.From.String()
	if cfg.Email.ResendAPIKey.IsSet() {
		return NewResend(cfg.Email.ResendAPIKey.Reveal(), from, nil, ""), nil
	}
	if cfg.Email.SMTPAddr != "" {
		return NewSMTP(cfg.Email.SMTPAddr, from), nil
	}
	return nil, ErrNotConfigured
}

// ---------------------------------------------------------------- Resend

// Resend sends through the Resend HTTP API.
type Resend struct {
	apiKey string
	from   string
	base   string
	client *http.Client
}

// NewResend returns a Resend sender; client and base may be empty.
func NewResend(apiKey, from string, client *http.Client, base string) *Resend {
	if client == nil {
		client = &http.Client{Timeout: DeliveryTimeout}
	}
	if base == "" {
		base = resendDefaultBase
	}
	return &Resend{apiKey: apiKey, from: from, base: strings.TrimRight(base, "/"), client: client}
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html,omitempty"`
	Tags    []tag    `json:"tags,omitempty"`
}

type tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Send implements Sender. The Idempotency-Key header lets Resend drop
// duplicates of a retried delivery.
func (r *Resend) Send(ctx context.Context, idempotencyKey string, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	body := resendRequest{From: r.from, To: []string{m.To}, Subject: m.Subject, Text: m.Text, HTML: m.HTML}
	if m.Tag != "" {
		body.Tags = []tag{{Name: "template", Value: m.Tag}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, DeliveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/emails", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("email: resend: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("email: resend: status %d", resp.StatusCode)
	}
	return nil
}

// ------------------------------------------------------------------ SMTP

// SMTP sends through a plain SMTP relay without authentication: the Mailpit
// container of the dev environment. It is never used in production.
type SMTP struct {
	addr string
	from string
	// dial is replaced in tests.
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

// NewSMTP returns a sender for host:port.
func NewSMTP(addr, from string) *SMTP {
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &SMTP{addr: addr, from: from, dial: func(ctx context.Context, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", addr)
	}}
}

// Send implements Sender.
func (s *SMTP) Send(ctx context.Context, _ string, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	fromAddr, err := mail.ParseAddress(s.from)
	if err != nil {
		return fmt.Errorf("email: remetente inválido: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, DeliveryTimeout)
	defer cancel()
	conn, err := s.dial(ctx, s.addr)
	if err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(DeliveryTimeout))
	host, _, _ := net.SplitHostPort(s.addr)
	if host == "" {
		host = s.addr
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("docmatch.local"); err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	if err := c.Mail(fromAddr.Address); err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	if _, err := w.Write(Encode(s.from, m)); err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: smtp: %w", err)
	}
	return c.Quit()
}

// Encode renders the RFC 5322 message (multipart/alternative when there is
// HTML) with base64 bodies, so any UTF-8 text survives every relay.
func Encode(from string, m Message) []byte {
	var b bytes.Buffer
	boundary := "dm-" + uuid.NewString()
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	if m.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		writeBase64(&b, m.Text)
		return b.Bytes()
	}
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary)
	writeBase64(&b, m.Text)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary)
	writeBase64(&b, m.HTML)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}

func writeBase64(b *bytes.Buffer, s string) {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
}

// ------------------------------------------------------------ River jobs

// SendArgs delivers one outbox row right away.
type SendArgs struct {
	OutboxID uuid.UUID `json:"outbox_id"`
}

// Kind implements river.JobArgs.
func (SendArgs) Kind() string { return KindSend }

// InsertOpts dedupes jobs per outbox row and bounds retries.
func (SendArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: MaxDeliveryTries, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// SweepArgs relays outbox rows whose immediate job was lost.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return KindSweep }

// InsertOpts dedupes sweeps inside one interval.
func (SweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: SweepInterval}}
}

// Backoff returns how long to wait before delivery attempt n (1-based):
// 30 s, 1 min, 2 min ... capped at one hour.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := 30 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}
