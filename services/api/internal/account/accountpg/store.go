// Package accountpg is the PostgreSQL implementation of account.Store and
// auth.Store over the sqlc queries. Users, sessions, e-mail challenges and
// exports belong to the user and have no row-level security; organizations,
// memberships, subscriptions, credits, audit rows and tenant keys do, so
// every query on them runs inside db.WithOrgTx for the organization at hand
// or, for the cross-tenant paths of the worker (erasure) and of the login
// (finding the user's organizations), in system mode.
package accountpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/auth"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
)

// exportAuditLimit bounds the audit rows copied into an export.
const exportAuditLimit = 10000

// Store implements account.Store and auth.Store.
type Store struct {
	pool  *pgxpool.Pool
	q     *sqlc.Queries
	river *emailpg.Client
}

// New wraps the pool; river (insert-only client) may be nil, in which case
// e-mails wait for the periodic sweep.
func New(pool *pgxpool.Pool, river *emailpg.Client) *Store {
	return &Store{pool: pool, q: sqlc.New(pool), river: river}
}

var (
	_ account.Store = (*Store)(nil)
	_ auth.Store    = (*Store)(nil)
)

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// UserFromRow converts a users row.
func UserFromRow(r sqlc.User) account.User {
	u := account.User{
		ID: r.ID, Email: r.Email, DisplayName: strVal(r.DisplayName), GoogleSub: strVal(r.GoogleSub), AppleSub: strVal(r.AppleSub),
		PasswordHash: strVal(r.PasswordHash), EmailVerifiedAt: r.EmailVerifiedAt, LastLoginAt: r.LastLoginAt,
		SuspendedAt: r.SuspendedAt, DeletionRequestedAt: r.DeletionRequestedAt, DeletionScheduledFor: r.DeletionScheduledFor, CreatedAt: r.CreatedAt,
	}
	if len(r.Notifications) > 0 {
		_ = json.Unmarshal(r.Notifications, &u.Notifications)
	}
	return u
}

func orgFromRow(r sqlc.Org) account.Org {
	return account.Org{ID: r.ID, Name: r.Name, Slug: r.Slug, PlanCode: r.PlanCode, Country: strVal(r.Country), CreatedAt: r.CreatedAt}
}

func exportFromRow(id, userID, orgID uuid.UUID, status string, requestedAt time.Time, readyAt, expiresAt *time.Time, size int64, errText *string) account.Export {
	return account.Export{ID: id, UserID: userID, OrgID: orgID, Status: status, RequestedAt: requestedAt, ReadyAt: readyAt, ExpiresAt: expiresAt, SizeBytes: size, Error: strVal(errText)}
}

func challengeFromRow(r sqlc.MagicLink) auth.Challenge {
	return auth.Challenge{
		ID: r.ID, Email: r.Email, TokenHash: r.TokenHash, CodeHash: r.CodeHash, Purpose: r.Purpose, IPPrefix: strVal(r.IpPrefix),
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, ConsumedAt: r.ConsumedAt, Attempts: int(r.Attempts),
	}
}

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// withTx runs fn in a plain transaction (tables without RLS).
func (s *Store) withTx(ctx context.Context, fn func(ctx context.Context, q *sqlc.Queries, tx pgx.Tx) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(ctx, sqlc.New(tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ------------------------------------------------------------------ users

// UserByID implements account.Store and auth.Store.
func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (account.User, bool, error) {
	r, err := s.q.GetUser(ctx, id)
	if noRows(err) {
		return account.User{}, false, nil
	}
	if err != nil {
		return account.User{}, false, err
	}
	return UserFromRow(r), true, nil
}

// UserByEmail implements auth.Store.
func (s *Store) UserByEmail(ctx context.Context, email string) (account.User, bool, error) {
	r, err := s.q.GetUserByEmail(ctx, email)
	if noRows(err) {
		return account.User{}, false, nil
	}
	if err != nil {
		return account.User{}, false, err
	}
	return UserFromRow(r), true, nil
}

// UserByProvider implements auth.Store.
func (s *Store) UserByProvider(ctx context.Context, provider, sub string) (account.User, bool, error) {
	var (
		r   sqlc.User
		err error
	)
	switch provider {
	case auth.ProviderGoogle:
		r, err = s.q.GetUserByGoogleSub(ctx, &sub)
	case auth.ProviderApple:
		r, err = s.q.GetUserByAppleSub(ctx, &sub)
	default:
		return account.User{}, false, nil
	}
	if noRows(err) {
		return account.User{}, false, nil
	}
	if err != nil {
		return account.User{}, false, err
	}
	return UserFromRow(r), true, nil
}

// Signup implements auth.Store: everything in the transaction of the new
// organization, which the RLS policy accepts because app.org_id is its id.
func (s *Store) Signup(ctx context.Context, u account.User, o account.Org, wrappedKey []byte) (account.User, account.Org, error) {
	if o.ID == uuid.Nil {
		return account.User{}, account.Org{}, errors.New("accountpg: org id is required")
	}
	var outU account.User
	var outO account.Org
	err := db.WithOrgTx(ctx, s.pool, o.ID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		ur, err := q.CreateUser(ctx, sqlc.CreateUserParams{
			Email: u.Email, GoogleSub: strPtr(u.GoogleSub), AppleSub: strPtr(u.AppleSub), PasswordHash: strPtr(u.PasswordHash),
			DisplayName: strPtr(u.DisplayName), EmailVerifiedAt: u.EmailVerifiedAt,
		})
		if err != nil {
			return fmt.Errorf("accountpg: criar usuário: %w", err)
		}
		or, err := q.CreateOrg(ctx, sqlc.CreateOrgParams{ID: o.ID, Name: o.Name, Slug: o.Slug, PlanCode: o.PlanCode, Country: strPtr(o.Country)})
		if err != nil {
			return fmt.Errorf("accountpg: criar organização: %w", err)
		}
		if err := q.AddMembership(ctx, sqlc.AddMembershipParams{OrgID: or.ID, UserID: ur.ID, Role: account.RoleOwner}); err != nil {
			return fmt.Errorf("accountpg: criar vínculo: %w", err)
		}
		if _, err := q.AuthInsertTenantKey(ctx, sqlc.AuthInsertTenantKeyParams{OrgID: or.ID, WrappedKey: wrappedKey, KekVersion: 1}); err != nil {
			return fmt.Errorf("accountpg: criar chave do locatário: %w", err)
		}
		outU, outO = UserFromRow(ur), orgFromRow(or)
		return nil
	})
	return outU, outO, err
}

// LinkProvider implements auth.Store.
func (s *Store) LinkProvider(ctx context.Context, userID uuid.UUID, provider, sub string) error {
	return s.withTx(ctx, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		var err error
		switch provider {
		case auth.ProviderGoogle:
			err = q.LinkUserGoogleSub(ctx, sqlc.LinkUserGoogleSubParams{ID: userID, GoogleSub: &sub})
		case auth.ProviderApple:
			err = q.LinkUserAppleSub(ctx, sqlc.LinkUserAppleSubParams{ID: userID, AppleSub: &sub})
		default:
			return errors.New("accountpg: unknown provider")
		}
		if err != nil {
			return err
		}
		return q.SetUserEmailVerified(ctx, userID)
	})
}

// MarkEmailVerified implements auth.Store.
func (s *Store) MarkEmailVerified(ctx context.Context, userID uuid.UUID) error {
	return s.q.SetUserEmailVerified(ctx, userID)
}

// SetDisplayName implements auth.Store.
func (s *Store) SetDisplayName(ctx context.Context, userID uuid.UUID, name string) error {
	return s.q.SetUserDisplayName(ctx, sqlc.SetUserDisplayNameParams{ID: userID, DisplayName: strPtr(name)})
}

// TouchLogin implements auth.Store.
func (s *Store) TouchLogin(ctx context.Context, userID uuid.UUID) error {
	return s.q.TouchUserLogin(ctx, userID)
}

// PrimaryTenant implements auth.Store: the oldest membership of the user
// (listed in system mode, then loaded in its own tenant scope).
func (s *Store) PrimaryTenant(ctx context.Context, userID uuid.UUID) (account.Tenant, bool, error) {
	var orgID uuid.UUID
	var found bool
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListUserMemberships(ctx, userID)
		if err != nil {
			return err
		}
		var oldest time.Time
		for _, r := range rows {
			if !found || r.CreatedAt.Before(oldest) {
				orgID, oldest, found = r.OrgID, r.CreatedAt, true
			}
		}
		return nil
	})
	if err != nil || !found {
		return account.Tenant{}, false, err
	}
	return s.Tenant(ctx, orgID, userID)
}

// Tenant implements account.Store and auth.Store.
func (s *Store) Tenant(ctx context.Context, orgID, userID uuid.UUID) (account.Tenant, bool, error) {
	if orgID == uuid.Nil {
		return account.Tenant{}, false, nil
	}
	var t account.Tenant
	found := false
	err := db.WithOrgTx(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		o, err := q.GetOrg(ctx, orgID)
		if noRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		m, err := q.GetMembership(ctx, sqlc.GetMembershipParams{OrgID: orgID, UserID: userID})
		if noRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		t = account.Tenant{Org: orgFromRow(o), Membership: account.Membership{OrgID: m.OrgID, UserID: m.UserID, Role: m.Role, CreatedAt: m.CreatedAt}}
		sub, err := q.GetActiveSubscription(ctx, orgID)
		if err != nil && !noRows(err) {
			return err
		}
		if err == nil {
			t.Subscription = &account.Subscription{PlanCode: sub.PlanCode, Status: sub.Status, CurrentPeriodEnd: sub.CurrentPeriodEnd, StripeSubscription: strVal(sub.StripeSubscriptionID)}
		}
		found = true
		return nil
	})
	return t, found, err
}

// Credits implements account.Store.
func (s *Store) Credits(ctx context.Context, orgID uuid.UUID) (account.CreditSummary, error) {
	var out account.CreditSummary
	err := db.WithOrgTx(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		acc, err := q.GetCreditAccount(ctx, orgID)
		if err != nil && !noRows(err) {
			return err
		}
		if err == nil {
			out.Balance, out.Reserved = acc.Balance, acc.Held
		}
		out.Available = out.Balance - out.Reserved
		rows, err := q.AuthSumOpenGrantsByKind(ctx, orgID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			// min(expires_at) is NULL when no open grant of the kind expires.
			var next *time.Time
			if t, ok := r.NextExpiry.(time.Time); ok {
				next = &t
			}
			switch r.Kind {
			case "plan":
				out.PlanCredits += r.Remaining
				out.PlanCycleEndsAt = next
			case "topup":
				out.TopupCredits += r.Remaining
			}
			if next != nil && (out.NextExpiryAt == nil || next.Before(*out.NextExpiryAt)) {
				out.NextExpiryAt = next
			}
		}
		return nil
	})
	return out, err
}

// UpdateProfile implements account.Store.
func (s *Store) UpdateProfile(ctx context.Context, userID uuid.UUID, p account.ProfilePatch) error {
	return s.withTx(ctx, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		if p.DisplayName != nil {
			if err := q.SetUserDisplayName(ctx, sqlc.SetUserDisplayNameParams{ID: userID, DisplayName: strPtr(*p.DisplayName)}); err != nil {
				return err
			}
		}
		if p.Notifications != nil {
			raw, err := json.Marshal(p.Notifications)
			if err != nil {
				return err
			}
			if err := q.AuthSetUserNotifications(ctx, sqlc.AuthSetUserNotificationsParams{ID: userID, Notifications: raw}); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateOrgName implements account.Store.
func (s *Store) UpdateOrgName(ctx context.Context, orgID uuid.UUID, name string) error {
	return db.WithOrgTx(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return sqlc.New(tx).AuthUpdateOrgName(ctx, sqlc.AuthUpdateOrgNameParams{ID: orgID, Name: name})
	})
}

// SetPassword implements account.Store.
func (s *Store) SetPassword(ctx context.Context, userID uuid.UUID, hash string) error {
	return s.q.AuthSetUserPassword(ctx, sqlc.AuthSetUserPasswordParams{ID: userID, PasswordHash: strPtr(hash)})
}

// RequestDeletion implements account.Store.
func (s *Store) RequestDeletion(ctx context.Context, userID uuid.UUID, scheduledFor time.Time) (bool, error) {
	_, err := s.q.AuthRequestUserDeletion(ctx, sqlc.AuthRequestUserDeletionParams{ID: userID, DeletionScheduledFor: &scheduledFor})
	if noRows(err) {
		return false, nil
	}
	return err == nil, err
}

// DueErasures implements account.Store.
func (s *Store) DueErasures(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	return s.q.AuthListUsersDueForErasure(ctx, sqlc.AuthListUsersDueForErasureParams{DeletionScheduledFor: &now, Limit: int32(limit)}) //nolint:gosec // small batch
}

// Erase implements account.Store (system mode): destroy the tenant keys of
// the organizations the user solely owns, delete them, drop the other
// memberships, revoke the sessions and delete the user.
func (s *Store) Erase(ctx context.Context, userID uuid.UUID, now time.Time) (account.EraseReport, error) {
	var rep account.EraseReport
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		u, err := q.GetUser(ctx, userID)
		if noRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.DeletionScheduledFor == nil || u.DeletionScheduledFor.After(now) {
			return nil
		}
		memberships, err := q.ListUserMemberships(ctx, userID)
		if err != nil {
			return err
		}
		for _, m := range memberships {
			members, err := q.AuthCountOrgMembers(ctx, m.OrgID)
			if err != nil {
				return err
			}
			if members == 1 && m.Role == account.RoleOwner {
				if _, err := q.AuthDeleteTenantKey(ctx, m.OrgID); err != nil {
					return fmt.Errorf("accountpg: destruir chave: %w", err)
				}
				if _, err := q.AuthHardDeleteOrg(ctx, m.OrgID); err != nil {
					return fmt.Errorf("accountpg: apagar organização: %w", err)
				}
				rep.OrgsDeleted = append(rep.OrgsDeleted, m.OrgID)
				continue
			}
			if _, err := q.RemoveMembership(ctx, sqlc.RemoveMembershipParams{OrgID: m.OrgID, UserID: userID}); err != nil {
				return err
			}
			rep.MembershipsRemoved++
		}
		if _, err := q.AuthRevokeUserSessions(ctx, sqlc.AuthRevokeUserSessionsParams{UserID: userID}); err != nil {
			return err
		}
		if _, err := q.AuthHardDeleteUser(ctx, userID); err != nil {
			return fmt.Errorf("accountpg: apagar usuário: %w", err)
		}
		rep.Erased = true
		return nil
	})
	return rep, err
}

// ---------------------------------------------------------------- exports

// CreateExport implements account.Store.
func (s *Store) CreateExport(ctx context.Context, userID, orgID uuid.UUID) (account.Export, error) {
	r, err := s.q.AuthInsertExport(ctx, sqlc.AuthInsertExportParams{UserID: userID, OrgID: orgID})
	if err != nil {
		return account.Export{}, err
	}
	return exportFromRow(r.ID, r.UserID, r.OrgID, r.Status, r.RequestedAt, r.ReadyAt, r.ExpiresAt, r.SizeBytes, r.Error), nil
}

// LatestExport implements account.Store.
func (s *Store) LatestExport(ctx context.Context, userID uuid.UUID) (account.Export, bool, error) {
	r, err := s.q.AuthLatestExport(ctx, userID)
	if noRows(err) {
		return account.Export{}, false, nil
	}
	if err != nil {
		return account.Export{}, false, err
	}
	return exportFromRow(r.ID, r.UserID, r.OrgID, r.Status, r.RequestedAt, r.ReadyAt, r.ExpiresAt, r.SizeBytes, r.Error), true, nil
}

// ExportByID implements account.Store.
func (s *Store) ExportByID(ctx context.Context, id uuid.UUID) (account.Export, bool, error) {
	r, err := s.q.AuthExportByID(ctx, id)
	if noRows(err) {
		return account.Export{}, false, nil
	}
	if err != nil {
		return account.Export{}, false, err
	}
	return exportFromRow(r.ID, r.UserID, r.OrgID, r.Status, r.RequestedAt, r.ReadyAt, r.ExpiresAt, r.SizeBytes, r.Error), true, nil
}

// ExportArchive implements account.Store.
func (s *Store) ExportArchive(ctx context.Context, id uuid.UUID) ([]byte, bool, error) {
	archive, err := s.q.AuthExportArchive(ctx, id)
	if noRows(err) || (err == nil && archive == nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return archive, true, nil
}

// CompleteExport implements account.Store.
func (s *Store) CompleteExport(ctx context.Context, id uuid.UUID, archive []byte, expiresAt time.Time) error {
	_, err := s.q.AuthMarkExportReady(ctx, sqlc.AuthMarkExportReadyParams{ID: id, ExpiresAt: &expiresAt, Archive: archive, SizeBytes: int64(len(archive))})
	return err
}

// FailExport implements account.Store.
func (s *Store) FailExport(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := s.q.AuthMarkExportFailed(ctx, sqlc.AuthMarkExportFailedParams{ID: id, Error: strPtr(reason)})
	return err
}

// ExpireExports implements account.Store.
func (s *Store) ExpireExports(ctx context.Context, now time.Time) (int64, error) {
	return s.q.AuthExpireExports(ctx, &now)
}

// ExportBundle implements account.Store.
func (s *Store) ExportBundle(ctx context.Context, userID, orgID uuid.UUID) (account.Bundle, error) {
	u, ok, err := s.UserByID(ctx, userID)
	if err != nil {
		return account.Bundle{}, err
	}
	if !ok {
		return account.Bundle{}, account.ErrNotFound
	}
	t, ok, err := s.Tenant(ctx, orgID, userID)
	if err != nil {
		return account.Bundle{}, err
	}
	if !ok {
		return account.Bundle{}, account.ErrNotFound
	}
	credits, err := s.Credits(ctx, orgID)
	if err != nil {
		return account.Bundle{}, err
	}
	b := account.Bundle{User: u, Org: t.Org, Membership: t.Membership, Subscription: t.Subscription, Credits: credits, Sessions: []account.SessionInfo{}, Audit: []account.AuditEntry{}}
	sessions, err := s.q.AuthListUserSessions(ctx, userID)
	if err != nil {
		return account.Bundle{}, err
	}
	for _, r := range sessions {
		b.Sessions = append(b.Sessions, account.SessionInfo{ID: r.ID, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, IPPrefix: strVal(r.IpPrefix), UserAgent: strVal(r.UserAgent)})
	}
	err = db.WithOrgTx(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListAuditByOrg(ctx, sqlc.ListAuditByOrgParams{OrgID: &orgID, Limit: exportAuditLimit})
		if err != nil {
			return err
		}
		for _, r := range rows {
			b.Audit = append(b.Audit, account.AuditEntry{Seq: r.Seq, Action: r.Action, Actor: r.ActorType + ":" + r.ActorID, Target: r.TargetType, TargetID: strVal(r.TargetID), Meta: string(r.Meta), CreatedAt: r.CreatedAt})
		}
		return nil
	})
	return b, err
}

// EnqueueEmail implements account.Store.
func (s *Store) EnqueueEmail(ctx context.Context, orgID *uuid.UUID, msg email.Message) error {
	return s.withTx(ctx, func(ctx context.Context, _ *sqlc.Queries, tx pgx.Tx) error {
		_, err := emailpg.Enqueue(ctx, tx, s.river, orgID, msg)
		return err
	})
}

// ------------------------------------------------------------- challenges

// CreateChallenge implements auth.Store: the challenge and the e-mail in
// one transaction.
func (s *Store) CreateChallenge(ctx context.Context, ch auth.Challenge, msg email.Message) error {
	return s.withTx(ctx, func(ctx context.Context, q *sqlc.Queries, tx pgx.Tx) error {
		if _, err := q.AuthCreateChallenge(ctx, sqlc.AuthCreateChallengeParams{
			ID: ch.ID, Email: ch.Email, TokenHash: ch.TokenHash, CodeHash: ch.CodeHash, Purpose: ch.Purpose, IpPrefix: strPtr(ch.IPPrefix), ExpiresAt: ch.ExpiresAt,
		}); err != nil {
			return err
		}
		_, err := emailpg.Enqueue(ctx, tx, s.river, nil, msg)
		return err
	})
}

// ChallengeByID implements auth.Store.
func (s *Store) ChallengeByID(ctx context.Context, id uuid.UUID) (auth.Challenge, bool, error) {
	r, err := s.q.AuthChallengeByID(ctx, id)
	if noRows(err) {
		return auth.Challenge{}, false, nil
	}
	if err != nil {
		return auth.Challenge{}, false, err
	}
	return challengeFromRow(r), true, nil
}

// ClaimChallengeAttempt implements auth.Store.
func (s *Store) ClaimChallengeAttempt(ctx context.Context, id uuid.UUID, maxAttempts int) (int, bool, error) {
	n, err := s.q.AuthClaimChallengeAttempt(ctx, sqlc.AuthClaimChallengeAttemptParams{ID: id, MaxAttempts: int32(min(maxAttempts, math.MaxInt32))}) //nolint:gosec // bounded above
	if noRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return int(n), true, nil
}

// ConsumeChallenge implements auth.Store.
func (s *Store) ConsumeChallenge(ctx context.Context, id uuid.UUID) (auth.Challenge, bool, error) {
	r, err := s.q.AuthConsumeChallenge(ctx, id)
	if noRows(err) {
		return auth.Challenge{}, false, nil
	}
	if err != nil {
		return auth.Challenge{}, false, err
	}
	return challengeFromRow(r), true, nil
}

// DeleteStaleChallenges removes challenges expired before the cut-off.
func (s *Store) DeleteStaleChallenges(ctx context.Context, before time.Time) (int64, error) {
	return s.q.AuthDeleteStaleChallenges(ctx, before)
}
