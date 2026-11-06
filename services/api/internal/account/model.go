// Package account implements the account endpoints of the API (/me,
// /me/password, /me/export and the LGPD deletion) and the domain types for
// users, organizations and memberships that the auth package shares.
//
// Deletion is crypto-shredding plus hard deletes: every organization has a
// random data key in tenant_keys wrapped with a KEK derived from
// DM_SESSION_SECRET (internal/crypto); the erasure job destroys the key
// first, so anything encrypted with it (and any backup of it) becomes noise,
// then deletes the organization, its rows and the user. The request itself
// only schedules the job seven days ahead and blocks logins meanwhile.
//
// Exports are built by the worker (one JSON file per data set, zipped),
// stored in account_exports for 24 hours and downloaded through a signed
// link that needs no session.
package account

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/email"
)

// Errors returned by the service; handlers map them onto catalog codes.
var (
	ErrNotFound            = errors.New("account: not found")
	ErrDeletionPending     = errors.New("account: deletion already requested")
	ErrInvalidCredentials  = errors.New("account: invalid credentials")
	ErrRecentLoginRequired = errors.New("account: recent login required")
	ErrPasswordLength      = errors.New("account: password length out of bounds")
	ErrExportNotReady      = errors.New("account: export not ready")
	ErrExportTooLarge      = errors.New("account: export exceeds the size limit")
	ErrRateLimited         = errors.New("account: too many export requests")
)

// Roles (memberships.role).
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// Locale is the only locale of the product.
const Locale = "pt-BR"

// User is a row of users.
type User struct {
	ID                   uuid.UUID
	Email                string
	DisplayName          string
	GoogleSub            string
	AppleSub             string
	PasswordHash         string
	EmailVerifiedAt      *time.Time
	LastLoginAt          *time.Time
	Notifications        NotificationPrefs
	SuspendedAt          *time.Time
	DeletionRequestedAt  *time.Time
	DeletionScheduledFor *time.Time
	CreatedAt            time.Time
}

// NotificationPrefs mirror the NotificationPrefs schema; nil means default.
type NotificationPrefs struct {
	RunCompleted *bool `json:"run_completed,omitempty"`
	CreditsLow   *bool `json:"credits_low,omitempty"`
	ProductNews  *bool `json:"product_news,omitempty"`
}

// Resolved applies the contract defaults (true, true, false).
func (p NotificationPrefs) Resolved() ResolvedPrefs {
	return ResolvedPrefs{
		RunCompleted: boolOr(p.RunCompleted, true),
		CreditsLow:   boolOr(p.CreditsLow, true),
		ProductNews:  boolOr(p.ProductNews, false),
	}
}

// Merge overlays the non-nil fields of o.
func (p NotificationPrefs) Merge(o NotificationPrefs) NotificationPrefs {
	if o.RunCompleted != nil {
		p.RunCompleted = o.RunCompleted
	}
	if o.CreditsLow != nil {
		p.CreditsLow = o.CreditsLow
	}
	if o.ProductNews != nil {
		p.ProductNews = o.ProductNews
	}
	return p
}

// ResolvedPrefs is what the API returns.
type ResolvedPrefs struct {
	RunCompleted bool `json:"run_completed"`
	CreditsLow   bool `json:"credits_low"`
	ProductNews  bool `json:"product_news"`
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// Org is a row of orgs.
type Org struct {
	ID        uuid.UUID
	Name      string
	Slug      string
	PlanCode  string
	Country   string
	CreatedAt time.Time
}

// Membership is a row of memberships.
type Membership struct {
	OrgID     uuid.UUID
	UserID    uuid.UUID
	Role      string
	CreatedAt time.Time
}

// Subscription is the active subscription of an organization.
type Subscription struct {
	PlanCode           string
	Status             string
	CurrentPeriodEnd   *time.Time
	StripeSubscription string
}

// Tenant is an organization seen by one of its members.
type Tenant struct {
	Org          Org
	Membership   Membership
	Subscription *Subscription
}

// CreditSummary mirrors the CreditSummary schema.
type CreditSummary struct {
	TokenExempt     bool       `json:"token_exempt,omitempty"`
	Balance         int64      `json:"balance"`
	Reserved        int64      `json:"reserved"`
	Available       int64      `json:"available"`
	PlanCredits     int64      `json:"plan_credits"`
	TopupCredits    int64      `json:"topup_credits"`
	PlanCycleEndsAt *time.Time `json:"plan_cycle_ends_at,omitempty"`
	NextExpiryAt    *time.Time `json:"next_expiry_at,omitempty"`
}

// ProfilePatch is what PATCH /me can change on the user.
type ProfilePatch struct {
	DisplayName   *string
	Notifications *NotificationPrefs
}

// Export statuses (account_exports.status).
const (
	ExportPending = "pending"
	ExportReady   = "ready"
	ExportFailed  = "failed"
	ExportExpired = "expired"
)

// Export is a row of account_exports without the archive.
type Export struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	OrgID       uuid.UUID
	Status      string
	RequestedAt time.Time
	ReadyAt     *time.Time
	ExpiresAt   *time.Time
	SizeBytes   int64
	Error       string
}

// SessionInfo is the part of a session that goes into the export.
type SessionInfo struct {
	ID         uuid.UUID `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	IPPrefix   string    `json:"ip_prefix,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
}

// AuditEntry is the part of an audit row that goes into the export.
type AuditEntry struct {
	Seq       int64     `json:"seq"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Target    string    `json:"target"`
	TargetID  string    `json:"target_id,omitempty"`
	Meta      string    `json:"meta"`
	CreatedAt time.Time `json:"created_at"`
}

// Bundle is everything the export contains.
type Bundle struct {
	User         User
	Org          Org
	Membership   Membership
	Subscription *Subscription
	Credits      CreditSummary
	Sessions     []SessionInfo
	Audit        []AuditEntry
}

// EraseReport summarizes an erasure.
type EraseReport struct {
	Erased             bool
	OrgsDeleted        []uuid.UUID
	MembershipsRemoved int
}

// Store is the persistence boundary. Tenant-scoped methods run inside
// db.WithOrgTx; the erasure and the due-erasure listing run in system mode
// (worker only).
type Store interface {
	UserByID(ctx context.Context, id uuid.UUID) (User, bool, error)
	Tenant(ctx context.Context, orgID, userID uuid.UUID) (Tenant, bool, error)
	Credits(ctx context.Context, orgID uuid.UUID) (CreditSummary, error)
	UpdateProfile(ctx context.Context, userID uuid.UUID, p ProfilePatch) error
	UpdateOrgName(ctx context.Context, orgID uuid.UUID, name string) error
	SetPassword(ctx context.Context, userID uuid.UUID, hash string) error
	// RequestDeletion schedules the erasure; ok is false when already requested.
	RequestDeletion(ctx context.Context, userID uuid.UUID, scheduledFor time.Time) (bool, error)
	DueErasures(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error)
	// Erase destroys the tenant keys of the organizations the user solely
	// owns, hard-deletes them and the user. It is a no-op (Erased=false)
	// when the request was cancelled or is not due yet.
	Erase(ctx context.Context, userID uuid.UUID, now time.Time) (EraseReport, error)

	CreateExport(ctx context.Context, userID, orgID uuid.UUID) (Export, error)
	LatestExport(ctx context.Context, userID uuid.UUID) (Export, bool, error)
	ExportByID(ctx context.Context, id uuid.UUID) (Export, bool, error)
	ExportArchive(ctx context.Context, id uuid.UUID) ([]byte, bool, error)
	CompleteExport(ctx context.Context, id uuid.UUID, archive []byte, expiresAt time.Time) error
	FailExport(ctx context.Context, id uuid.UUID, reason string) error
	ExpireExports(ctx context.Context, now time.Time) (int64, error)
	ExportBundle(ctx context.Context, userID, orgID uuid.UUID) (Bundle, error)

	// EnqueueEmail writes an e-mail to the outbox (delivered by the worker).
	EnqueueEmail(ctx context.Context, orgID *uuid.UUID, msg email.Message) error
}

// Enqueuer inserts the background jobs (River); nil disables them and the
// periodic maintenance job picks the work up instead.
type Enqueuer interface {
	EnqueueExport(ctx context.Context, exportID uuid.UUID) error
	EnqueueErase(ctx context.Context, userID uuid.UUID, at time.Time) error
}
