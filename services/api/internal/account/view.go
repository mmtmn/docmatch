package account

import (
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/publicid"
)

// UserView mirrors the User schema.
type UserView struct {
	ID             string    `json:"id"`
	Email          string    `json:"email"`
	DisplayName    string    `json:"display_name"`
	Role           string    `json:"role"`
	MembershipRole string    `json:"membership_role"`
	Locale         string    `json:"locale"`
	CreatedAt      time.Time `json:"created_at"`
}

// OrgView mirrors the Org schema.
type OrgView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Plan       *string   `json:"plan"`
	PlanStatus string    `json:"plan_status"`
	CreatedAt  time.Time `json:"created_at"`
}

// SessionView mirrors the Session schema returned by the login endpoints.
type SessionView struct {
	User      UserView  `json:"user"`
	Org       OrgView   `json:"org"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MeView mirrors the Me schema.
type MeView struct {
	User          UserView      `json:"user"`
	Org           OrgView       `json:"org"`
	Credits       CreditSummary `json:"credits"`
	Entitlements  Entitlements  `json:"entitlements"`
	Notifications ResolvedPrefs `json:"notifications"`
}

// NewUserView renders u with the role of its membership (owner or member in
// the contract; admin counts as owner, viewer as member).
func NewUserView(u User, role string) UserView {
	r := "member"
	if role == RoleOwner || role == RoleAdmin {
		r = "owner"
	}
	return UserView{ID: publicid.Encode(publicid.PrefixUser, u.ID), Email: u.Email, DisplayName: u.DisplayName, Role: r, MembershipRole: role, Locale: Locale, CreatedAt: u.CreatedAt.UTC()}
}

// NewOrgView renders o with the plan of its active subscription (nil plan
// and status "none" without one).
func NewOrgView(o Org, sub *Subscription) OrgView {
	v := OrgView{ID: publicid.Encode(publicid.PrefixOrg, o.ID), Name: o.Name, PlanStatus: "none", CreatedAt: o.CreatedAt.UTC()}
	if sub == nil {
		return v
	}
	plan := sub.PlanCode
	if plan == "" {
		plan = o.PlanCode
	}
	v.Plan = &plan
	switch sub.Status {
	case "trialing", "active", "past_due":
		v.PlanStatus = sub.Status
	case "canceled", "cancelled":
		v.PlanStatus = "cancelled"
	default:
		v.PlanStatus = "none"
		v.Plan = nil
	}
	return v
}

// NewSessionView renders the login response.
func NewSessionView(u User, t Tenant, expiresAt time.Time) SessionView {
	return SessionView{User: NewUserView(u, t.Membership.Role), Org: NewOrgView(t.Org, t.Subscription), ExpiresAt: expiresAt.UTC()}
}
