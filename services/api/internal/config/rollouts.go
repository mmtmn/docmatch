package config

import (
	"strings"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/publicid"
)

const (
	VarRolloutProjects    = "DM_ROLLOUT_PROJECTS"
	VarRolloutInvitations = "DM_ROLLOUT_INVITATIONS"
	VarRolloutExtensions  = "DM_ROLLOUT_EXTENSIONS"
)

// AccountRollout is immutable process configuration. A nil value preserves the
// behavior of explicitly constructed development/test Config values; LoadFrom
// always supplies a policy, disabled by default outside development.
type AccountRollout struct {
	All      bool
	Accounts map[uuid.UUID]bool
}

func (p *AccountRollout) Allows(org uuid.UUID) bool {
	return p == nil || p.All || p.Accounts[org]
}

type Rollouts struct {
	Projects    *AccountRollout
	Invitations *AccountRollout
	Extensions  *AccountRollout
}

type AccountFeatures struct {
	Projects    bool `json:"projects"`
	Invitations bool `json:"invitations"`
	Extensions  bool `json:"extensions"`
}

func (p Rollouts) For(org uuid.UUID) AccountFeatures {
	return AccountFeatures{p.Projects.Allows(org), p.Invitations.Allows(org), p.Extensions.Allows(org)}
}

func (r *reader) accountRollout(name string, dev bool) *AccountRollout {
	fallback := "none"
	if dev {
		fallback = "*"
	}
	raw := r.optional(name, fallback)
	p := &AccountRollout{Accounts: map[uuid.UUID]bool{}}
	switch raw {
	case "*":
		p.All = true
		return p
	case "none":
		return p
	}
	items := strings.Split(raw, ",")
	if len(items) > 1000 {
		r.problem(name, "aceita no máximo 1000 contas")
		return p
	}
	for _, value := range items {
		id, err := publicid.Decode(publicid.PrefixOrg, strings.TrimSpace(value))
		if err != nil || id == uuid.Nil {
			r.problem(name, "use *, none ou IDs org_ separados por vírgula")
			continue
		}
		p.Accounts[id] = true
	}
	return p
}
