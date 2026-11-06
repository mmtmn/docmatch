package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtensionClientsRequireExactRegisteredDestinations(t *testing.T) {
	good := []ExtensionClient{{ID: "chrome-dev", Name: "Chrome local", RedirectURIs: []string{"https://abcdefghijklmnop.chromiumapp.org/", "docmatch-extension-dev://auth"}}}
	raw, _ := json.Marshal(good)
	env := validEnv()
	env[VarExtensionClients] = string(raw)
	if got := mustLoad(t, env).Auth.ExtensionClients; len(got) != 1 || got[0].ID != "chrome-dev" {
		t.Fatal(got)
	}
	for _, raw := range []string{
		`{`, `[] true`, `[{"id":"chrome-dev","name":"Chrome","redirect_uris":["https://example.com/"],"extra":true}]`,
		`[{"id":"bad id","name":"X","redirect_uris":["https://example.com/"]}]`,
		`[{"id":"one","name":"","redirect_uris":[]}]`,
		`[{"id":"one","name":"X","redirect_uris":["http://example.com/"]}]`,
		`[{"id":"one","name":"X","redirect_uris":["https://example.com/*"]}]`,
		`[{"id":"one","name":"X","redirect_uris":["https://example.com/?a=1"]}]`,
		`[{"id":"one","name":"X","redirect_uris":["https://a:password@example.com/"]}]`,
		`[{"id":"one","name":"X","redirect_uris":["https://%/"]}]`,
		`[{"id":"one","name":"X","redirect_uris":["docmatch-extension://other"]}]`,
		`[{"id":"one","name":"X","redirect_uris":["https://example.com/#fragment"]}]`,
		`[{"id":"one","name":"X","redirect_uris":["https://example.com/"]},{"id":"one","name":"Y","redirect_uris":["https://example.com/"]}]`,
		`[` + strings.Repeat(`{"id":"one","name":"X","redirect_uris":["https://example.com/"]},`, 20) + `{}]`,
	} {
		env := validEnv()
		env[VarExtensionClients] = raw
		if _, err := LoadFrom(lookupFrom(env)); err == nil {
			t.Fatalf("accepted invalid registration %s", raw)
		}
	}
}
