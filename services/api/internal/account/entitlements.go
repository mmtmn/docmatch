package account

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"
)

// Entitlements mirrors the Entitlements schema: the effective limits of a
// plan, read from config/pricing.yaml (DM_PRICING_FILE).
type Entitlements struct {
	DocumentFilesPerRun      int      `json:"document_files_per_run" yaml:"document_files_per_run"`
	DocumentFileMB           int      `json:"document_file_mb" yaml:"document_file_mb"`
	DocumentRunMB            int      `json:"document_run_mb" yaml:"document_run_mb"`
	MediaFilesPerRun         int      `json:"media_files_per_run" yaml:"media_files_per_run"`
	MediaRunGB               int      `json:"media_run_gb" yaml:"media_run_gb"`
	MediaRunHours            int      `json:"media_run_hours" yaml:"media_run_hours"`
	ConcurrentRuns           int      `json:"concurrent_runs" yaml:"concurrent_runs"`
	APIKeys                  int      `json:"api_keys" yaml:"api_keys"`
	APILanes                 []string `json:"api_lanes" yaml:"api_lanes"`
	AutoRuleLearning         bool     `json:"auto_rule_learning" yaml:"auto_rule_learning"`
	UICustomizationsPerMonth int      `json:"ui_customizations_per_month" yaml:"ui_customizations_per_month"`
	EscalationModel          bool     `json:"escalation_model" yaml:"escalation_model"`
	RetentionDays            int      `json:"retention_days" yaml:"retention_days"`
}

// Catalog holds the entitlements of every plan.
type Catalog struct {
	plans map[string]Entitlements
}

type pricingFile struct {
	Plans map[string]Entitlements `yaml:"plans"`
}

var knownLanes = map[string]bool{"app": true, "api_batch": true, "api_standard": true, "api_priority": true}

// ParseEntitlements reads the plans section of a pricing.yaml document.
func ParseEntitlements(data []byte) (*Catalog, error) {
	var f pricingFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("pricing.yaml: %w", err)
	}
	if len(f.Plans) == 0 {
		return nil, errors.New("pricing.yaml: nenhum plano em plans")
	}
	for code, e := range f.Plans {
		if e.DocumentFilesPerRun <= 0 || e.DocumentFileMB <= 0 || e.DocumentRunMB <= 0 || e.ConcurrentRuns <= 0 || e.APIKeys < 0 || e.RetentionDays <= 0 || e.UICustomizationsPerMonth < -1 {
			return nil, fmt.Errorf("pricing.yaml: plano %s com limites inválidos", code)
		}
		if e.APILanes == nil {
			e.APILanes = []string{}
		}
		for _, lane := range e.APILanes {
			if !knownLanes[lane] {
				return nil, fmt.Errorf("pricing.yaml: plano %s com faixa desconhecida %q", code, lane)
			}
		}
		f.Plans[code] = e
	}
	return &Catalog{plans: f.Plans}, nil
}

// LoadEntitlements reads path. A relative path that does not exist in the
// working directory is searched upward (the API runs from services/api while
// DM_PRICING_FILE is relative to the repository root).
func LoadEntitlements(path string) (*Catalog, error) {
	resolved, err := resolve(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved) //nolint:gosec // path comes from the configuration
	if err != nil {
		return nil, fmt.Errorf("pricing.yaml: %w", err)
	}
	return ParseEntitlements(data)
}

func resolve(path string) (string, error) {
	if path == "" {
		return "", errors.New("pricing.yaml: caminho vazio (DM_PRICING_FILE)")
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, path)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("pricing.yaml: %s não encontrado a partir do diretório atual", path)
		}
		dir = parent
	}
}

// For returns the entitlements of a plan code.
func (c *Catalog) For(plan string) (Entitlements, bool) {
	if c == nil {
		return Entitlements{}, false
	}
	e, ok := c.plans[plan]
	return e, ok
}

// Plans lists the plan codes, sorted.
func (c *Catalog) Plans() []string {
	out := make([]string, 0, len(c.plans))
	for code := range c.plans {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
