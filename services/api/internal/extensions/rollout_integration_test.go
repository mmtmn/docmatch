//go:build integration

package extensions

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
)

func TestExtensionRolloutRetainsReadsAndLogout(t *testing.T) {
	f := setup(t)
	tokens := f.issue(t)
	f.s.rollout = &config.AccountRollout{}
	in := AuthorizeRequest{ClientID: "chrome-local", RedirectURI: "https://extension.example.invalid/", CodeChallenge: challenge(crypto.RandomToken(32)), CodeChallengeMethod: "S256", State: crypto.RandomToken(32)}
	f.call(t, "POST", "/v1/extensions/authorize", in, true, "", 409)
	f.call(t, "GET", "/v1/projects", nil, false, tokens.AccessToken, 200)
	f.call(t, "POST", "/v1/projects", map[string]string{"name": "Pausado"}, false, tokens.AccessToken, 409)
	f.s.rollout = &config.AccountRollout{Accounts: map[uuid.UUID]bool{f.p.OrgID: true}}
	f.call(t, "POST", "/v1/projects", map[string]string{"name": "Liberado"}, false, tokens.AccessToken, 201)
	f.code(t)
	f.s.rollout = &config.AccountRollout{}
	f.call(t, "POST", "/v1/extensions/revoke", nil, false, tokens.AccessToken, 204)
}
