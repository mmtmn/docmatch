package config

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	sampleDBPassword    = "senha-super-secreta"
	sampleOpenRouterKey = "chave-openrouter-secreta"
)

// sampleSessionSecret is built at runtime (not a literal) so secret scanners
// do not mistake the test fixture for a real credential.
var sampleSessionSecret = base64.StdEncoding.EncodeToString([]byte("sessao-de-teste-com-mais-de-32-bytes!!"))

// validEnv is a complete dev environment, mirroring .env.example.
func validEnv() map[string]string {
	return map[string]string{
		VarEnv:               "dev",
		VarPublicBaseURL:     "http://localhost:4321",
		VarAPIBaseURL:        "http://localhost:8080",
		VarAPIListen:         "127.0.0.1:8080",
		VarAdminListen:       "127.0.0.1:8443",
		VarOriginSecret:      "change-me",
		VarDatabaseURL:       "postgres://docmatch:" + sampleDBPassword + "@localhost:5432/docmatch?sslmode=disable",
		VarAWSRegion:         "us-east-1",
		VarS3UploadsBucket:   "docmatch-uploads-dev",
		VarS3BackupsBucket:   "docmatch-backups-dev",
		VarS3Endpoint:        "http://localhost:9000",
		VarR2MediaBucket:     "docmatch-media-dev",
		VarLLMMode:           "local",
		VarLocalLLMBaseURL:   "http://localhost:11434/v1",
		VarModelsFile:        "config/models.yaml",
		VarLLMCacheDir:       ".cache/llm",
		VarSessionSecret:     sampleSessionSecret,
		VarTargetMargin:      "0.30",
		VarIncomeTaxRate:     "0.21",
		VarPaymentRate:       "0.076",
		VarInfraRate:         "0.10",
		VarOpenRouterFee:     "0.055",
		VarPricingFile:       "config/pricing.yaml",
		VarEmailFrom:         "docmatch <no-reply@example.com>",
		VarSecurityContact:   "security@example.com",
		VarQuoteHMACKey:      "quote-hmac-key-with-length",
		VarAPIKeyPepper:      "apikey-pepper-with-length",
		VarWebhookSigningKey: "webhook-signing-key-with-length",
	}
}

func lookupFrom(m map[string]string) Lookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func mustLoad(t *testing.T, env map[string]string) *Config {
	t.Helper()
	cfg, err := LoadFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	return cfg
}

func validationVars(t *testing.T, err error) []string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("esperava *ValidationError, veio %T: %v", err, err)
	}
	return ve.Vars()
}

func TestLoadValidDev(t *testing.T) {
	t.Parallel()
	cfg := mustLoad(t, validEnv())
	if cfg.Env != EnvDev || !cfg.IsDev() || cfg.IsProd() {
		t.Fatalf("Env = %q", cfg.Env)
	}
	if cfg.PublicBaseURL.String() != "http://localhost:4321" || cfg.APIBaseURL.Host != "localhost:8080" {
		t.Fatalf("URLs = %v %v", cfg.PublicBaseURL, cfg.APIBaseURL)
	}
	if cfg.Database.URL.Reveal() == "" || cfg.Database.AppRole != DefaultDBAppRole {
		t.Fatalf("Database = %+v", cfg.Database)
	}
	if cfg.Storage.S3Endpoint == nil || cfg.Storage.S3Endpoint.Port() != "9000" {
		t.Fatalf("S3Endpoint = %v", cfg.Storage.S3Endpoint)
	}
	if cfg.LLM.Mode != LLMModeLocal || cfg.LLM.LocalBaseURL.Path != "/v1" {
		t.Fatalf("LLM = %+v", cfg.LLM)
	}
	wantSecret, _ := base64.StdEncoding.DecodeString(sampleSessionSecret)
	if !bytes.Equal(cfg.Auth.SessionSecret.Reveal(), wantSecret) || len(wantSecret) < 32 {
		t.Fatalf("SessionSecret = %d bytes, esperado %d", len(cfg.Auth.SessionSecret), len(wantSecret))
	}
	if cfg.Billing.TargetMargin == nil || *cfg.Billing.TargetMargin != 0.30 || cfg.Billing.PaymentRate == nil || *cfg.Billing.PaymentRate != 0.076 {
		t.Fatalf("Billing = %+v", cfg.Billing)
	}
	if cfg.Email.From == nil || cfg.Email.From.Name != "docmatch" || cfg.Email.SecurityContact == nil {
		t.Fatalf("Email = %+v", cfg.Email)
	}
	if !cfg.Signing.QuoteHMACKey.IsSet() || !cfg.Signing.APIKeyPepper.IsSet() || !cfg.Signing.WebhookSigningKey.IsSet() {
		t.Fatalf("Signing = %+v", cfg.Signing)
	}
	if got := cfg.AllowedOrigins(); !slices.Equal(got, []string{"http://localhost:4321", "http://localhost:8080"}) {
		t.Fatalf("AllowedOrigins = %v", got)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()
	env := validEnv()
	for _, k := range []string{VarEnv, VarLLMMode, VarModelsFile, VarPricingFile, VarLLMCacheDir, VarTargetMargin, VarAWSRegion, VarEmailFrom, VarSecurityContact} {
		delete(env, k)
	}
	cfg := mustLoad(t, env)
	if cfg.Env != DefaultEnv || cfg.LLM.Mode != DefaultLLMMode || cfg.LLM.ModelsFile != DefaultModelsFile ||
		cfg.Billing.PricingFile != DefaultPricingFile || cfg.LLM.CacheDir != DefaultLLMCacheDir {
		t.Fatalf("defaults não aplicados: %+v", cfg)
	}
	if cfg.Billing.TargetMargin != nil {
		t.Fatal("DM_TARGET_MARGIN ausente deveria ficar nil (usa o arquivo)")
	}
	if cfg.Email.From != nil || cfg.Email.SecurityContact != nil || cfg.Storage.AWSRegion != "" {
		t.Fatalf("opcionais ausentes deveriam ficar vazios: %+v", cfg)
	}
}

func TestLoadReportsEveryMissingRequiredVar(t *testing.T) {
	t.Parallel()
	_, err := LoadFrom(lookupFrom(map[string]string{}))
	if err == nil {
		t.Fatal("esperava erro")
	}
	got := validationVars(t, err)
	want := []string{
		VarAdminListen, VarAPIKeyPepper, VarAPIListen, VarAPIBaseURL, VarLocalLLMBaseURL, VarPublicBaseURL,
		VarQuoteHMACKey, VarSessionSecret, VarWebhookSigningKey, VarDatabaseURL,
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("variáveis reportadas = %v, esperado %v", got, want)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "configuração inválida (10 problemas):") {
		t.Fatalf("mensagem inesperada: %q", msg)
	}
	for _, v := range want {
		if !strings.Contains(msg, "  - "+v+": obrigatória") {
			t.Fatalf("mensagem não lista %s: %q", v, msg)
		}
	}
}

func TestLoadSingleProblemMessage(t *testing.T) {
	t.Parallel()
	env := validEnv()
	delete(env, VarDatabaseURL)
	_, err := LoadFrom(lookupFrom(env))
	if err == nil || !strings.HasPrefix(err.Error(), "configuração inválida (1 problema):") {
		t.Fatalf("mensagem inesperada: %v", err)
	}
}

func TestLoadBadValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(env map[string]string)
		wantVar string
		wantMsg string
	}{
		{"env desconhecido", func(e map[string]string) { e[VarEnv] = "qa" }, VarEnv, "inválido"},
		{"url sem esquema", func(e map[string]string) { e[VarPublicBaseURL] = "localhost:4321" }, VarPublicBaseURL, "http"},
		{"url com credenciais", func(e map[string]string) { e[VarAPIBaseURL] = "http://user:pw@localhost" }, VarAPIBaseURL, "credenciais"},
		{"url ftp", func(e map[string]string) { e[VarS3Endpoint] = "ftp://localhost" }, VarS3Endpoint, "http"},
		{"url sem host", func(e map[string]string) { e[VarLocalLLMBaseURL] = "http://" }, VarLocalLLMBaseURL, "host"},
		{"url inválida", func(e map[string]string) { e[VarLocalLLMBaseURL] = "http://[::1" }, VarLocalLLMBaseURL, "URL"},
		{"listen sem porta", func(e map[string]string) { e[VarAPIListen] = "127.0.0.1" }, VarAPIListen, "host:porta"},
		{"listen porta inválida", func(e map[string]string) { e[VarAPIListen] = "127.0.0.1:99999" }, VarAPIListen, "porta"},
		{"listen host inválido", func(e map[string]string) { e[VarAdminListen] = "exemplo:8443" }, VarAdminListen, "IP"},
		{"database url esquema", func(e map[string]string) { e[VarDatabaseURL] = "mysql://x" }, VarDatabaseURL, "postgres://"},
		{"papel inválido", func(e map[string]string) { e[VarDBAppRole] = "app;drop" }, VarDBAppRole, "identificador"},
		{"papel começa com dígito", func(e map[string]string) { e[VarDBAppRole] = "1app" }, VarDBAppRole, "identificador"},
		{"papel longo", func(e map[string]string) { e[VarDBAppRole] = strings.Repeat("a", 64) }, VarDBAppRole, "63"},
		{"llm mode", func(e map[string]string) { e[VarLLMMode] = "cloud" }, VarLLMMode, "inválido"},
		{"openrouter sem chave", func(e map[string]string) { e[VarLLMMode] = "openrouter" }, VarOpenRouterKey, "obrigatória"},
		{"local sem url", func(e map[string]string) { delete(e, VarLocalLLMBaseURL) }, VarLocalLLMBaseURL, "local"},
		{"sessão base64 inválido", func(e map[string]string) { e[VarSessionSecret] = "not*base64" }, VarSessionSecret, "base64"},
		{"sessão curta", func(e map[string]string) { e[VarSessionSecret] = base64.StdEncoding.EncodeToString([]byte("curta")) }, VarSessionSecret, "32 bytes"},
		{"taxa fora do intervalo", func(e map[string]string) { e[VarTargetMargin] = "1.5" }, VarTargetMargin, "entre 0 e 1"},
		{"taxa negativa", func(e map[string]string) { e[VarInfraRate] = "-0.1" }, VarInfraRate, "entre 0 e 1"},
		{"taxa não numérica", func(e map[string]string) { e[VarPaymentRate] = "sete" }, VarPaymentRate, "número"},
		{"email inválido", func(e map[string]string) { e[VarEmailFrom] = "docmatch sem email" }, VarEmailFrom, "e-mail"},
		{"contato inválido", func(e map[string]string) { e[VarSecurityContact] = "@" }, VarSecurityContact, "e-mail"},
		{"chave curta", func(e map[string]string) { e[VarQuoteHMACKey] = "curta" }, VarQuoteHMACKey, "16 caracteres"},
		{"pepper curto", func(e map[string]string) { e[VarAPIKeyPepper] = "curto" }, VarAPIKeyPepper, "16 caracteres"},
		{"só espaços conta como ausente", func(e map[string]string) { e[VarWebhookSigningKey] = "   " }, VarWebhookSigningKey, "ausente"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := validEnv()
			tc.mutate(env)
			_, err := LoadFrom(lookupFrom(env))
			if err == nil {
				t.Fatal("esperava erro")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("esperava ValidationError, veio %v", err)
			}
			idx := slices.IndexFunc(ve.Problems, func(p Problem) bool { return p.Var == tc.wantVar })
			if idx < 0 {
				t.Fatalf("problema em %s não reportado: %v", tc.wantVar, err)
			}
			if !strings.Contains(ve.Problems[idx].Message, tc.wantMsg) {
				t.Fatalf("mensagem %q não contém %q", ve.Problems[idx].Message, tc.wantMsg)
			}
		})
	}
}

func TestLoadProdRules(t *testing.T) {
	t.Parallel()
	prodEnv := func() map[string]string {
		env := validEnv()
		env[VarEnv] = "prod"
		env[VarPublicBaseURL] = "https://app.example.com"
		env[VarAPIBaseURL] = "https://api.example.com"
		env[VarOriginSecret] = "segredo-real-da-origem"
		delete(env, VarS3Endpoint)
		return env
	}
	cfg := mustLoad(t, prodEnv())
	if !cfg.IsProd() || cfg.Storage.S3Endpoint != nil {
		t.Fatalf("prod válido não carregou: %+v", cfg)
	}

	cases := []struct {
		name    string
		mutate  func(env map[string]string)
		wantVar string
		wantMsg string
	}{
		{"origem ausente", func(e map[string]string) { delete(e, VarOriginSecret) }, VarOriginSecret, "ausente"},
		{"origem placeholder", func(e map[string]string) { e[VarOriginSecret] = "change-me" }, VarOriginSecret, "exemplo"},
		{"http em prod", func(e map[string]string) { e[VarPublicBaseURL] = "http://app.example.com" }, VarPublicBaseURL, "https"},
		{"endpoint s3 em prod", func(e map[string]string) { e[VarS3Endpoint] = "http://localhost:9000" }, VarS3Endpoint, "vazia"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := prodEnv()
			tc.mutate(env)
			_, err := LoadFrom(lookupFrom(env))
			if err == nil {
				t.Fatal("esperava erro")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("esperava ValidationError, veio %v", err)
			}
			idx := slices.IndexFunc(ve.Problems, func(p Problem) bool { return p.Var == tc.wantVar })
			if idx < 0 || !strings.Contains(ve.Problems[idx].Message, tc.wantMsg) {
				t.Fatalf("problema esperado em %s (%q): %v", tc.wantVar, tc.wantMsg, err)
			}
		})
	}
}

func TestSessionSecretAcceptsRawAndURLBase64(t *testing.T) {
	t.Parallel()
	raw := bytes.Repeat([]byte{0xfb, 0xff}, 20)
	for name, enc := range map[string]string{
		"std":    base64.StdEncoding.EncodeToString(raw),
		"rawstd": base64.RawStdEncoding.EncodeToString(raw),
		"url":    base64.URLEncoding.EncodeToString(raw),
	} {
		env := validEnv()
		env[VarSessionSecret] = enc
		cfg := mustLoad(t, env)
		if !bytes.Equal(cfg.Auth.SessionSecret.Reveal(), raw) {
			t.Fatalf("%s: segredo decodificado difere", name)
		}
	}
}

func TestAllowedOriginsDeduplicates(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarAPIBaseURL] = "HTTP://LOCALHOST:4321"
	cfg := mustLoad(t, env)
	if got := cfg.AllowedOrigins(); !slices.Equal(got, []string{"http://localhost:4321"}) {
		t.Fatalf("AllowedOrigins = %v", got)
	}
	empty := &Config{}
	if got := empty.AllowedOrigins(); len(got) != 0 {
		t.Fatalf("AllowedOrigins vazio = %v", got)
	}
}

func TestSecretNeverLeaks(t *testing.T) {
	t.Parallel()
	s := Secret("hunter2-valor-secreto")
	sb := SecretBytes("hunter2-bytes-secretos")
	formats := []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x", "%X", "%10s", "%-5v", "%T"}
	for _, f := range formats {
		out := fmt.Sprintf(f, s)
		if strings.Contains(out, "hunter2") {
			t.Fatalf("formato %s vazou o segredo: %q", f, out)
		}
		out = fmt.Sprintf(f, sb)
		if strings.Contains(out, "hunter2") || strings.Contains(out, "104 117") || strings.Contains(out, "68756e") {
			t.Fatalf("formato %s vazou os bytes: %q", f, out)
		}
	}
	if fmt.Sprintf("%T", sb) != "config.SecretBytes" || sb.String() != Redacted || sb.GoString() != "config.SecretBytes("+Redacted+")" {
		t.Fatalf("SecretBytes formatação inesperada: %T %s %s", sb, sb.String(), sb.GoString())
	}
	if string(sb.Reveal()) != "hunter2-bytes-secretos" || !sb.IsSet() || SecretBytes(nil).IsSet() {
		t.Fatal("SecretBytes Reveal/IsSet incorretos")
	}
	if jsb, err := json.Marshal(sb); err != nil || string(jsb) != `"`+Redacted+`"` {
		t.Fatalf("SecretBytes json = %s (%v)", jsb, err)
	}
	if txt, _ := sb.MarshalText(); string(txt) != Redacted || sb.LogValue().String() != Redacted {
		t.Fatal("SecretBytes MarshalText/LogValue incorretos")
	}
	if fmt.Sprintf("%T", s) != "config.Secret" {
		t.Fatalf("%%T = %q", fmt.Sprintf("%T", s))
	}
	if s.String() != Redacted || s.GoString() != "config.Secret("+Redacted+")" {
		t.Fatalf("String/GoString = %q %q", s.String(), s.GoString())
	}
	if s.Reveal() != "hunter2-valor-secreto" || !s.IsSet() || Secret("").IsSet() {
		t.Fatal("Reveal/IsSet incorretos")
	}
	js, err := json.Marshal(map[string]any{"k": s})
	if err != nil || strings.Contains(string(js), "hunter2") || !strings.Contains(string(js), Redacted) {
		t.Fatalf("json = %s (%v)", js, err)
	}
	txt, _ := s.MarshalText()
	if string(txt) != Redacted {
		t.Fatalf("MarshalText = %q", txt)
	}
	if s.LogValue().String() != Redacted {
		t.Fatalf("LogValue = %v", s.LogValue())
	}
}

func TestConfigRedactsEverywhere(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarLLMMode] = "openrouter"
	env[VarOpenRouterKey] = sampleOpenRouterKey
	env[VarStripeSecretKey] = "stripe-secret-value"
	env[VarR2SecretAccessKey] = "r2-secret-value"
	env[VarGoogleClientSecret] = "google-secret-value"
	env[VarApplePrivateKey] = "apple-private-key-value"
	env[VarResendAPIKey] = "resend-secret-value"
	cfg := mustLoad(t, env)

	secrets := []string{
		sampleDBPassword, sampleOpenRouterKey, "stripe-secret-value", "r2-secret-value", "google-secret-value",
		"apple-private-key-value", "resend-secret-value", "quote-hmac-key-with-length", "apikey-pepper-with-length",
		"webhook-signing-key-with-length", sampleSessionSecret,
	}
	outputs := map[string]string{
		"String": cfg.String(),
		"%v":     fmt.Sprintf("%v", cfg),
		"%+v":    fmt.Sprintf("%+v", *cfg),
		"%#v":    fmt.Sprintf("%#v", *cfg),
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("boot", "config", cfg, "db", cfg.Database.URL)
	outputs["slog"] = buf.String()
	js, err := json.Marshal(cfg.Signing)
	if err != nil {
		t.Fatal(err)
	}
	outputs["json"] = string(js)

	for name, out := range outputs {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Fatalf("%s vazou %q: %s", name, s, out)
			}
		}
	}
	if !strings.Contains(outputs["String"], VarDatabaseURL+"="+Redacted) {
		t.Fatalf("String não marca DATABASE_URL como redigida: %s", outputs["String"])
	}
	if !strings.Contains(outputs["String"], VarEnv+"=dev") || !strings.Contains(outputs["String"], VarTargetMargin+"=0.3") {
		t.Fatalf("String não mostra valores públicos: %s", outputs["String"])
	}
	if !strings.Contains(outputs["slog"], `"`+VarSessionSecret+`":"`+Redacted+`"`) {
		t.Fatalf("slog não redigiu a sessão: %s", outputs["slog"])
	}
	// Session secret decoded bytes must not be printed either (as hex or base64).
	for name, out := range outputs {
		if strings.Contains(out, "[115 101 115") {
			t.Fatalf("%s vazou os bytes da sessão", name)
		}
	}
	empty := &Config{}
	if !strings.Contains(empty.String(), VarSessionSecret+"=(vazio)") || !strings.Contains(empty.String(), VarTargetMargin+"=(arquivo)") {
		t.Fatalf("String vazio inesperado: %s", empty.String())
	}
}

func TestLoadUsesProcessEnv(t *testing.T) {
	for k, v := range validEnv() {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIListen != "127.0.0.1:8080" {
		t.Fatalf("APIListen = %q", cfg.APIListen)
	}
}

// TestKnownVarsMatchEnvExample keeps the variable names in lock-step with the
// documentation at the repo root.
func TestKnownVarsMatchEnvExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "..", ".env.example")
	f, err := os.Open(path)
	if err != nil {
		t.Skipf(".env.example indisponível (%v)", err)
	}
	defer func() { _ = f.Close() }()
	documented := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if ok {
			documented[strings.TrimSpace(name)] = true
		}
	}
	known := KnownVars()
	extra := ExtraVars()
	for name := range documented {
		if !slices.Contains(known, name) {
			t.Errorf("%s está em .env.example mas config não a lê", name)
		}
	}
	for _, name := range known {
		if !documented[name] && !slices.Contains(extra, name) {
			t.Errorf("%s é lida por config mas não está em .env.example nem em ExtraVars", name)
		}
	}
}

func TestKillFlagsAndMaintenance(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarKillFlags] = " spending, signups,,spending "
	env[VarMaintenance] = "1"
	cfg := mustLoad(t, env)
	if got, want := strings.Join(cfg.KillFlags, ","), "spending,signups"; got != want {
		t.Fatalf("KillFlags = %q, quer %q", got, want)
	}
	if !cfg.Maintenance {
		t.Fatal("Maintenance deveria ser true")
	}

	env = validEnv()
	cfg = mustLoad(t, env)
	if cfg.KillFlags != nil || cfg.Maintenance {
		t.Fatalf("padrões: KillFlags=%v Maintenance=%v", cfg.KillFlags, cfg.Maintenance)
	}

	for _, bad := range []map[string]string{
		{VarKillFlags: "Spending"},
		{VarKillFlags: "rm -rf"},
		{VarMaintenance: "talvez"},
	} {
		env = validEnv()
		for k, v := range bad {
			env[k] = v
		}
		_, err := LoadFrom(lookupFrom(env))
		if err == nil {
			t.Fatalf("%v deveria ser rejeitado", bad)
		}
	}
}

func TestSMTPAddr(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarSMTPAddr] = "localhost:1025"
	if cfg := mustLoad(t, env); cfg.Email.SMTPAddr != "localhost:1025" || !strings.Contains(cfg.String(), VarSMTPAddr+"=localhost:1025") {
		t.Fatalf("carregou errado: %q", cfg.Email.SMTPAddr)
	}
	if cfg := mustLoad(t, validEnv()); cfg.Email.SMTPAddr != "" {
		t.Fatal("padrão vazio")
	}
	for _, v := range []string{"semporta", ":1025", "localhost:99999", "localhost:x"} {
		env := validEnv()
		env[VarSMTPAddr] = v
		_, err := LoadFrom(lookupFrom(env))
		if vars := validationVars(t, err); !slices.Contains(vars, VarSMTPAddr) {
			t.Fatalf("%s=%q: problemas %v", VarSMTPAddr, v, vars)
		}
	}
	prod := validEnv()
	prod[VarEnv] = "prod"
	prod[VarPublicBaseURL] = "https://app.example.com"
	prod[VarAPIBaseURL] = "https://api.example.com"
	prod[VarOriginSecret] = "segredo-real-da-origem"
	delete(prod, VarS3Endpoint)
	prod[VarSMTPAddr] = "localhost:1025"
	_, err := LoadFrom(lookupFrom(prod))
	if vars := validationVars(t, err); !slices.Contains(vars, VarSMTPAddr) {
		t.Fatalf("SMTP em prod: %v", vars)
	}
}

func TestDevLoginOnlyInDev(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarDevLogin] = "1"
	if cfg := mustLoad(t, env); !cfg.Auth.DevLogin {
		t.Fatal("DM_DEV_LOGIN=1 em dev deveria ligar o login local")
	}
	if cfg := mustLoad(t, validEnv()); cfg.Auth.DevLogin {
		t.Fatal("o login local é desligado por padrão")
	}
	for _, e := range []string{EnvStaging, EnvProd} {
		env := validEnv()
		env[VarEnv] = e
		env[VarDevLogin] = "true"
		_, err := LoadFrom(lookupFrom(env))
		if err == nil || !strings.Contains(err.Error(), VarDevLogin) {
			t.Fatalf("%s com DM_DEV_LOGIN deveria falhar citando a variável, veio %v", e, err)
		}
	}
}

func TestAppleNativeClientIDs(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarAppleNativeClientIDs] = "br.com.docmatch, br.com.docmatch.debug"
	cfg := mustLoad(t, env)
	if got, want := strings.Join(cfg.Auth.AppleNativeClientIDs, ","), "br.com.docmatch,br.com.docmatch.debug"; got != want {
		t.Fatalf("AppleNativeClientIDs = %q, quer %q", got, want)
	}
	env[VarAppleNativeClientIDs] = "br.com/docmatch"
	if _, err := LoadFrom(lookupFrom(env)); err == nil {
		t.Fatal("bundle id inválido deveria ser rejeitado")
	}
}

func TestGoogleNativeClientIDs(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarGoogleNativeClientIDs] = " 123-and.apps.googleusercontent.com, 456-ios.apps.googleusercontent.com,,123-and.apps.googleusercontent.com "
	cfg := mustLoad(t, env)
	if got, want := strings.Join(cfg.Auth.GoogleNativeClientIDs, ","), "123-and.apps.googleusercontent.com,456-ios.apps.googleusercontent.com"; got != want {
		t.Fatalf("GoogleNativeClientIDs = %q, quer %q", got, want)
	}
	for _, bad := range []string{"tem espaço.apps", "-começa-com-hífen", "a/b"} {
		env := validEnv()
		env[VarGoogleNativeClientIDs] = bad
		if _, err := LoadFrom(lookupFrom(env)); err == nil {
			t.Fatalf("%q deveria ser rejeitado", bad)
		}
	}
}
