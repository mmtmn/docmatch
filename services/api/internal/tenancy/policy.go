// Package tenancy enforces the selected account and live membership permissions.
package tenancy

import (
	"net/http"
	"strings"
)

const Header = "X-Docmatch-Org"

type Capabilities struct {
	ManageAccount bool `json:"manage_account"`
	CreateProject bool `json:"create_project"`
	Reconcile     bool `json:"reconcile"`
	ReadResults   bool `json:"read_results"`
	Upload        bool `json:"upload"`
}

func Permissions(role string) Capabilities {
	switch role {
	case "owner", "admin":
		return Capabilities{true, true, true, true, true}
	case "member", "reconciler":
		return Capabilities{false, true, true, true, true}
	case "viewer":
		return Capabilities{ReadResults: true}
	case "uploader":
		return Capabilities{Upload: true}
	default:
		return Capabilities{}
	}
}

// LegacyAllowed preserves existing member workflows. New invited roles must use
// project-aware endpoints, where assignment is checked for each resource. Merely
// guessing an old run/upload URL must never bypass a project's permissions.
func LegacyAllowed(role, method, path string) bool {
	if Permissions(role).ManageAccount {
		return true
	}
	switch path {
	case "/v1/me", "/v1/me/password", "/v1/me/devices", "/v1/workspace":
		return true // handlers constrain fields and personal operations
	}
	for _, group := range []string{"/v1/uploads", "/v1/runs", "/v1/rules", "/v1/workflows"} {
		if path == group || strings.HasPrefix(path, group+"/") {
			return role == "member" || role == "viewer" && (method == http.MethodGet || method == http.MethodHead)
		}
	}
	return false
}
