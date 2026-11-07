// Package projects persists named work, document versions and access assignments.
package projects

import (
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
)

const Prefix = "prj"

type Settings struct {
	RulesText string   `json:"rules_text,omitempty"`
	UseRules  bool     `json:"use_rules"`
	RuleIDs   []string `json:"rule_ids,omitempty"`
	LLMMode   string   `json:"llm_mode,omitempty"`
	WebSearch bool     `json:"web_search"`
}

type Project struct {
	ImportedRunID string               `json:"imported_run_id,omitempty"`
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Visibility    string               `json:"visibility"`
	Version       int64                `json:"version"`
	Revision      int64                `json:"revision"`
	Settings      *Settings            `json:"settings,omitempty"`
	DocumentCount int                  `json:"document_count"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	ArchivedAt    *time.Time           `json:"archived_at"`
	Capabilities  tenancy.Capabilities `json:"capabilities"`
	OwnerID       *uuid.UUID           `json:"-"`
}

type Document struct {
	PendingReplacementID *string    `json:"pending_replacement_id,omitempty"`
	ID                   string     `json:"id"`
	UploadID             string     `json:"upload_id"`
	ReplacesID           *string    `json:"replaces_id,omitempty"`
	FileName             string     `json:"file_name"`
	Role                 string     `json:"role"`
	Status               string     `json:"status"`
	SizeBytes            int64      `json:"size_bytes"`
	CreatedAt            time.Time  `json:"created_at"`
	ExpiresAt            *time.Time `json:"expires_at"`
	RemovedAt            *time.Time `json:"removed_at"`
}

type Detail struct {
	Project
	Documents  []Document `json:"documents"`
	SharedWith []string   `json:"shared_with,omitempty"`
}

type Patch struct {
	Version  int64     `json:"version"`
	Name     *string   `json:"name,omitempty"`
	Archived *bool     `json:"archived,omitempty"`
	Settings *Settings `json:"settings,omitempty"`
}

type Event struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"created_at"`
}
