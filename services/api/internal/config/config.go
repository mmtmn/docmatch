// Package config loads the process configuration from environment variables.
//
// Variable names are exactly the ones documented in .env.example at the repo
// root. Required variables make Load fail fast with a single pt-BR error that
// lists every missing or invalid variable. Secrets are typed as Secret, have no
// defaults and never appear in logs or String().
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Environment names, as in .env.example.
const (
	EnvDev     = "dev"
	EnvStaging = "staging"
	EnvProd    = "prod"
)

// LLM modes, as in .env.example.
const (
	LLMModeOpenRouter = "openrouter"
	LLMModeLocal      = "local"
	LLMModeOff        = "off"
)

// Environment variable names. Keep in sync with .env.example (the names are
// the contract; defaults live here only for non-secret, non-deployment values).
const (
	VarEnv           = "DM_ENV"
	VarPublicBaseURL = "DM_PUBLIC_BASE_URL"
	VarAPIBaseURL    = "DM_API_BASE_URL"
	VarAPIListen     = "DM_API_LISTEN"
	VarAdminListen   = "DM_ADMIN_LISTEN"
	VarOriginSecret  = "DM_ORIGIN_SECRET"
	VarVantaraPath   = "DM_VANTARA_PATH"
	VarKillFlags     = "DM_KILL_FLAGS"
	VarMaintenance   = "DM_MAINTENANCE"

	VarDatabaseURL       = "DATABASE_URL"
	VarDBAppRole         = "DM_DB_APP_ROLE"
	VarAWSRegion         = "AWS_REGION"
	VarS3UploadsBucket   = "DM_S3_UPLOADS_BUCKET"
	VarS3BackupsBucket   = "DM_S3_BACKUPS_BUCKET"
	VarS3Endpoint        = "DM_S3_ENDPOINT"
	VarR2AccountID       = "R2_ACCOUNT_ID"
	VarR2AccessKeyID     = "R2_ACCESS_KEY_ID"
	VarR2SecretAccessKey = "R2_SECRET_ACCESS_KEY"
	VarR2MediaBucket     = "DM_R2_MEDIA_BUCKET"

	VarLLMMode         = "DM_LLM_MODE"
	VarOpenRouterKey   = "OPENROUTER_API_KEY"
	VarLocalLLMBaseURL = "DM_LOCAL_LLM_BASE_URL"
	VarModelsFile      = "DM_MODELS_FILE"
	VarLLMCacheDir     = "DM_LLM_CACHE_DIR"

	VarGoogleClientID     = "GOOGLE_OAUTH_CLIENT_ID"
	VarGoogleClientSecret = "GOOGLE_OAUTH_CLIENT_SECRET"
	VarAppleTeamID        = "APPLE_TEAM_ID"
	VarAppleClientID      = "APPLE_CLIENT_ID"
	VarAppleKeyID         = "APPLE_KEY_ID"
	VarApplePrivateKey    = "APPLE_PRIVATE_KEY"
	VarSessionSecret      = "DM_SESSION_SECRET"

	VarStripeSecretKey      = "STRIPE_SECRET_KEY"
	VarStripeWebhookSecret  = "STRIPE_WEBHOOK_SECRET"
	VarStripePublishableKey = "STRIPE_PUBLISHABLE_KEY"
	VarTargetMargin         = "DM_TARGET_MARGIN"
	VarIncomeTaxRate        = "DM_INCOME_TAX_RATE"
	VarPaymentRate          = "DM_PAYMENT_RATE"
	VarInfraRate            = "DM_INFRA_RATE"
	VarOpenRouterFee        = "DM_OPENROUTER_FEE"
	VarPricingFile          = "DM_PRICING_FILE"

	VarResendAPIKey    = "RESEND_API_KEY"
	VarEmailFrom       = "DM_EMAIL_FROM"
	VarSecurityContact = "DM_SECURITY_CONTACT"
	VarSMTPAddr        = "DM_SMTP_ADDR"

	VarQuoteHMACKey      = "DM_QUOTE_HMAC_KEY"
	VarAPIKeyPepper      = "DM_APIKEY_PEPPER"
	VarWebhookSigningKey = "DM_WEBHOOK_SIGNING_KEY"
)

// Defaults for optional, non-secret values. Deployment-specific values (URLs,
// buckets, account ids) have no defaults on purpose.
const (
	DefaultEnv         = EnvDev
	DefaultDBAppRole   = "docmatch_app"
	DefaultLLMMode     = LLMModeLocal
	DefaultModelsFile  = "config/models.yaml"
	DefaultPricingFile = "config/pricing.yaml"
	DefaultLLMCacheDir = ".cache/llm"

	// minSessionSecretBytes is the decoded length DM_SESSION_SECRET must reach.
	minSessionSecretBytes = 32
	// minKeyChars is the minimum length of the other signing secrets.
	minKeyChars = 16
	// placeholderOriginSecret is the .env.example placeholder, refused in prod.
	placeholderOriginSecret = "change-me"
)

// Config is the typed, validated process configuration.
type Config struct {
	Env           string
	PublicBaseURL *url.URL
	APIBaseURL    *url.URL
	APIListen     string
	AdminListen   string
	OriginSecret  Secret
	VantaraPath   string
	// KillFlags are feature flags forced off from the environment (layer 2 of
	// the kill switches); they win over anything stored in the database.
	KillFlags []string
	// Maintenance makes the API answer 503 to everything except health and version.
	Maintenance bool

	Database Database
	Storage  Storage
	LLM      LLM
	Auth     Auth
	Billing  Billing
	Email    Email
	Signing  Signing
}

// Database holds the PostgreSQL connection settings.
type Database struct {
	// URL is a secret because it carries the password.
	URL Secret
	// AppRole is the NOBYPASSRLS role the API assumes (SET ROLE) after
	// connecting; migrations create it. Empty disables SET ROLE.
	AppRole string
}

// Storage holds S3 (documents) and Cloudflare R2 (media) settings.
type Storage struct {
	AWSRegion       string
	S3UploadsBucket string
	S3BackupsBucket string
	S3Endpoint      *url.URL // MinIO in dev; nil in prod
	R2AccountID     string
	R2AccessKeyID   string
	R2SecretKey     Secret
	R2MediaBucket   string
}

// LLM holds model routing settings.
type LLM struct {
	Mode          string
	OpenRouterKey Secret
	LocalBaseURL  *url.URL
	ModelsFile    string
	CacheDir      string
}

// Auth holds identity provider settings and the session secret.
type Auth struct {
	GoogleClientID     string
	GoogleClientSecret Secret
	AppleTeamID        string
	AppleClientID      string
	AppleKeyID         string
	ApplePrivateKey    Secret
	// SessionSecret is the decoded DM_SESSION_SECRET (32+ random bytes).
	SessionSecret SecretBytes
}

// Billing holds Stripe keys and the optional rate overrides.
type Billing struct {
	StripeSecretKey      Secret
	StripeWebhookSecret  Secret
	StripePublishableKey string
	// Rates override config/pricing.yaml when set (nil means "use the file").
	TargetMargin  *float64
	IncomeTaxRate *float64
	PaymentRate   *float64
	InfraRate     *float64
	OpenRouterFee *float64
	PricingFile   string
}

// Email holds the transactional e-mail settings.
type Email struct {
	ResendAPIKey    Secret
	From            *mail.Address
	SecurityContact *mail.Address
	// SMTPAddr is the host:port of the unauthenticated dev relay (Mailpit);
	// empty outside dev, where Resend is used.
	SMTPAddr string
}

// Signing holds the keys that sign quotes, API keys and outgoing webhooks.
type Signing struct {
	QuoteHMACKey      Secret
	APIKeyPepper      Secret
	WebhookSigningKey Secret
}

// Lookup resolves a variable, like os.LookupEnv.
type Lookup func(name string) (string, bool)

// Load reads the configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.LookupEnv) }

// LoadFrom reads the configuration through lookup. It returns a *ValidationError
// listing every problem found, never just the first one.
func LoadFrom(lookup Lookup) (*Config, error) {
	r := &reader{lookup: lookup}
	cfg := &Config{}

	cfg.Env = r.enum(VarEnv, DefaultEnv, EnvDev, EnvStaging, EnvProd)
	prod := cfg.Env == EnvProd

	cfg.PublicBaseURL = r.urlRequired(VarPublicBaseURL, prod)
	cfg.APIBaseURL = r.urlRequired(VarAPIBaseURL, prod)
	cfg.APIListen = r.listen(VarAPIListen)
	cfg.AdminListen = r.listen(VarAdminListen)
	cfg.OriginSecret = r.secret(VarOriginSecret, prod)
	if prod && cfg.OriginSecret.Reveal() == placeholderOriginSecret {
		r.problem(VarOriginSecret, "não pode manter o valor de exemplo em produção")
	}
	cfg.VantaraPath = r.optional(VarVantaraPath, "")
	cfg.KillFlags = r.list(VarKillFlags)
	cfg.Maintenance = r.boolean(VarMaintenance, false)

	cfg.Database.URL = r.secret(VarDatabaseURL, true)
	if u := cfg.Database.URL.Reveal(); u != "" && !strings.HasPrefix(u, "postgres://") && !strings.HasPrefix(u, "postgresql://") {
		r.problem(VarDatabaseURL, "precisa começar com postgres:// ou postgresql://")
	}
	cfg.Database.AppRole = r.identifier(VarDBAppRole, DefaultDBAppRole)

	cfg.Storage.AWSRegion = r.optional(VarAWSRegion, "")
	cfg.Storage.S3UploadsBucket = r.optional(VarS3UploadsBucket, "")
	cfg.Storage.S3BackupsBucket = r.optional(VarS3BackupsBucket, "")
	cfg.Storage.S3Endpoint = r.urlOptional(VarS3Endpoint, false)
	if prod && cfg.Storage.S3Endpoint != nil {
		r.problem(VarS3Endpoint, "precisa ficar vazia em produção (só o MinIO local usa endpoint)")
	}
	cfg.Storage.R2AccountID = r.optional(VarR2AccountID, "")
	cfg.Storage.R2AccessKeyID = r.optional(VarR2AccessKeyID, "")
	cfg.Storage.R2SecretKey = r.secret(VarR2SecretAccessKey, false)
	cfg.Storage.R2MediaBucket = r.optional(VarR2MediaBucket, "")

	cfg.LLM.Mode = r.enum(VarLLMMode, DefaultLLMMode, LLMModeOpenRouter, LLMModeLocal, LLMModeOff)
	cfg.LLM.OpenRouterKey = r.secret(VarOpenRouterKey, cfg.LLM.Mode == LLMModeOpenRouter)
	cfg.LLM.LocalBaseURL = r.urlOptional(VarLocalLLMBaseURL, false)
	if cfg.LLM.Mode == LLMModeLocal && cfg.LLM.LocalBaseURL == nil {
		r.problem(VarLocalLLMBaseURL, "obrigatória quando "+VarLLMMode+"=local")
	}
	cfg.LLM.ModelsFile = r.optional(VarModelsFile, DefaultModelsFile)
	cfg.LLM.CacheDir = r.optional(VarLLMCacheDir, DefaultLLMCacheDir)

	cfg.Auth.GoogleClientID = r.optional(VarGoogleClientID, "")
	cfg.Auth.GoogleClientSecret = r.secret(VarGoogleClientSecret, false)
	cfg.Auth.AppleTeamID = r.optional(VarAppleTeamID, "")
	cfg.Auth.AppleClientID = r.optional(VarAppleClientID, "")
	cfg.Auth.AppleKeyID = r.optional(VarAppleKeyID, "")
	cfg.Auth.ApplePrivateKey = r.secret(VarApplePrivateKey, false)
	cfg.Auth.SessionSecret = r.base64Secret(VarSessionSecret, minSessionSecretBytes)

	cfg.Billing.StripeSecretKey = r.secret(VarStripeSecretKey, false)
	cfg.Billing.StripeWebhookSecret = r.secret(VarStripeWebhookSecret, false)
	cfg.Billing.StripePublishableKey = r.optional(VarStripePublishableKey, "")
	cfg.Billing.TargetMargin = r.rate(VarTargetMargin)
	cfg.Billing.IncomeTaxRate = r.rate(VarIncomeTaxRate)
	cfg.Billing.PaymentRate = r.rate(VarPaymentRate)
	cfg.Billing.InfraRate = r.rate(VarInfraRate)
	cfg.Billing.OpenRouterFee = r.rate(VarOpenRouterFee)
	cfg.Billing.PricingFile = r.optional(VarPricingFile, DefaultPricingFile)

	cfg.Email.ResendAPIKey = r.secret(VarResendAPIKey, false)
	cfg.Email.From = r.address(VarEmailFrom)
	cfg.Email.SecurityContact = r.address(VarSecurityContact)
	cfg.Email.SMTPAddr = r.hostPort(VarSMTPAddr)
	if prod && cfg.Email.SMTPAddr != "" {
		r.problem(VarSMTPAddr, "precisa ficar vazia em produção (só o Mailpit local usa SMTP)")
	}

	cfg.Signing.QuoteHMACKey = r.key(VarQuoteHMACKey)
	cfg.Signing.APIKeyPepper = r.key(VarAPIKeyPepper)
	cfg.Signing.WebhookSigningKey = r.key(VarWebhookSigningKey)

	if err := r.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// IsProd reports whether the process runs with DM_ENV=prod.
func (c *Config) IsProd() bool { return c.Env == EnvProd }

// IsDev reports whether the process runs with DM_ENV=dev.
func (c *Config) IsDev() bool { return c.Env == EnvDev }

// AllowedOrigins returns the browser origins allowed by CORS and by the Origin
// check on state-changing requests: the public site and the API itself.
func (c *Config) AllowedOrigins() []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range []*url.URL{c.PublicBaseURL, c.APIBaseURL} {
		if u == nil {
			continue
		}
		o := strings.ToLower(u.Scheme + "://" + u.Host)
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out
}

// String renders every setting with secrets redacted; safe for boot logs.
func (c *Config) String() string {
	items := c.redacted()
	parts := make([]string, 0, len(items))
	for _, kv := range items {
		parts = append(parts, kv[0]+"="+kv[1])
	}
	return strings.Join(parts, " ")
}

// LogValue implements slog.LogValuer with secrets redacted.
func (c *Config) LogValue() slog.Value {
	items := c.redacted()
	attrs := make([]slog.Attr, 0, len(items))
	for _, kv := range items {
		attrs = append(attrs, slog.String(kv[0], kv[1]))
	}
	return slog.GroupValue(attrs...)
}

func urlString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}

func addrString(a *mail.Address) string {
	if a == nil {
		return ""
	}
	return a.String()
}

func rateString(f *float64) string {
	if f == nil {
		return "(arquivo)"
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

func presence(s Secret) string {
	if s.IsSet() {
		return Redacted
	}
	return "(vazio)"
}

// redacted lists variable/value pairs in .env.example order; secrets are
// shown only as present or empty.
func (c *Config) redacted() [][2]string {
	sessionSecret := "(vazio)"
	if len(c.Auth.SessionSecret) > 0 {
		sessionSecret = Redacted
	}
	return [][2]string{
		{VarEnv, c.Env},
		{VarPublicBaseURL, urlString(c.PublicBaseURL)},
		{VarAPIBaseURL, urlString(c.APIBaseURL)},
		{VarAPIListen, c.APIListen},
		{VarAdminListen, c.AdminListen},
		{VarOriginSecret, presence(c.OriginSecret)},
		{VarVantaraPath, c.VantaraPath},
		{VarKillFlags, strings.Join(c.KillFlags, ",")},
		{VarMaintenance, strconv.FormatBool(c.Maintenance)},
		{VarDatabaseURL, presence(c.Database.URL)},
		{VarDBAppRole, c.Database.AppRole},
		{VarAWSRegion, c.Storage.AWSRegion},
		{VarS3UploadsBucket, c.Storage.S3UploadsBucket},
		{VarS3BackupsBucket, c.Storage.S3BackupsBucket},
		{VarS3Endpoint, urlString(c.Storage.S3Endpoint)},
		{VarR2AccountID, c.Storage.R2AccountID},
		{VarR2AccessKeyID, c.Storage.R2AccessKeyID},
		{VarR2SecretAccessKey, presence(c.Storage.R2SecretKey)},
		{VarR2MediaBucket, c.Storage.R2MediaBucket},
		{VarLLMMode, c.LLM.Mode},
		{VarOpenRouterKey, presence(c.LLM.OpenRouterKey)},
		{VarLocalLLMBaseURL, urlString(c.LLM.LocalBaseURL)},
		{VarModelsFile, c.LLM.ModelsFile},
		{VarLLMCacheDir, c.LLM.CacheDir},
		{VarGoogleClientID, c.Auth.GoogleClientID},
		{VarGoogleClientSecret, presence(c.Auth.GoogleClientSecret)},
		{VarAppleTeamID, c.Auth.AppleTeamID},
		{VarAppleClientID, c.Auth.AppleClientID},
		{VarAppleKeyID, c.Auth.AppleKeyID},
		{VarApplePrivateKey, presence(c.Auth.ApplePrivateKey)},
		{VarSessionSecret, sessionSecret},
		{VarStripeSecretKey, presence(c.Billing.StripeSecretKey)},
		{VarStripeWebhookSecret, presence(c.Billing.StripeWebhookSecret)},
		{VarStripePublishableKey, c.Billing.StripePublishableKey},
		{VarTargetMargin, rateString(c.Billing.TargetMargin)},
		{VarIncomeTaxRate, rateString(c.Billing.IncomeTaxRate)},
		{VarPaymentRate, rateString(c.Billing.PaymentRate)},
		{VarInfraRate, rateString(c.Billing.InfraRate)},
		{VarOpenRouterFee, rateString(c.Billing.OpenRouterFee)},
		{VarPricingFile, c.Billing.PricingFile},
		{VarResendAPIKey, presence(c.Email.ResendAPIKey)},
		{VarEmailFrom, addrString(c.Email.From)},
		{VarSecurityContact, addrString(c.Email.SecurityContact)},
		{VarSMTPAddr, c.Email.SMTPAddr},
		{VarQuoteHMACKey, presence(c.Signing.QuoteHMACKey)},
		{VarAPIKeyPepper, presence(c.Signing.APIKeyPepper)},
		{VarWebhookSigningKey, presence(c.Signing.WebhookSigningKey)},
	}
}

// Problem is one invalid or missing variable.
type Problem struct {
	Var     string
	Message string
}

func (p Problem) String() string { return p.Var + ": " + p.Message }

// ValidationError lists every problem found by Load, sorted by variable name.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "configuração inválida (%d problema", len(e.Problems))
	if len(e.Problems) != 1 {
		b.WriteString("s")
	}
	b.WriteString("):")
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p.String())
	}
	return b.String()
}

// Vars returns the names of the offending variables, sorted.
func (e *ValidationError) Vars() []string {
	out := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		out = append(out, p.Var)
	}
	return out
}

// reader accumulates problems while reading variables.
type reader struct {
	lookup   Lookup
	problems []Problem
}

func (r *reader) problem(name, msg string) {
	r.problems = append(r.problems, Problem{Var: name, Message: msg})
}

func (r *reader) err() error {
	if len(r.problems) == 0 {
		return nil
	}
	sort.SliceStable(r.problems, func(i, j int) bool { return r.problems[i].Var < r.problems[j].Var })
	return &ValidationError{Problems: r.problems}
}

// get returns the trimmed value and whether it is non-empty.
func (r *reader) get(name string) (string, bool) {
	v, ok := r.lookup(name)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

func (r *reader) optional(name, def string) string {
	if v, ok := r.get(name); ok {
		return v
	}
	return def
}

// list reads a comma-separated list of flag names. Items are trimmed, empty
// items dropped, duplicates removed; each item must be a simple lowercase
// identifier so a typo never silently disables the wrong switch.
func (r *reader) list(name string) []string {
	v, ok := r.get(name)
	if !ok {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		for i, c := range item {
			isLower := (c >= 'a' && c <= 'z') || c == '_'
			isDigit := c >= '0' && c <= '9'
			if !isLower && (!isDigit || i == 0) {
				r.problem(name, fmt.Sprintf("item %q inválido (use letras minúsculas, dígitos e _, separados por vírgula)", item))
				return nil
			}
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// boolean reads a switch: 1/0, true/false, on/off, yes/no, sim/não.
func (r *reader) boolean(name string, def bool) bool {
	v, ok := r.get(name)
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "on", "yes", "sim":
		return true
	case "0", "false", "off", "no", "nao", "não":
		return false
	}
	r.problem(name, fmt.Sprintf("valor %q inválido (use 1 ou 0)", v))
	return def
}

func (r *reader) required(name string) (string, bool) {
	v, ok := r.get(name)
	if !ok {
		r.problem(name, "obrigatória e ausente")
	}
	return v, ok
}

func (r *reader) enum(name, def string, allowed ...string) string {
	v := r.optional(name, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	r.problem(name, fmt.Sprintf("valor %q inválido (esperado %s)", v, strings.Join(allowed, ", ")))
	return v
}

func (r *reader) secret(name string, required bool) Secret {
	v, ok := r.get(name)
	if !ok && required {
		r.problem(name, "obrigatória e ausente")
	}
	return Secret(v)
}

// key reads a mandatory signing secret with a minimum length.
func (r *reader) key(name string) Secret {
	v, ok := r.required(name)
	if ok && len(v) < minKeyChars {
		r.problem(name, fmt.Sprintf("precisa ter pelo menos %d caracteres", minKeyChars))
	}
	return Secret(v)
}

func (r *reader) base64Secret(name string, minBytes int) SecretBytes {
	v, ok := r.required(name)
	if !ok {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(v)
	}
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(v)
	}
	if err != nil {
		r.problem(name, "precisa ser base64 válido")
		return nil
	}
	if len(raw) < minBytes {
		r.problem(name, fmt.Sprintf("precisa decodificar para pelo menos %d bytes (tem %d)", minBytes, len(raw)))
		return nil
	}
	return raw
}

func parseHTTPURL(v string) (*url.URL, error) {
	u, err := url.Parse(v)
	if err != nil {
		return nil, errors.New("não é uma URL válida")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("precisa usar http:// ou https://")
	}
	if u.Host == "" {
		return nil, errors.New("precisa ter host")
	}
	if u.User != nil {
		return nil, errors.New("não pode conter credenciais")
	}
	return u, nil
}

func (r *reader) urlRequired(name string, httpsOnly bool) *url.URL {
	v, ok := r.required(name)
	if !ok {
		return nil
	}
	return r.checkURL(name, v, httpsOnly)
}

func (r *reader) urlOptional(name string, httpsOnly bool) *url.URL {
	v, ok := r.get(name)
	if !ok {
		return nil
	}
	return r.checkURL(name, v, httpsOnly)
}

func (r *reader) checkURL(name, v string, httpsOnly bool) *url.URL {
	u, err := parseHTTPURL(v)
	if err != nil {
		r.problem(name, err.Error())
		return nil
	}
	if httpsOnly && u.Scheme != "https" {
		r.problem(name, "precisa usar https:// em produção")
		return nil
	}
	return u
}

func (r *reader) listen(name string) string {
	v, ok := r.required(name)
	if !ok {
		return ""
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		r.problem(name, "precisa ter o formato host:porta")
		return ""
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		r.problem(name, "porta inválida")
		return ""
	}
	if host != "" && net.ParseIP(host) == nil && host != "localhost" {
		r.problem(name, "host precisa ser um IP ou localhost")
		return ""
	}
	return v
}

// hostPort reads an optional host:port value (SMTP relay).
func (r *reader) hostPort(name string) string {
	v, ok := r.get(name)
	if !ok {
		return ""
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil || host == "" {
		r.problem(name, "precisa ter o formato host:porta")
		return ""
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		r.problem(name, "porta inválida")
		return ""
	}
	return v
}

// identifier reads a value that is used as an SQL identifier (role name).
func (r *reader) identifier(name, def string) string {
	v := r.optional(name, def)
	for i, c := range v {
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		valid := isLetter || (isDigit && i > 0)
		if !valid {
			r.problem(name, "precisa ser um identificador SQL simples (letras, dígitos e _)")
			return ""
		}
	}
	if len(v) > 63 {
		r.problem(name, "precisa ter no máximo 63 caracteres")
		return ""
	}
	return v
}

func (r *reader) rate(name string) *float64 {
	v, ok := r.get(name)
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f >= 1 {
		r.problem(name, "precisa ser um número entre 0 e 1 (ex.: 0.30)")
		return nil
	}
	return &f
}

func (r *reader) address(name string) *mail.Address {
	v, ok := r.get(name)
	if !ok {
		return nil
	}
	a, err := mail.ParseAddress(v)
	if err != nil {
		r.problem(name, "precisa ser um endereço de e-mail válido (ex.: nome <email@dominio>)")
		return nil
	}
	return a
}

// KnownVars lists every variable Load reads, in .env.example order plus the
// migration-time extras. The package tests check it against .env.example.
func KnownVars() []string {
	return []string{
		VarEnv, VarPublicBaseURL, VarAPIBaseURL, VarAPIListen, VarAdminListen, VarOriginSecret, VarVantaraPath,
		VarKillFlags, VarMaintenance,
		VarDatabaseURL, VarDBAppRole, VarAWSRegion, VarS3UploadsBucket, VarS3BackupsBucket, VarS3Endpoint,
		VarR2AccountID, VarR2AccessKeyID, VarR2SecretAccessKey, VarR2MediaBucket,
		VarLLMMode, VarOpenRouterKey, VarLocalLLMBaseURL, VarModelsFile, VarLLMCacheDir,
		VarGoogleClientID, VarGoogleClientSecret, VarAppleTeamID, VarAppleClientID, VarAppleKeyID, VarApplePrivateKey, VarSessionSecret,
		VarStripeSecretKey, VarStripeWebhookSecret, VarStripePublishableKey,
		VarTargetMargin, VarIncomeTaxRate, VarPaymentRate, VarInfraRate, VarOpenRouterFee, VarPricingFile,
		VarResendAPIKey, VarEmailFrom, VarSecurityContact, VarSMTPAddr,
		VarQuoteHMACKey, VarAPIKeyPepper, VarWebhookSigningKey,
	}
}

// ExtraVars are variables read by this service that .env.example does not
// list yet (documented in services/api/README.md).
func ExtraVars() []string { return []string{VarDBAppRole} }
