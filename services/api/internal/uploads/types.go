// Package uploads issues presigned upload tickets (S3 POST for documents,
// R2 PUT for media), confirms the object arrived with the right checksum,
// hands it to the scanner and records the scanner's verdict. A file is only
// usable by a run once its status is clean.
package uploads

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Kind is the upload family (openapi UploadKind).
type Kind string

// Kinds.
const (
	KindDocument Kind = "document"
	KindMedia    Kind = "media"
)

// Status is the persisted state (uploads.status).
type Status string

// Statuses (mirror the CHECK constraint).
const (
	StatusPending  Status = "pending"
	StatusScanning Status = "scanning"
	StatusClean    Status = "clean"
	StatusInfected Status = "infected"
	StatusRejected Status = "rejected"
	StatusDeleted  Status = "deleted"
)

// API statuses (openapi UploadStatus).
const (
	APIPending  = "pending"
	APIScanning = "scanning"
	APIReady    = "ready"
	APIRejected = "rejected"
	APIExpired  = "expired"
)

// Rejection reasons (openapi UploadRejectedReason).
const (
	ReasonType     = "type"
	ReasonSize     = "size"
	ReasonMalware  = "malware"
	ReasonBomb     = "bomb"
	ReasonPlaylist = "playlist"
	ReasonName     = "name"
	ReasonChecksum = "checksum"
)

// Upload is the domain record.
type Upload struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	UserID       *uuid.UUID
	Kind         Kind
	Store        string
	Role         string
	FileName     string
	DeclaredMime string
	DetectedMime string
	SizeBytes    int64
	SHA256       []byte
	StorageKey   string
	Status       Status
	RejectedRsn  string
	ScanResult   json.RawMessage
	DurationMs   *int64
	CreatedAt    time.Time
	TicketExpiry *time.Time
	CompletedAt  *time.Time
	ScannedAt    *time.Time
	ExpiresAt    *time.Time
}

// PublicID renders the API identifier.
func (u Upload) PublicID() string { return apix.Encode(apix.PrefixUpload, u.ID) }

// Ready reports whether a run may use the file.
func (u Upload) Ready() bool { return u.Status == StatusClean }

// Mime is the sniffed type when known, else the declared one.
func (u Upload) Mime() string {
	if u.DetectedMime != "" {
		return u.DetectedMime
	}
	return u.DeclaredMime
}

// APIStatus maps the persisted state to the contract's enum.
func (u Upload) APIStatus(now time.Time) string {
	switch u.Status {
	case StatusPending:
		if u.TicketExpiry != nil && now.After(*u.TicketExpiry) {
			return APIExpired
		}
		return APIPending
	case StatusScanning:
		return APIScanning
	case StatusClean:
		return APIReady
	default:
		return APIRejected
	}
}

// View is the JSON shape of openapi Upload.
type View struct {
	ID             string `json:"id"`
	Kind           Kind   `json:"kind"`
	FileName       string `json:"file_name"`
	SizeBytes      int64  `json:"size_bytes"`
	Mime           string `json:"mime,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Status         string `json:"status"`
	RejectedReason string `json:"rejected_reason,omitempty"`
	DurationMs     *int64 `json:"duration_ms,omitempty"`
	Role           string `json:"role,omitempty"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
}

// View renders the upload for the API.
func (u Upload) View(now time.Time) View {
	v := View{
		ID: u.PublicID(), Kind: u.Kind, FileName: u.FileName, SizeBytes: u.SizeBytes,
		Status: u.APIStatus(now), DurationMs: u.DurationMs, Role: u.Role,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339), ExpiresAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
	if u.ExpiresAt != nil {
		v.ExpiresAt = u.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if u.Status == StatusClean || u.Status == StatusRejected || u.Status == StatusInfected {
		v.Mime = u.Mime()
	}
	if len(u.SHA256) == 32 {
		v.SHA256 = hex.EncodeToString(u.SHA256)
	}
	if v.Status == APIRejected {
		v.RejectedReason = u.RejectedRsn
		if v.RejectedReason == "" {
			v.RejectedReason = ReasonMalware
		}
	}
	return v
}

// Store is the persistence boundary.
type Store interface {
	CreateUpload(ctx context.Context, u Upload) (Upload, error)
	GetUpload(ctx context.Context, orgID, id uuid.UUID) (Upload, bool, error)
	ListUploads(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]Upload, error)
	UpdateUpload(ctx context.Context, u Upload) (Upload, error)
	// GetUploadAny finds an upload across tenants (verdicts arriving from
	// the scanner queue carry no tenant context).
	GetUploadAny(ctx context.Context, id uuid.UUID) (Upload, bool, error)
}
