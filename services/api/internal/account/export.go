package account

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// MaxExportBytes bounds the archive kept in the database.
const MaxExportBytes = 64 << 20

// exportReadme explains the archive in pt-BR.
const exportReadme = `Exportação dos seus dados na docmatch (LGPD, art. 18)

Arquivos:
  perfil.json        seu cadastro (e-mail, nome, preferências, datas)
  organizacao.json   sua organização, papel, assinatura e saldo de tokens
  sessoes.json       sessões ativas (dispositivo e prefixo de rede, sem segredos)
  auditoria.json     trilha de auditoria da organização

Gerado em %s (UTC). Execuções, achados e regras entram neste pacote quando
existirem na sua conta.
`

type exportProfile struct {
	ID              string            `json:"id"`
	Email           string            `json:"email"`
	DisplayName     string            `json:"display_name"`
	EmailVerifiedAt *time.Time        `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time        `json:"last_login_at,omitempty"`
	Notifications   ResolvedPrefs     `json:"notifications"`
	Providers       map[string]bool   `json:"providers"`
	CreatedAt       time.Time         `json:"created_at"`
	Deletion        *time.Time        `json:"deletion_scheduled_for,omitempty"`
	Locale          string            `json:"locale"`
	Extra           map[string]string `json:"-"`
}

type exportOrg struct {
	Org          OrgView       `json:"org"`
	Role         string        `json:"role"`
	MemberSince  time.Time     `json:"member_since"`
	Subscription *Subscription `json:"subscription,omitempty"`
	Credits      CreditSummary `json:"credits"`
}

// BuildArchive renders the bundle as a zip with one JSON file per data set.
func BuildArchive(b Bundle, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, v any) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now.UTC()})
		if err != nil {
			return err
		}
		if s, ok := v.(string); ok {
			_, err = w.Write([]byte(s))
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	profile := exportProfile{
		ID: NewUserView(b.User, b.Membership.Role).ID, Email: b.User.Email, DisplayName: b.User.DisplayName,
		EmailVerifiedAt: b.User.EmailVerifiedAt, LastLoginAt: b.User.LastLoginAt, Notifications: b.User.Notifications.Resolved(),
		Providers: map[string]bool{"google": b.User.GoogleSub != "", "apple": b.User.AppleSub != "", "password": b.User.PasswordHash != ""},
		CreatedAt: b.User.CreatedAt, Deletion: b.User.DeletionScheduledFor, Locale: Locale,
	}
	org := exportOrg{Org: NewOrgView(b.Org, b.Subscription), Role: b.Membership.Role, MemberSince: b.Membership.CreatedAt, Subscription: b.Subscription, Credits: b.Credits}
	sessions := b.Sessions
	if sessions == nil {
		sessions = []SessionInfo{}
	}
	audit := b.Audit
	if audit == nil {
		audit = []AuditEntry{}
	}
	steps := []struct {
		name string
		v    any
	}{
		{"LEIA-ME.txt", fmt.Sprintf(exportReadme, now.UTC().Format(time.RFC3339))},
		{"perfil.json", profile},
		{"organizacao.json", org},
		{"sessoes.json", sessions},
		{"auditoria.json", audit},
	}
	for _, s := range steps {
		if err := add(s.name, s.v); err != nil {
			return nil, fmt.Errorf("account: export %s: %w", s.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > MaxExportBytes {
		return nil, ErrExportTooLarge
	}
	return buf.Bytes(), nil
}
