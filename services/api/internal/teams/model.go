// Package teams manages accepted seats without changing subscription quantities.
package teams

import (
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
)

const (
	InvitationTTL      = 7 * 24 * time.Hour
	ResendCooldown     = time.Minute
	InvitationsPerHour = 20
)

type Member struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

type Invitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	SentAt    time.Time `json:"sent_at"`
}

type View struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Kind         string                 `json:"kind"`
	Role         string                 `json:"role"`
	Capabilities tenancy.Capabilities   `json:"capabilities"`
	Members      []Member               `json:"members"`
	Invitations  []Invitation           `json:"invitations"`
	MemberLimit  int                    `json:"member_limit"`
	SeatPrice    int                    `json:"seat_price"`
	CanInvite    bool                   `json:"can_invite"`
	Features     config.AccountFeatures `json:"features"`
}

type Preview struct {
	ID           string    `json:"id"`
	Organization string    `json:"organization"`
	EmailHint    string    `json:"email_hint"`
	Role         string    `json:"role"`
	Status       string    `json:"status"`
	ExpiresAt    time.Time `json:"expires_at"`
	EmailMatches bool      `json:"email_matches"`
}
