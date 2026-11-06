package config

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

const VarExtensionClients = "DM_EXTENSION_CLIENTS"

// ExtensionClient is an operator-registered public client, never a client secret.
// Empty configuration disables authorization; redirects must match byte for byte.
type ExtensionClient struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
}

var extensionID = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,63}$`)

func (r *reader) extensionClients() []ExtensionClient {
	raw := r.optional(VarExtensionClients, "[]")
	var clients []ExtensionClient
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&clients); err != nil || len(clients) > 20 || !json.Valid([]byte(raw)) {
		r.problem(VarExtensionClients, "precisa ser uma lista JSON de clientes com id, name e redirect_uris")
		return nil
	}
	seen := map[string]bool{}
	for _, c := range clients {
		valid := extensionID.MatchString(c.ID) && !seen[c.ID] && len(c.Name) > 0 && len(c.Name) <= 80 && len(c.RedirectURIs) > 0 && len(c.RedirectURIs) <= 4
		seen[c.ID] = true
		for _, target := range c.RedirectURIs {
			u, err := url.Parse(target)
			if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(target, "*\\\r\n\t ") {
				valid = false
				continue
			}
			nativeReturn := (u.Scheme == "docmatch-extension" || u.Scheme == "docmatch-extension-dev") && u.Host == "auth" && u.Path == ""
			if u.Scheme != "https" && !nativeReturn {
				valid = false
			}
		}
		if !valid {
			r.problem(VarExtensionClients, "cliente inválido; use IDs únicos e destinos completos HTTPS ou o retorno nativo registrado")
		}
	}
	return clients
}
