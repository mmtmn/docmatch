// Package customizations implements versioned, tenant-scoped workspace preferences.
// The planner proposes data. Only the fixed renderer and operations below execute.
package customizations

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Manifest struct {
	Version       int    `json:"version"`
	Mode          string `json:"mode"`
	Density       string `json:"density"`
	FindingsLabel string `json:"findings_label"`
	GroupBy       string `json:"group_by"`
	SortBy        string `json:"sort_by"`
	ExportFormat  string `json:"export_format"`
}

func Default() Manifest {
	return Manifest{1, "system", "comfortable", "Achados", "none", "severity", "xlsx"}
}
func (m Manifest) Validate() error {
	if m.Version != 1 || !slices.Contains([]string{"system", "light", "dark"}, m.Mode) ||
		!slices.Contains([]string{"comfortable", "compact"}, m.Density) ||
		!slices.Contains([]string{"none", "severity", "kind"}, m.GroupBy) ||
		!slices.Contains([]string{"severity", "amount_at_risk", "confidence"}, m.SortBy) ||
		!slices.Contains([]string{"xlsx", "csv", "pdf"}, m.ExportFormat) {
		return errors.New("unsupported workspace setting")
	}
	if utf8.RuneCountInString(m.FindingsLabel) < 1 || utf8.RuneCountInString(m.FindingsLabel) > 40 || strings.ContainsAny(m.FindingsLabel, "<>{}\\") || strings.Contains(m.FindingsLabel, "://") {
		return errors.New("invalid label")
	}
	for _, r := range m.FindingsLabel {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errors.New("invalid label")
		}
	}
	return nil
}
func decodeManifest(data []byte) (Manifest, error) {
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	return m, m.Validate()
}

// Endpoint metadata is derived server-side; never accept a URL, code, SQL or
// an authorization scope from the planner. Each operation rechecks run ownership.
func endpoints() []map[string]string {
	return []map[string]string{
		{"operation": "run.read", "method": "GET", "path": "/v1/runs/{run_id}"},
		{"operation": "findings.list", "method": "GET", "path": "/v1/runs/{run_id}/findings"},
		{"operation": "document.export", "method": "GET", "path": "/v1/runs/{run_id}/export"},
	}
}
