package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// Defaults.
const (
	DefaultDeletionGrace     = 7 * 24 * time.Hour
	DefaultExportTTL         = 24 * time.Hour
	DefaultRecentLoginWindow = 15 * time.Minute
	// StalePendingAfter is how long a pending export may sit before a new
	// request replaces it (the job was lost).
	StalePendingAfter = time.Hour
	// DownloadPath is the route of the signed download link.
	DownloadPath = "/v1/me/export/download"
	erasureBatch = 100
)

// Audit actions recorded by this package.
const (
	ActionProfileUpdated    = "account.profile_updated"
	ActionPasswordSet       = "account.password_set"
	ActionDeletionRequested = "account.deletion_requested"
	ActionErased            = "account.erased"
	ActionExportRequested   = "account.export_requested"
	ActionExportReady       = "account.export_ready"
)

// Recorder appends audit entries (audit.Service).
type Recorder interface {
	Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error)
}

// Deps are the collaborators of the service.
type Deps struct {
	Store        Store
	Sessions     *session.Manager
	Keys         *crypto.Keyring
	Limits       *limits.Limits
	Entitlements *Catalog
	Audit        Recorder
	// Enqueue inserts the worker jobs; nil leaves the work to the periodic
	// maintenance job.
	Enqueue Enqueuer
	// Limiter bounds export requests per user (default: in-memory GCRA).
	Limiter      ratelimit.Limiter
	ExportPolicy ratelimit.Policy
	// APIBaseURL builds the download link.
	APIBaseURL *url.URL
	// SupportContact goes into the deletion e-mail (DM_SECURITY_CONTACT).
	SupportContact string
	Logger         *slog.Logger
	Now            func() time.Time

	DeletionGrace     time.Duration
	ExportTTL         time.Duration
	RecentLoginWindow time.Duration
}

// Service implements the account operations.
type Service struct {
	d Deps
}

// New validates the dependencies and returns the service.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Sessions == nil || d.Keys == nil {
		return nil, errors.New("account: Store, Sessions and Keys are required")
	}
	if d.Limits == nil {
		d.Limits = limits.Must()
	}
	if d.Audit == nil {
		return nil, errors.New("account: Audit is required")
	}
	if d.Limiter == nil {
		d.Limiter = ratelimit.NewMemory()
	}
	if d.ExportPolicy.Limit == 0 {
		d.ExportPolicy = ratelimit.Policy{Limit: 10, Period: time.Minute}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.DeletionGrace <= 0 {
		d.DeletionGrace = DefaultDeletionGrace
	}
	if d.ExportTTL <= 0 {
		d.ExportTTL = DefaultExportTTL
	}
	if d.RecentLoginWindow <= 0 {
		d.RecentLoginWindow = DefaultRecentLoginWindow
	}
	return &Service{d: d}, nil
}

func userActor(id uuid.UUID) audit.Actor {
	return audit.Actor{Type: audit.ActorUser, ID: id.String()}
}

func (s *Service) record(ctx context.Context, orgID uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) error {
	var org *uuid.UUID
	if orgID != uuid.Nil {
		org = &orgID
	}
	if _, err := s.d.Audit.Record(ctx, org, actor, action, target, meta); err != nil {
		return fmt.Errorf("account: auditoria: %w", err)
	}
	return nil
}

// me loads the user and the tenant of the principal.
func (s *Service) me(ctx context.Context, p session.Principal) (User, Tenant, error) {
	u, ok, err := s.d.Store.UserByID(ctx, p.UserID)
	if err != nil {
		return User{}, Tenant{}, err
	}
	if !ok {
		return User{}, Tenant{}, ErrNotFound
	}
	t, ok, err := s.d.Store.Tenant(ctx, p.OrgID, p.UserID)
	if err != nil {
		return User{}, Tenant{}, err
	}
	if !ok {
		return User{}, Tenant{}, ErrNotFound
	}
	return u, t, nil
}

// Me renders GET /me.
func (s *Service) Me(ctx context.Context, p session.Principal) (MeView, error) {
	u, t, err := s.me(ctx, p)
	if err != nil {
		return MeView{}, err
	}
	credits, err := s.d.Store.Credits(ctx, t.Org.ID)
	if err != nil {
		return MeView{}, err
	}
	ent, _ := s.d.Entitlements.For(t.Org.PlanCode)
	return MeView{
		User: NewUserView(u, t.Membership.Role), Org: NewOrgView(t.Org, t.Subscription),
		Credits: credits, Entitlements: ent, Notifications: u.Notifications.Resolved(),
	}, nil
}

// MePatch is the body of PATCH /me.
type MePatch struct {
	DisplayName   *string            `json:"display_name"`
	CompanyName   *string            `json:"company_name"`
	Notifications *NotificationPrefs `json:"notifications"`
}

// FieldError is one invalid field of a request.
type FieldError struct {
	Path string
	Code string
	Max  int
}

// ValidationError lists invalid fields.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string { return "account: validation failed" }

// TooLong reports whether any field exceeded its limit (422 instead of 400).
func (e *ValidationError) TooLong() bool {
	for _, f := range e.Fields {
		if f.Code == "text_too_long" {
			return true
		}
	}
	return false
}

func validateLine(path, value string, max int) (string, *FieldError) {
	v, err := httpx.ValidateText(value, max, true)
	switch {
	case errors.Is(err, httpx.ErrTooLong):
		return "", &FieldError{Path: path, Code: "text_too_long", Max: max}
	case err != nil, v == "":
		return "", &FieldError{Path: path, Code: "invalid"}
	}
	return v, nil
}

// UpdateMe applies PATCH /me.
func (s *Service) UpdateMe(ctx context.Context, p session.Principal, patch MePatch) (MeView, error) {
	if patch.DisplayName == nil && patch.CompanyName == nil && patch.Notifications == nil {
		return MeView{}, &ValidationError{Fields: []FieldError{{Path: "/", Code: "required"}}}
	}
	var ve ValidationError
	var profile ProfilePatch
	if patch.DisplayName != nil {
		v, fe := validateLine("/display_name", *patch.DisplayName, s.d.Limits.Text.DisplayName)
		if fe != nil {
			ve.Fields = append(ve.Fields, *fe)
		} else {
			profile.DisplayName = &v
		}
	}
	var company string
	if patch.CompanyName != nil {
		v, fe := validateLine("/company_name", *patch.CompanyName, s.d.Limits.Text.CompanyName)
		if fe != nil {
			ve.Fields = append(ve.Fields, *fe)
		} else {
			company = v
		}
	}
	if len(ve.Fields) > 0 {
		return MeView{}, &ve
	}
	u, t, err := s.me(ctx, p)
	if err != nil {
		return MeView{}, err
	}
	if patch.CompanyName != nil && t.Membership.Role != RoleOwner && t.Membership.Role != RoleAdmin {
		return MeView{}, ErrForbidden
	}
	changed := map[string]any{}
	if patch.Notifications != nil {
		merged := u.Notifications.Merge(*patch.Notifications)
		profile.Notifications = &merged
		changed["notifications"] = merged.Resolved()
	}
	if profile.DisplayName != nil {
		changed["display_name"] = true
	}
	if profile.DisplayName != nil || profile.Notifications != nil {
		if err := s.d.Store.UpdateProfile(ctx, u.ID, profile); err != nil {
			return MeView{}, err
		}
	}
	if patch.CompanyName != nil {
		if err := s.d.Store.UpdateOrgName(ctx, t.Org.ID, company); err != nil {
			return MeView{}, err
		}
		changed["company_name"] = true
	}
	if err := s.record(ctx, t.Org.ID, userActor(u.ID), ActionProfileUpdated, audit.Target{Type: "user", ID: u.ID.String()}, changed); err != nil {
		return MeView{}, err
	}
	return s.Me(ctx, p)
}

// SetPassword applies PUT /me/password: the current password when one
// exists, a recent login otherwise; every other session of the user is
// revoked afterwards.
func (s *Service) SetPassword(ctx context.Context, p session.Principal, current, next string) error {
	if n := httpx.CountChars(next); n < s.d.Limits.Text.PasswordMin || n > s.d.Limits.Text.PasswordMax {
		return ErrPasswordLength
	}
	u, ok, err := s.d.Store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if u.PasswordHash != "" {
		if current == "" {
			return ErrInvalidCredentials
		}
		match, err := crypto.VerifyPassword(u.PasswordHash, current)
		if err != nil || !match {
			return ErrInvalidCredentials
		}
	} else if s.d.Now().Sub(p.SessionCreatedAt) > s.d.RecentLoginWindow {
		return ErrRecentLoginRequired
	}
	hash, err := crypto.HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.d.Store.SetPassword(ctx, u.ID, hash); err != nil {
		return err
	}
	revoked, err := s.d.Sessions.Store().RevokeUser(ctx, u.ID, p.SessionID)
	if err != nil {
		return err
	}
	return s.record(ctx, p.OrgID, userActor(u.ID), ActionPasswordSet, audit.Target{Type: "user", ID: u.ID.String()},
		map[string]any{"first": u.PasswordHash == "", "sessions_revoked": revoked})
}

// RequestDeletion applies DELETE /me: schedules the erasure after the grace
// period, revokes every session, e-mails a confirmation and enqueues the job.
func (s *Service) RequestDeletion(ctx context.Context, p session.Principal) (time.Time, error) {
	u, t, err := s.me(ctx, p)
	if err != nil {
		return time.Time{}, err
	}
	when := s.d.Now().Add(s.d.DeletionGrace).UTC()
	ok, err := s.d.Store.RequestDeletion(ctx, u.ID, when)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		return time.Time{}, ErrDeletionPending
	}
	if _, err := s.d.Sessions.Store().RevokeUser(ctx, u.ID, uuid.Nil); err != nil {
		return time.Time{}, err
	}
	if err := s.record(ctx, t.Org.ID, userActor(u.ID), ActionDeletionRequested, audit.Target{Type: "user", ID: u.ID.String()},
		map[string]any{"scheduled_for": when}); err != nil {
		return time.Time{}, err
	}
	orgID := t.Org.ID
	if err := s.d.Store.EnqueueEmail(ctx, &orgID, email.DeletionScheduledMessage(u.Email, when, s.d.SupportContact)); err != nil {
		return time.Time{}, err
	}
	if s.d.Enqueue != nil {
		if err := s.d.Enqueue.EnqueueErase(ctx, u.ID, when); err != nil {
			s.d.Logger.Warn("exclusão: job não enfileirado; a manutenção periódica cobre", slog.String("err", err.Error()))
		}
	}
	return when, nil
}

// ExportStatus mirrors the ExportStatus schema.
type ExportStatus struct {
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requested_at"`
	DownloadURL *string    `json:"download_url,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// Export applies GET /me/export: returns the current export, creating one
// when there is none, the last one expired or failed, or a pending one
// went stale.
func (s *Service) Export(ctx context.Context, p session.Principal) (ExportStatus, error) {
	u, t, err := s.me(ctx, p)
	if err != nil {
		return ExportStatus{}, err
	}
	if t.Membership.Role != RoleOwner && t.Membership.Role != RoleAdmin {
		return ExportStatus{}, ErrForbidden
	}
	d, err := s.d.Limiter.Allow(ctx, "export:"+p.UserID.String(), s.d.ExportPolicy)
	if err == nil && !d.Allowed {
		return ExportStatus{}, ErrRateLimited
	}
	now := s.d.Now()
	latest, ok, err := s.d.Store.LatestExport(ctx, p.UserID, p.OrgID)
	if err != nil {
		return ExportStatus{}, err
	}
	if ok {
		switch {
		case latest.Status == ExportReady && latest.ExpiresAt != nil && now.Before(*latest.ExpiresAt):
			return s.status(latest), nil
		case latest.Status == ExportPending && now.Sub(latest.RequestedAt) < StalePendingAfter:
			return s.status(latest), nil
		}
	}
	exp, err := s.d.Store.CreateExport(ctx, u.ID, t.Org.ID)
	if err != nil {
		return ExportStatus{}, err
	}
	if err := s.record(ctx, t.Org.ID, userActor(u.ID), ActionExportRequested, audit.Target{Type: "export", ID: exp.ID.String()}, nil); err != nil {
		return ExportStatus{}, err
	}
	if s.d.Enqueue != nil {
		if err := s.d.Enqueue.EnqueueExport(ctx, exp.ID); err != nil {
			s.d.Logger.Warn("exportação: job não enfileirado; a manutenção periódica cobre", slog.String("err", err.Error()))
		}
	}
	return s.status(exp), nil
}

func (s *Service) status(e Export) ExportStatus {
	st := ExportStatus{Status: e.Status, RequestedAt: e.RequestedAt.UTC()}
	switch e.Status {
	case ExportReady:
		link := s.DownloadURL(e)
		st.DownloadURL = &link
		if e.ExpiresAt != nil {
			t := e.ExpiresAt.UTC()
			st.ExpiresAt = &t
		}
	case ExportFailed:
		st.Status = ExportExpired
	}
	return st
}

// DownloadURL signs the link of a ready export (valid until it expires).
func (s *Service) DownloadURL(e Export) string {
	exp := s.d.Now().Add(s.d.ExportTTL)
	if e.ExpiresAt != nil {
		exp = *e.ExpiresAt
	}
	token := crypto.SignToken(s.d.Keys.Key(crypto.PurposeSignedURL), e.ID[:], exp)
	base := ""
	if s.d.APIBaseURL != nil {
		base = strings.TrimRight(s.d.APIBaseURL.String(), "/")
	}
	return base + DownloadPath + "?token=" + url.QueryEscape(token)
}

// OpenDownload verifies a signed token and returns the archive.
func (s *Service) OpenDownload(ctx context.Context, token string) ([]byte, Export, error) {
	payload, err := crypto.VerifyToken(s.d.Keys.Key(crypto.PurposeSignedURL), token, s.d.Now())
	if err != nil || len(payload) != 16 {
		return nil, Export{}, ErrNotFound
	}
	var id uuid.UUID
	copy(id[:], payload)
	e, ok, err := s.d.Store.ExportByID(ctx, id)
	if err != nil {
		return nil, Export{}, err
	}
	if !ok {
		return nil, Export{}, ErrNotFound
	}
	if e.Status != ExportReady || e.ExpiresAt == nil || !s.d.Now().Before(*e.ExpiresAt) {
		return nil, Export{}, ErrExportNotReady
	}
	t, member, err := s.d.Store.Tenant(ctx, e.OrgID, e.UserID)
	if err != nil {
		return nil, Export{}, err
	}
	if !member || (t.Membership.Role != RoleOwner && t.Membership.Role != RoleAdmin) {
		return nil, Export{}, ErrNotFound
	}
	archive, ok, err := s.d.Store.ExportArchive(ctx, id)
	if err != nil {
		return nil, Export{}, err
	}
	if !ok {
		return nil, Export{}, ErrExportNotReady
	}
	return archive, e, nil
}

// RunExport builds the archive of a pending export (worker job).
func (s *Service) RunExport(ctx context.Context, exportID uuid.UUID) error {
	e, ok, err := s.d.Store.ExportByID(ctx, exportID)
	if err != nil {
		return err
	}
	if !ok || e.Status != ExportPending {
		return nil // already done or gone: idempotent
	}
	bundle, err := s.d.Store.ExportBundle(ctx, e.UserID, e.OrgID)
	if err != nil {
		return s.failExport(ctx, e, err)
	}
	if bundle.Membership.Role != RoleOwner && bundle.Membership.Role != RoleAdmin {
		return s.failExport(ctx, e, ErrForbidden)
	}
	now := s.d.Now()
	archive, err := BuildArchive(bundle, now)
	if err != nil {
		return s.failExport(ctx, e, err)
	}
	expires := now.Add(s.d.ExportTTL).UTC()
	if err := s.d.Store.CompleteExport(ctx, e.ID, archive, expires); err != nil {
		return err
	}
	e.Status, e.ExpiresAt = ExportReady, &expires
	orgID := e.OrgID
	if err := s.d.Store.EnqueueEmail(ctx, &orgID, email.ExportReadyMessage(bundle.User.Email, s.DownloadURL(e), expires)); err != nil {
		return err
	}
	return s.record(ctx, e.OrgID, userActor(e.UserID), ActionExportReady, audit.Target{Type: "export", ID: e.ID.String()},
		map[string]any{"size_bytes": len(archive), "expires_at": expires})
}

func (s *Service) failExport(ctx context.Context, e Export, cause error) error {
	s.d.Logger.Error("exportação falhou", slog.String("export_id", e.ID.String()), slog.String("err", cause.Error()))
	if err := s.d.Store.FailExport(ctx, e.ID, cause.Error()); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// RunErase performs a due erasure (worker job). It is safe to run twice.
func (s *Service) RunErase(ctx context.Context, userID uuid.UUID) error {
	rep, err := s.d.Store.Erase(ctx, userID, s.d.Now())
	if err != nil {
		return err
	}
	if !rep.Erased {
		return nil
	}
	s.d.Logger.Info("conta apagada", slog.String("user_id", userID.String()), slog.Int("orgs", len(rep.OrgsDeleted)))
	return s.record(ctx, uuid.Nil, audit.Actor{Type: audit.ActorSystem, ID: "worker"}, ActionErased, audit.Target{Type: "user", ID: userID.String()},
		map[string]any{"orgs_deleted": rep.OrgsDeleted, "memberships_removed": rep.MembershipsRemoved})
}

// RunMaintenance expires old exports and erases due accounts (periodic job).
func (s *Service) RunMaintenance(ctx context.Context) error {
	now := s.d.Now()
	expired, err := s.d.Store.ExpireExports(ctx, now)
	if err != nil {
		return err
	}
	due, err := s.d.Store.DueErasures(ctx, now, erasureBatch)
	if err != nil {
		return err
	}
	var failed []error
	for _, id := range due {
		if err := s.RunErase(ctx, id); err != nil {
			failed = append(failed, fmt.Errorf("usuário %s: %w", id, err))
		}
	}
	s.d.Logger.Info("manutenção de contas", slog.Int64("exports_expired", expired), slog.Int("erasures", len(due)), slog.Int("failed", len(failed)))
	return errors.Join(failed...)
}
