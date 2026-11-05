package email

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
)

func TestMessageValidate(t *testing.T) {
	t.Parallel()
	ok := Message{To: "a@example.com", Subject: "s", Text: "t"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Message{To: "não é e-mail", Subject: "s", Text: "t"}).Validate(); !errors.Is(err, ErrInvalidTo) {
		t.Fatalf("destinatário: %v", err)
	}
	if err := (Message{To: "a@example.com", Subject: " ", Text: "t"}).Validate(); err == nil {
		t.Fatal("assunto vazio")
	}
	if err := (Message{To: "a@example.com", Subject: "s", Text: ""}).Validate(); err == nil {
		t.Fatal("texto vazio")
	}
}

func TestNewSender(t *testing.T) {
	t.Parallel()
	from, _ := mail.ParseAddress("docmatch <no-reply@example.com>")
	if _, err := NewSender(nil); err == nil {
		t.Fatal("config nula")
	}
	if _, err := NewSender(&config.Config{}); err == nil {
		t.Fatal("sem remetente")
	}
	cfg := &config.Config{Email: config.Email{From: from}}
	if _, err := NewSender(cfg); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nada configurado: %v", err)
	}
	cfg.Email.SMTPAddr = "localhost:1025"
	if s, err := NewSender(cfg); err != nil {
		t.Fatal(err)
	} else if _, ok := s.(*SMTP); !ok {
		t.Fatalf("esperava SMTP, veio %T", s)
	}
	cfg.Email.ResendAPIKey = config.Secret("re_x")
	if s, err := NewSender(cfg); err != nil {
		t.Fatal(err)
	} else if _, ok := s.(*Resend); !ok {
		t.Fatalf("esperava Resend, veio %T", s)
	}
}

func TestResend(t *testing.T) {
	t.Parallel()
	var status atomic.Int32
	status.Store(200)
	var got resendRequest
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if r.URL.Path != "/emails" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()
	s := NewResend("chave", "docmatch <no-reply@example.com>", srv.Client(), srv.URL+"/")
	msg := Message{To: "a@example.com", Subject: "Oi", Text: "corpo", HTML: "<p>corpo</p>", Tag: TagLogin}
	key := uuid.NewString()
	if err := s.Send(context.Background(), key, msg); err != nil {
		t.Fatal(err)
	}
	if headers.Get("Authorization") != "Bearer chave" || headers.Get("Idempotency-Key") != key || headers.Get("Content-Type") != "application/json" {
		t.Fatalf("headers: %v", headers)
	}
	if got.From != "docmatch <no-reply@example.com>" || got.To[0] != "a@example.com" || got.HTML != "<p>corpo</p>" || got.Tags[0].Value != TagLogin {
		t.Fatalf("corpo: %+v", got)
	}
	if err := s.Send(context.Background(), "", Message{To: "x"}); !errors.Is(err, ErrInvalidTo) {
		t.Fatalf("validação: %v", err)
	}
	status.Store(422)
	if err := s.Send(context.Background(), "", msg); err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("status: %v", err)
	}
	down := NewResend("k", "a@example.com", nil, "http://127.0.0.1:1")
	if err := down.Send(context.Background(), "", msg); err == nil {
		t.Fatal("servidor fora")
	}
	if NewResend("k", "a@example.com", nil, "").base != resendDefaultBase {
		t.Fatal("base padrão")
	}
	if err := NewResend("k", "a@example.com", nil, "://x").Send(context.Background(), "", msg); err == nil {
		t.Fatal("URL inválida")
	}
}

// fakeSMTP speaks just enough SMTP for net/smtp and records the DATA.
type fakeSMTP struct {
	ln   net.Listener
	data chan string
	fail string // command that answers 5xx
}

func newFakeSMTP(t *testing.T, fail string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, data: make(chan string, 4), fail: fail}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	say := func(s string) { _, _ = rw.WriteString(s + "\r\n"); _ = rw.Flush() }
	say("220 fake ESMTP")
	inData := false
	var body strings.Builder
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				f.data <- body.String()
				say("250 ok")
				continue
			}
			body.WriteString(line + "\n")
			continue
		}
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		if cmd == f.fail || (f.fail == "EHLO" && cmd == "HELO") {
			say("550 recusado")
			continue
		}
		switch cmd {
		case "EHLO":
			say("250-fake")
			say("250 OK")
		case "HELO", "MAIL", "RCPT":
			say("250 ok")
		case "DATA":
			inData = true
			say("354 go")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 unknown")
		}
	}
}

func TestSMTP(t *testing.T) {
	t.Parallel()
	f := newFakeSMTP(t, "")
	s := NewSMTP(f.ln.Addr().String(), "docmatch <no-reply@example.com>")
	msg := Message{To: "a@example.com", Subject: "Código: ação", Text: "corpo ç", HTML: "<p>corpo</p>"}
	if err := s.Send(context.Background(), "k", msg); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-f.data:
		if !strings.Contains(data, "To: a@example.com") || !strings.Contains(data, "multipart/alternative") || !strings.Contains(data, "=?utf-8?q?") {
			t.Fatalf("mensagem: %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DATA não chegou")
	}
	if err := s.Send(context.Background(), "k", Message{To: "x"}); !errors.Is(err, ErrInvalidTo) {
		t.Fatalf("validação: %v", err)
	}
	bad := NewSMTP(f.ln.Addr().String(), "sem-arroba")
	if err := bad.Send(context.Background(), "k", msg); err == nil {
		t.Fatal("remetente inválido")
	}
	down := NewSMTP("127.0.0.1:1", "a@example.com")
	if err := down.Send(context.Background(), "k", msg); err == nil {
		t.Fatal("servidor fora")
	}
	for _, cmd := range []string{"EHLO", "MAIL", "RCPT", "DATA"} {
		f := newFakeSMTP(t, cmd)
		s := NewSMTP(f.ln.Addr().String(), "a@example.com")
		if err := s.Send(context.Background(), "k", msg); err == nil {
			t.Fatalf("%s recusado deveria falhar", cmd)
		}
	}
	// A greeting that is not SMTP fails at NewClient.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("999 nope\r\n"))
		_ = conn.Close()
	}()
	if err := NewSMTP(ln.Addr().String(), "a@example.com").Send(context.Background(), "k", msg); err == nil {
		t.Fatal("saudação inválida")
	}
}

func TestEncode(t *testing.T) {
	t.Parallel()
	plain := string(Encode("a@example.com", Message{To: "b@example.com", Subject: "s", Text: "t"}))
	if !strings.Contains(plain, "Content-Type: text/plain") || strings.Contains(plain, "multipart") {
		t.Fatalf("texto puro: %s", plain)
	}
	long := string(Encode("a@example.com", Message{To: "b@example.com", Subject: "s", Text: strings.Repeat("x", 500), HTML: "<b>x</b>"}))
	for _, line := range strings.Split(long, "\r\n") {
		if len(line) > 998 {
			t.Fatal("linha longa demais")
		}
	}
	if !strings.Contains(long, "text/html") {
		t.Fatal("parte html")
	}
}

func TestTemplatesAndJobs(t *testing.T) {
	t.Parallel()
	exp := time.Date(2026, 11, 4, 12, 15, 0, 0, time.UTC)
	m := LoginMessage("a@example.com", LoginData{Code: "123456", Link: "https://app.example.com/login/email?c=1&t=<x>", Expires: exp, Minutes: 15})
	if m.To != "a@example.com" || !strings.Contains(m.Subject, "123 456") || !strings.Contains(m.Text, "123 456") || !strings.Contains(m.Text, "15 minutos") {
		t.Fatalf("login: %+v", m)
	}
	if !strings.Contains(m.HTML, "&lt;x&gt;") || strings.Contains(m.HTML, "<x>") || m.Tag != TagLogin {
		t.Fatalf("html precisa escapar: %s", m.HTML)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	short := LoginMessage("a@example.com", LoginData{Code: "12", Link: "l", Minutes: 1})
	if !strings.Contains(short.Subject, "12") {
		t.Fatal("código curto fica como está")
	}
	e := ExportReadyMessage("a@example.com", "https://api.example.com/d?token=1", exp)
	if !strings.Contains(e.Text, "04/11/2026 12:15 UTC") || e.Tag != TagExportReady || e.Validate() != nil {
		t.Fatalf("export: %+v", e)
	}
	d := DeletionScheduledMessage("a@example.com", exp, " suporte@example.com ")
	if !strings.Contains(d.Text, "04/11/2026") || !strings.Contains(d.Text, "suporte@example.com") || d.Tag != TagDeletion {
		t.Fatalf("exclusão: %+v", d)
	}
	if strings.Contains(DeletionScheduledMessage("a@example.com", exp, "").Text, "cancelar") {
		t.Fatal("sem contato não há instrução de cancelamento")
	}
	if (SendArgs{}).Kind() != KindSend || (SweepArgs{}).Kind() != KindSweep {
		t.Fatal("kinds")
	}
	if o := (SendArgs{}).InsertOpts(); o.MaxAttempts != MaxDeliveryTries || !o.UniqueOpts.ByArgs {
		t.Fatalf("send opts: %+v", o)
	}
	if o := (SweepArgs{}).InsertOpts(); o.UniqueOpts.ByPeriod != SweepInterval {
		t.Fatalf("sweep opts: %+v", o)
	}
	if Backoff(0) != 30*time.Second || Backoff(1) != 30*time.Second || Backoff(2) != time.Minute || Backoff(3) != 2*time.Minute || Backoff(20) != time.Hour {
		t.Fatalf("backoff: %v %v %v", Backoff(1), Backoff(3), Backoff(20))
	}
}
