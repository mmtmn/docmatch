// Package limits exposes the shared input limits declared in
// packages/contracts/limits.yaml to Go code.
//
// The embed directive cannot reach outside the module, so limits.yaml in this
// directory is a synced copy of the canonical file (make generate). TestEmbeddedMatchesContract
// fails when the two drift. Until the contracts codegen lands (plano, fase de
// contratos), this package is the single source of these numbers for Go.
package limits

import (
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed limits.yaml
var embedded []byte

// Limits mirrors the sections of limits.yaml. Unknown keys are tolerated so a
// contracts change does not break this module before codegen catches up; the
// fields used by the API are validated to be present and positive.
type Limits struct {
	Version int        `yaml:"version"`
	Text    TextLimits `yaml:"text"`
	Files   FileLimits `yaml:"files"`
	HTTP    HTTPLimits `yaml:"http"`
	IDs     IDLimits   `yaml:"ids"`
}

// TextLimits are maximum lengths in Unicode code points after NFC normalization.
type TextLimits struct {
	RulesText              int `yaml:"rules_text"`
	UICustomizationRequest int `yaml:"ui_customization_request"`
	SavedRuleTitle         int `yaml:"saved_rule_title"`
	SavedRuleBody          int `yaml:"saved_rule_body"`
	RulePickerSearch       int `yaml:"rule_picker_search"`
	WorkflowName           int `yaml:"workflow_name"`
	DisplayName            int `yaml:"display_name"`
	CompanyName            int `yaml:"company_name"`
	Email                  int `yaml:"email"`
	PasswordMin            int `yaml:"password_min"`
	PasswordMax            int `yaml:"password_max"`
	APIKeyLabel            int `yaml:"api_key_label"`
	CouponCode             int `yaml:"coupon_code"`
	ContactMessage         int `yaml:"contact_message"`
	AdminNote              int `yaml:"admin_note"`
	FindingFeedbackNote    int `yaml:"finding_feedback_note"`
	BlogComment            int `yaml:"blog_comment"`
}

// FileLimits bound uploads; sizes are absolute ceilings (plans may be lower).
type FileLimits struct {
	UploadedFileNameBytes int `yaml:"uploaded_file_name_bytes"`
	DocumentFileMaxMB     int `yaml:"document_file_max_mb"`
	DocumentRunMaxMB      int `yaml:"document_run_max_mb"`
	MediaFileMaxGB        int `yaml:"media_file_max_gb"`
	MediaFileMaxHours     int `yaml:"media_file_max_hours"`
	MediaRunMaxGB         int `yaml:"media_run_max_gb"`
	MediaRunMaxHours      int `yaml:"media_run_max_hours"`
	ZipMaxEntries         int `yaml:"zip_max_entries"`
	ZipMaxRatio           int `yaml:"zip_max_ratio"`
	ZipMaxUncompressedMB  int `yaml:"zip_max_uncompressed_mb"`
	ZipMaxDepth           int `yaml:"zip_max_depth"`
}

// HTTPLimits size the http.Server and body readers.
type HTTPLimits struct {
	JSONBodyMaxKB      int `yaml:"json_body_max_kb"`
	HeaderMaxKB        int `yaml:"header_max_kb"`
	ReadHeaderTimeoutS int `yaml:"read_header_timeout_s"`
	RequestTimeoutS    int `yaml:"request_timeout_s"`
	SSEIdleTimeoutS    int `yaml:"sse_idle_timeout_s"`
}

// IDLimits bound identifiers and collection sizes.
type IDLimits struct {
	IdempotencyKeyMax       int `yaml:"idempotency_key_max"`
	APIKeyScopesMax         int `yaml:"api_key_scopes_max"`
	FilesPerRunHardMax      int `yaml:"files_per_run_hard_max"`
	MediaFilesPerRunHardMax int `yaml:"media_files_per_run_hard_max"`
}

const (
	kib = 1024
	mib = 1024 * kib
	gib = 1024 * mib
)

// JSONBodyMax is the byte ceiling for JSON request bodies.
func (h HTTPLimits) JSONBodyMax() int64 { return int64(h.JSONBodyMaxKB) * kib }

// HeaderMax is the byte ceiling for request headers (http.Server.MaxHeaderBytes).
func (h HTTPLimits) HeaderMax() int { return h.HeaderMaxKB * kib }

// ReadHeaderTimeout bounds how long the server waits for request headers.
func (h HTTPLimits) ReadHeaderTimeout() time.Duration {
	return time.Duration(h.ReadHeaderTimeoutS) * time.Second
}

// RequestTimeout bounds reading the body and writing the response.
func (h HTTPLimits) RequestTimeout() time.Duration {
	return time.Duration(h.RequestTimeoutS) * time.Second
}

// SSEIdleTimeout is how long a streaming response may stay silent.
func (h HTTPLimits) SSEIdleTimeout() time.Duration {
	return time.Duration(h.SSEIdleTimeoutS) * time.Second
}

// DocumentFileMax is the byte ceiling for one uploaded document.
func (f FileLimits) DocumentFileMax() int64 { return int64(f.DocumentFileMaxMB) * mib }

// DocumentRunMax is the byte ceiling for all documents of one run.
func (f FileLimits) DocumentRunMax() int64 { return int64(f.DocumentRunMaxMB) * mib }

// MediaFileMax is the byte ceiling for one uploaded media file.
func (f FileLimits) MediaFileMax() int64 { return int64(f.MediaFileMaxGB) * gib }

// Parse decodes and validates a limits.yaml document.
func Parse(data []byte) (*Limits, error) {
	var l Limits
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("limits.yaml: %w", err)
	}
	if err := l.validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

// validate checks that every limit the API depends on is present and sane.
// blog_comment may be zero (comments are disabled by contract).
func (l *Limits) validate() error {
	if l.Version < 1 {
		return errors.New("limits.yaml: version ausente ou inválida")
	}
	positive := map[string]int{
		"text.rules_text":                  l.Text.RulesText,
		"text.ui_customization_request":    l.Text.UICustomizationRequest,
		"text.saved_rule_title":            l.Text.SavedRuleTitle,
		"text.saved_rule_body":             l.Text.SavedRuleBody,
		"text.rule_picker_search":          l.Text.RulePickerSearch,
		"text.workflow_name":               l.Text.WorkflowName,
		"text.display_name":                l.Text.DisplayName,
		"text.company_name":                l.Text.CompanyName,
		"text.email":                       l.Text.Email,
		"text.password_min":                l.Text.PasswordMin,
		"text.password_max":                l.Text.PasswordMax,
		"text.api_key_label":               l.Text.APIKeyLabel,
		"text.coupon_code":                 l.Text.CouponCode,
		"text.contact_message":             l.Text.ContactMessage,
		"text.admin_note":                  l.Text.AdminNote,
		"text.finding_feedback_note":       l.Text.FindingFeedbackNote,
		"files.uploaded_file_name_bytes":   l.Files.UploadedFileNameBytes,
		"files.document_file_max_mb":       l.Files.DocumentFileMaxMB,
		"files.document_run_max_mb":        l.Files.DocumentRunMaxMB,
		"files.media_file_max_gb":          l.Files.MediaFileMaxGB,
		"files.media_file_max_hours":       l.Files.MediaFileMaxHours,
		"files.media_run_max_gb":           l.Files.MediaRunMaxGB,
		"files.media_run_max_hours":        l.Files.MediaRunMaxHours,
		"files.zip_max_entries":            l.Files.ZipMaxEntries,
		"files.zip_max_ratio":              l.Files.ZipMaxRatio,
		"files.zip_max_uncompressed_mb":    l.Files.ZipMaxUncompressedMB,
		"files.zip_max_depth":              l.Files.ZipMaxDepth,
		"http.json_body_max_kb":            l.HTTP.JSONBodyMaxKB,
		"http.header_max_kb":               l.HTTP.HeaderMaxKB,
		"http.read_header_timeout_s":       l.HTTP.ReadHeaderTimeoutS,
		"http.request_timeout_s":           l.HTTP.RequestTimeoutS,
		"http.sse_idle_timeout_s":          l.HTTP.SSEIdleTimeoutS,
		"ids.idempotency_key_max":          l.IDs.IdempotencyKeyMax,
		"ids.api_key_scopes_max":           l.IDs.APIKeyScopesMax,
		"ids.files_per_run_hard_max":       l.IDs.FilesPerRunHardMax,
		"ids.media_files_per_run_hard_max": l.IDs.MediaFilesPerRunHardMax,
	}
	var missing []string
	for name, v := range positive {
		if v <= 0 {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("limits.yaml: limites ausentes ou não positivos: %v", missing)
	}
	if l.Text.PasswordMin >= l.Text.PasswordMax {
		return errors.New("limits.yaml: text.password_min precisa ser menor que text.password_max")
	}
	if l.Text.BlogComment < 0 {
		return errors.New("limits.yaml: text.blog_comment não pode ser negativo")
	}
	return nil
}

var (
	once    sync.Once
	cached  *Limits
	loadErr error
)

// Load parses the embedded limits once and caches the result.
func Load() (*Limits, error) {
	once.Do(func() { cached, loadErr = Parse(embedded) })
	return cached, loadErr
}

// Must returns the embedded limits or panics; the embedded file is validated
// by the package tests, so a panic here means a broken build, not bad input.
func Must() *Limits { return must(Load()) }

func must(l *Limits, err error) *Limits {
	if err != nil {
		panic(err)
	}
	return l
}

// Embedded returns the raw bytes of the synced copy (used by the drift test).
func Embedded() []byte { return embedded }
