package uploads

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Verdict is the JSON the scanner produces for one upload. It is the
// contract between this worker and the scanner (infra/lambda/scanner in
// production, the dm-scanner binary in dev): the scanner writes exactly this
// document to the SQS queue (DM_SCAN_QUEUE_URL) or to stdout, and the worker
// applies it with Service.ApplyVerdict. Fields are only ever added.
//
// The dev binary is invoked as
//
//	dm-scanner scan --input <file> --file-name <name> --declared-mime <mime> --size <bytes>
//
// and prints one Verdict on stdout, exit status 0 even when rejecting;
// a non-zero exit is a scanner failure and the job retries.
type Verdict struct {
	// Version of this document (1).
	Version int `json:"version"`
	// UploadID is the public id (upl_...) the ticket was issued for.
	UploadID string `json:"upload_id"`
	// Bucket and Key locate the scanned object (quarantine prefix).
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	// Verdict is accept, accept_sanitized (use Rebuilt instead of the
	// original) or reject.
	Verdict string `json:"verdict"`
	// ReasonCode explains a reject: malware, bomb, playlist, type, size,
	// name or unreadable. It maps to the rejected_reason the client sees.
	ReasonCode string `json:"reason_code,omitempty"`
	// Reason is free text for operators (never shown to users).
	Reason string `json:"reason,omitempty"`
	// SHA256 is the hex digest of the original object.
	SHA256 string `json:"sha256"`
	// Mime is the type sniffed from content (empty on reject).
	Mime string `json:"mime,omitempty"`
	// SizeBytes of the original object.
	SizeBytes int64 `json:"size_bytes"`
	// DurationMs of media, when known.
	DurationMs *int64 `json:"duration_ms,omitempty"`
	// Rebuilt lists sanitized or extracted outputs written by the scanner
	// (canonical extracts under the extracts/ prefix).
	Rebuilt []RebuiltOutput `json:"rebuilt,omitempty"`
	// Profile is the scanner's structural profile (pages, sheets, rows,
	// locale hints); opaque to the API, forwarded to the harness.
	Profile json.RawMessage `json:"profile,omitempty"`
	// Scanner identifies the producer.
	Scanner ScannerInfo `json:"scanner"`
	// ScannedAt is when the verdict was produced.
	ScannedAt time.Time `json:"scanned_at"`
}

// RebuiltOutput is one object produced by the scanner.
type RebuiltOutput struct {
	// Kind is sanitized (same format, dangerous parts removed) or extract
	// (canonical text/tables the harness reads).
	Kind      string `json:"kind"`
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	SHA256    string `json:"sha256"`
	Mime      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
}

// ScannerInfo names the scanner build.
type ScannerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Verdict values.
const (
	VerdictAccept          = "accept"
	VerdictAcceptSanitized = "accept_sanitized"
	VerdictReject          = "reject"
)

// VerdictVersion is the current document version.
const VerdictVersion = 1

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrInvalidVerdict is returned by Validate.
var ErrInvalidVerdict = errors.New("uploads: veredito inválido")

// Validate checks the document before it is applied.
func (v Verdict) Validate() error {
	if v.Version != VerdictVersion {
		return fmt.Errorf("%w: versão %d", ErrInvalidVerdict, v.Version)
	}
	if _, err := apix.Decode(apix.PrefixUpload, v.UploadID); err != nil {
		return fmt.Errorf("%w: upload_id", ErrInvalidVerdict)
	}
	switch v.Verdict {
	case VerdictAccept, VerdictAcceptSanitized:
		if !sha256Re.MatchString(v.SHA256) {
			return fmt.Errorf("%w: sha256", ErrInvalidVerdict)
		}
		if v.Verdict == VerdictAcceptSanitized && len(v.Rebuilt) == 0 {
			return fmt.Errorf("%w: accept_sanitized sem rebuilt", ErrInvalidVerdict)
		}
	case VerdictReject:
		if v.ReasonCode == "" {
			return fmt.Errorf("%w: reject sem reason_code", ErrInvalidVerdict)
		}
	default:
		return fmt.Errorf("%w: verdict %q", ErrInvalidVerdict, v.Verdict)
	}
	return nil
}

// RejectedReason maps the scanner's reason code to the contract's enum.
func (v Verdict) RejectedReason() string {
	switch v.ReasonCode {
	case ReasonMalware, ReasonBomb, ReasonPlaylist, ReasonType, ReasonSize, ReasonName, ReasonChecksum:
		return v.ReasonCode
	case "unreadable":
		return ReasonType
	default:
		return ReasonMalware
	}
}

// ParseVerdict decodes and validates a verdict document.
func ParseVerdict(data []byte) (Verdict, error) {
	var v Verdict
	if err := json.Unmarshal(data, &v); err != nil {
		return Verdict{}, fmt.Errorf("%w: %w", ErrInvalidVerdict, err)
	}
	if err := v.Validate(); err != nil {
		return Verdict{}, err
	}
	return v, nil
}
