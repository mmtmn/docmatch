// Package estimate turns an EstimateRequest into a signed Quote: it asks the
// harness for the work breakdown, prices it with config/pricing.yaml and the
// latest model prices (margin formula of docs/plano-mestre.md §5), caps it
// at the p99 overrun of recent runs and signs it with DM_QUOTE_HMAC_KEY for
// quote_ttl_minutes. POST /runs later verifies the signature, the expiry
// and the fingerprint of the request before reserving credits.
package estimate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Item kinds and units (schemas/estimate-breakdown.json).
const (
	ItemDocuments    = "documents"
	ItemScanFiles    = "scan_files"
	ItemAudioMinutes = "audio_minutes"
	ItemVideoMinutes = "video_minutes"
	ItemFrames       = "frames"
	ItemLLMTokens    = "llm_tokens"
	ItemEmbeddings   = "embeddings"
	ItemNewTools     = "new_tools"
	ItemWebRequests  = "web_requests"

	UnitFiles    = "files"
	UnitPages    = "pages"
	UnitMB       = "mb"
	UnitMinutes  = "minutes"
	UnitFrames   = "frames"
	UnitTokens   = "tokens"
	UnitTools    = "tools"
	UnitRequests = "requests"
)

// Lanes (openapi Lane).
const (
	LaneApp         = "app"
	LaneAPIBatch    = "api_batch"
	LaneAPIStandard = "api_standard"
	LaneAPIPriority = "api_priority"
)

// Quote statuses (quotes.status).
const (
	StatusOpen     = "open"
	StatusAccepted = "accepted"
	StatusExpired  = "expired"
	StatusUsed     = "used"
)

// Quote kinds (quotes.kind).
const (
	KindInitial    = "initial"
	KindAdditional = "additional"
)

// Item is one cost component.
type Item struct {
	Item     string  `json:"item"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
	Credits  int64   `json:"credits"`
	Note     string  `json:"note,omitempty"`
}

// Calibration is the weekly calibration snapshot.
type Calibration struct {
	SampleRuns   int64   `json:"sample_runs"`
	P99Overrun   float64 `json:"p99_overrun"`
	CalibratedAt string  `json:"calibrated_at,omitempty"`
}

// Breakdown mirrors schemas/estimate-breakdown.json.
type Breakdown struct {
	Lane            string       `json:"lane"`
	LaneMultiplier  float64      `json:"lane_multiplier"`
	Items           []Item       `json:"items"`
	SubtotalCredits int64        `json:"subtotal_credits"`
	MinimumCredits  int64        `json:"minimum_credits"`
	TotalCredits    int64        `json:"total_credits"`
	PricingVersion  string       `json:"pricing_version"`
	Calibration     *Calibration `json:"calibration,omitempty"`
}

// Quote mirrors schemas/quote.json.
type Quote struct {
	ID               string    `json:"id"`
	RunID            *string   `json:"run_id,omitempty"`
	ExpectedCredits  int64     `json:"expected_credits"`
	CapCredits       int64     `json:"cap_credits"`
	Breakdown        Breakdown `json:"breakdown"`
	ExpiresAt        string    `json:"expires_at"`
	Signature        string    `json:"signature"`
	Fingerprint      string    `json:"fingerprint"`
	RequiresApproval bool      `json:"requires_approval"`
	ApprovalReasons  []string  `json:"approval_reasons,omitempty"`
	// ExpectedSeconds is how long the run should take, queue included
	// (config/throughput.yaml); absent on additional quotes and on quotes
	// issued before the field existed.
	ExpectedSeconds int64 `json:"expected_seconds,omitempty"`
	// Large is ExpectedSeconds above large_after_seconds: the clients show the
	// duration and the person gets an e-mail when the run ends.
	Large     bool   `json:"large"`
	CreatedAt string `json:"created_at"`
}

// Expiry parses ExpiresAt.
func (q Quote) Expiry() (time.Time, error) { return time.Parse(time.RFC3339, q.ExpiresAt) }

// Hints mirrors schemas/workflow-config.json#/$defs/LocatorHints.
type Hints struct {
	Sheet            string `json:"sheet,omitempty"`
	HeaderRow        int    `json:"header_row,omitempty"`
	Delimiter        string `json:"delimiter,omitempty"`
	Encoding         string `json:"encoding,omitempty"`
	DateFormat       string `json:"date_format,omitempty"`
	DecimalSeparator string `json:"decimal_separator,omitempty"`
	Timezone         string `json:"timezone,omitempty"`
	Currency         string `json:"currency,omitempty"`
	Language         string `json:"language,omitempty"`
}

// Map renders the hints for the harness (map<string,string>).
func (h *Hints) Map() map[string]string {
	if h == nil {
		return nil
	}
	out := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	put("sheet", h.Sheet)
	if h.HeaderRow > 0 {
		out["header_row"] = strconv.Itoa(h.HeaderRow)
	}
	put("delimiter", h.Delimiter)
	put("encoding", h.Encoding)
	put("date_format", h.DateFormat)
	put("decimal_separator", h.DecimalSeparator)
	put("timezone", h.Timezone)
	put("currency", h.Currency)
	put("language", h.Language)
	return out
}

// SourceRef is openapi RunSource.
type SourceRef struct {
	UploadID string `json:"upload_id"`
	Role     string `json:"role,omitempty"`
	Hints    *Hints `json:"hints,omitempty"`
}

// Options is openapi RunOptions.
type Options struct {
	LlmMode    string   `json:"llm_mode,omitempty"`
	WebSearch  bool     `json:"web_search,omitempty"`
	UseRules   *bool    `json:"use_rules,omitempty"`
	RuleIDs    []string `json:"rule_ids,omitempty"`
	Lane       string   `json:"lane,omitempty"`
	Escalation bool     `json:"escalation,omitempty"`
}

// Request is openapi EstimateRequest (and the base of RunCreateRequest).
type Request struct {
	Sources      []SourceRef `json:"sources"`
	MediaSources []SourceRef `json:"media_sources,omitempty"`
	WorkflowID   string      `json:"workflow_id,omitempty"`
	RulesText    string      `json:"rules_text,omitempty"`
	Options      *Options    `json:"options,omitempty"`
}

// UseRules applies the contract default (true).
func (r Request) UseRules() bool {
	if r.Options == nil || r.Options.UseRules == nil {
		return true
	}
	return *r.Options.UseRules
}

// Lane returns the requested lane or the default for the caller.
func (r Request) Lane() string {
	if r.Options != nil && r.Options.Lane != "" {
		return r.Options.Lane
	}
	return LaneApp
}

// fingerprintDoc is the canonical, sorted form hashed by Fingerprint.
type fingerprintDoc struct {
	Sources    []fingerprintSource `json:"sources"`
	Media      []fingerprintSource `json:"media"`
	WorkflowID string              `json:"workflow_id"`
	RulesText  string              `json:"rules_text"`
	LlmMode    string              `json:"llm_mode"`
	WebSearch  bool                `json:"web_search"`
	UseRules   bool                `json:"use_rules"`
	RuleIDs    []string            `json:"rule_ids"`
	Lane       string              `json:"lane"`
	Escalation bool                `json:"escalation"`
}

type fingerprintSource struct {
	ID     string            `json:"id"`
	SHA256 string            `json:"sha256"`
	Role   string            `json:"role"`
	Hints  map[string]string `json:"hints"`
}

// Fingerprint hashes the sources (ids and content hashes) and options of a
// request, so a run created with different inputs is refused (quote_mismatch).
func Fingerprint(req Request, hashes map[string]string) string {
	conv := func(refs []SourceRef) []fingerprintSource {
		out := make([]fingerprintSource, 0, len(refs))
		for _, s := range refs {
			out = append(out, fingerprintSource{ID: s.UploadID, SHA256: hashes[s.UploadID], Role: s.Role, Hints: s.Hints.Map()})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out
	}
	doc := fingerprintDoc{Sources: conv(req.Sources), Media: conv(req.MediaSources), WorkflowID: req.WorkflowID, RulesText: req.RulesText, UseRules: req.UseRules(), Lane: req.Lane()}
	if req.Options != nil {
		doc.LlmMode, doc.WebSearch, doc.Escalation = req.Options.LlmMode, req.Options.WebSearch, req.Options.Escalation
		doc.RuleIDs = append([]string(nil), req.Options.RuleIDs...)
		sort.Strings(doc.RuleIDs)
	}
	if doc.RuleIDs == nil {
		doc.RuleIDs = []string{}
	}
	data, _ := json.Marshal(doc)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Record is a stored quote.
type Record struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	UserID      *uuid.UUID
	Lane        string
	Expected    int64
	Cap         int64
	Request     json.RawMessage
	Signature   string
	Fingerprint string
	Quote       Quote
	RunID       *uuid.UUID
	Kind        string
	Status      string
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// PublicID renders the API identifier.
func (r Record) PublicID() string { return apix.Encode(apix.PrefixQuote, r.ID) }

// ModelPrice is one row of model_prices (cents per million tokens).
type ModelPrice struct {
	Model              string
	InputCentsPerMTok  int64
	OutputCentsPerMTok int64
	Source             string
}

// Overrun pairs the estimate and the actual spend of a finished run.
type Overrun struct {
	Estimated int64
	Spent     int64
}

// Store is the persistence boundary.
type Store interface {
	InsertQuote(ctx context.Context, rec Record) error
	GetQuote(ctx context.Context, orgID, id uuid.UUID) (Record, bool, error)
	SetQuoteStatus(ctx context.Context, orgID, id uuid.UUID, status string, runID *uuid.UUID) error
	LatestModelPrices(ctx context.Context) ([]ModelPrice, error)
	// SeedModelPrices inserts rows only when the table is empty.
	SeedModelPrices(ctx context.Context, rows []ModelPrice) error
	RecentOverruns(ctx context.Context, since time.Time, limit int) ([]Overrun, error)
	// RecentTimings lists completed runs since a time, newest first (system mode).
	RecentTimings(ctx context.Context, since time.Time, limit int) ([]TimingSample, error)
	// InsertTiming stores the measured duration of a finished run; a second
	// call for the same run changes nothing.
	InsertTiming(ctx context.Context, t RunTiming) error
	// RunsInFlight counts the runs queued or running in every organization,
	// touched after since (older ones are stuck, not ahead in the queue).
	RunsInFlight(ctx context.Context, since time.Time) (int64, error)
}
