package email

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Template tags.
const (
	TagLogin       = "login"
	TagExportReady = "export_ready"
	TagDeletion    = "deletion_scheduled"
)

// LoginData fills the login e-mail.
type LoginData struct {
	// Code is the six-digit code the person can type.
	Code string
	// Link opens the app with the challenge and the magic-link token.
	Link string
	// Expires is when both stop working.
	Expires time.Time
	// Minutes is the validity shown to the person.
	Minutes int
}

// LoginMessage renders the pt-BR login e-mail with the code and the link.
func LoginMessage(to string, d LoginData) Message {
	code := d.Code
	if len(code) == 6 {
		code = code[:3] + " " + code[3:]
	}
	text := fmt.Sprintf(`Olá,

Seu código de acesso à docmatch é:

    %s

Ou entre direto por este link:

%s

O código e o link valem por %d minutos e só funcionam uma vez. Se você não pediu este acesso, ignore este e-mail: ninguém entra na sua conta sem ele.

— docmatch
`, code, d.Link, d.Minutes)
	htmlBody := fmt.Sprintf(`<!doctype html><html lang="pt-BR"><body style="font-family:system-ui,sans-serif;line-height:1.5;color:#111">
<p>Olá,</p>
<p>Seu código de acesso à docmatch é:</p>
<p style="font-size:28px;letter-spacing:4px;font-weight:600">%s</p>
<p>Ou entre direto por este link:</p>
<p><a href="%s">Entrar na docmatch</a></p>
<p style="color:#555">O código e o link valem por %d minutos e só funcionam uma vez. Se você não pediu este acesso, ignore este e-mail: ninguém entra na sua conta sem ele.</p>
<p>— docmatch</p>
</body></html>`, html.EscapeString(code), html.EscapeString(d.Link), d.Minutes)
	return Message{To: to, Subject: "Seu código de acesso à docmatch: " + code, Text: text, HTML: htmlBody, Tag: TagLogin}
}

// ExportReadyMessage tells the person their data export can be downloaded.
func ExportReadyMessage(to, link string, expires time.Time) Message {
	when := expires.UTC().Format("02/01/2006 15:04") + " UTC"
	text := fmt.Sprintf(`Olá,

A exportação dos seus dados na docmatch está pronta. Baixe o arquivo por este link até %s:

%s

Depois disso o arquivo é apagado e você pode pedir uma nova exportação.

— docmatch
`, when, link)
	htmlBody := fmt.Sprintf(`<!doctype html><html lang="pt-BR"><body style="font-family:system-ui,sans-serif;line-height:1.5;color:#111">
<p>Olá,</p>
<p>A exportação dos seus dados na docmatch está pronta. Baixe o arquivo por este link até %s:</p>
<p><a href="%s">Baixar meus dados</a></p>
<p style="color:#555">Depois disso o arquivo é apagado e você pode pedir uma nova exportação.</p>
<p>— docmatch</p>
</body></html>`, html.EscapeString(when), html.EscapeString(link))
	return Message{To: to, Subject: "Sua exportação de dados está pronta", Text: text, HTML: htmlBody, Tag: TagExportReady}
}

// DeletionScheduledMessage confirms an account deletion request.
func DeletionScheduledMessage(to string, scheduledFor time.Time, supportContact string) Message {
	when := scheduledFor.UTC().Format("02/01/2006")
	contact := strings.TrimSpace(supportContact)
	extra := ""
	if contact != "" {
		extra = " Para cancelar o pedido, escreva para " + contact + " antes dessa data."
	}
	text := fmt.Sprintf(`Olá,

Recebemos o pedido de exclusão da sua conta na docmatch. A conta e todos os dados serão apagados em %s; até lá o login fica bloqueado.%s

— docmatch
`, when, extra)
	htmlBody := fmt.Sprintf(`<!doctype html><html lang="pt-BR"><body style="font-family:system-ui,sans-serif;line-height:1.5;color:#111">
<p>Olá,</p>
<p>Recebemos o pedido de exclusão da sua conta na docmatch. A conta e todos os dados serão apagados em <strong>%s</strong>; até lá o login fica bloqueado.%s</p>
<p>— docmatch</p>
</body></html>`, html.EscapeString(when), html.EscapeString(extra))
	return Message{To: to, Subject: "Pedido de exclusão da conta recebido", Text: text, HTML: htmlBody, Tag: TagDeletion}
}
