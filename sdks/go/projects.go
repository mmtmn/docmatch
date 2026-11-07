// Apache-2.0. Saved projects use the key's account and preserve analysis approval.
package docmatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}
type Capabilities struct {
	ManageAccount bool `json:"manage_account"`
	CreateProject bool `json:"create_project"`
	Reconcile     bool `json:"reconcile"`
	ReadResults   bool `json:"read_results"`
	Upload        bool `json:"upload"`
}
type Organization struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Kind         string           `json:"kind"`
	Role         string           `json:"role"`
	Plan         string           `json:"plan"`
	Capabilities Capabilities     `json:"capabilities"`
	Features     *AccountFeatures `json:"features,omitempty"`
}
type AccountFeatures struct {
	Projects    bool `json:"projects"`
	Invitations bool `json:"invitations"`
	Extensions  bool `json:"extensions"`
}
type Organizations struct {
	UserID string         `json:"user_id"`
	Items  []Organization `json:"items"`
}
type ProjectSettings struct {
	RulesText string   `json:"rules_text,omitempty"`
	UseRules  bool     `json:"use_rules"`
	WebSearch bool     `json:"web_search"`
	RuleIDs   []string `json:"rule_ids,omitempty"`
	LLMMode   string   `json:"llm_mode,omitempty"`
}
type Project struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Visibility    string            `json:"visibility"`
	Version       int64             `json:"version"`
	Revision      int64             `json:"revision"`
	DocumentCount int               `json:"document_count"`
	Settings      *ProjectSettings  `json:"settings,omitempty"`
	Capabilities  Capabilities      `json:"capabilities"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	ArchivedAt    *string           `json:"archived_at"`
	Documents     []ProjectDocument `json:"documents,omitempty"`
	SharedWith    []string          `json:"shared_with,omitempty"`
}
type ProjectDocument struct {
	ID                   string  `json:"id"`
	UploadID             string  `json:"upload_id"`
	FileName             string  `json:"file_name"`
	Role                 string  `json:"role"`
	Status               string  `json:"status"`
	ReplacesID           string  `json:"replaces_id,omitempty"`
	PendingReplacementID string  `json:"pending_replacement_id,omitempty"`
	SizeBytes            int64   `json:"size_bytes"`
	CreatedAt            string  `json:"created_at"`
	ExpiresAt            *string `json:"expires_at"`
	RemovedAt            *string `json:"removed_at"`
}
type ProjectPatch struct {
	Name     *string          `json:"name,omitempty"`
	Archived *bool            `json:"archived,omitempty"`
	Settings *ProjectSettings `json:"settings,omitempty"`
}
type ProjectAnalysis struct {
	State    string `json:"state"`
	Revision int64  `json:"revision"`
	Quote    *Quote `json:"quote,omitempty"`
	Run      *Run   `json:"run,omitempty"`
}
type ProjectRule struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Version    int             `json:"version"`
	Status     string          `json:"status"`
	Provenance json.RawMessage `json:"provenance"`
}
type ProjectEvent struct {
	ID        int64  `json:"id"`
	Action    string `json:"action"`
	CreatedAt string `json:"created_at"`
}

// WithAccount returns a copy for a cookie-authenticated session. API keys remain
// bound to their original account; the server rejects any attempt to redirect one.
func (c *Client) WithAccount(orgID string) *Client { next := *c; next.orgID = orgID; return &next }
func (c *Client) Organizations(ctx context.Context) (Organizations, error) {
	next := *c
	next.orgID = ""
	var out Organizations
	err := next.Do(ctx, "GET", Operations["listOrganizations"].Path, nil, &out)
	return out, err
}
func (c *Client) ListProjects(ctx context.Context, archived bool, cursor string) (Page[Project], error) {
	var out Page[Project]
	err := c.Do(ctx, "GET", Operations["listProjects"].Path+"?archived="+strconv.FormatBool(archived)+"&cursor="+url.QueryEscape(cursor), nil, &out)
	return out, err
}
func (c *Client) CreateProject(ctx context.Context, name string) (Project, error) {
	var out Project
	err := c.Do(ctx, "POST", Operations["createProject"].Path, map[string]string{"name": name}, &out)
	return out, err
}

// Project binds all subsequent upload/result calls to one project. Keep the
// returned value during an operation instead of changing another client's state.
func (c *Client) Project(id string) *ProjectClient { return &ProjectClient{client: c, ID: id} }

type ProjectClient struct {
	client *Client
	ID     string
}

func (p *ProjectClient) path(operation string, pairs ...string) string {
	path := strings.ReplaceAll(Operations[operation].Path, "{project}", url.PathEscape(p.ID))
	for i := 0; i+1 < len(pairs); i += 2 {
		path = strings.ReplaceAll(path, "{"+pairs[i]+"}", url.PathEscape(pairs[i+1]))
	}
	return path
}
func (p *ProjectClient) Get(ctx context.Context) (Project, error) {
	var out Project
	err := p.client.Do(ctx, "GET", p.path("getProject"), nil, &out)
	return out, err
}
func (p *ProjectClient) Update(ctx context.Context, version int64, patch ProjectPatch) (Project, error) {
	var out Project
	body := struct {
		ProjectPatch
		Version int64 `json:"version"`
	}{patch, version}
	err := p.client.Do(ctx, "PATCH", p.path("updateProject"), body, &out)
	return out, err
}
func (p *ProjectClient) Delete(ctx context.Context, version int64) error {
	return p.client.Do(ctx, "DELETE", p.path("deleteProject")+"?version="+strconv.FormatInt(version, 10), nil, nil)
}
func (p *ProjectClient) Share(ctx context.Context, version int64, visibility string, users []string) (Project, error) {
	if users == nil {
		users = []string{}
	}
	var out Project
	err := p.client.Do(ctx, "PUT", p.path("shareProject"), map[string]any{"version": version, "visibility": visibility, "user_ids": users}, &out)
	return out, err
}
func (p *ProjectClient) Upload(ctx context.Context, name, mime string, data []byte, role string) (Upload, error) {
	return p.client.upload(ctx, p.path("createProjectUpload"), name, mime, data, role, "")
}

// UploadReplacement verifies the new file but keeps the previous input active.
// Call ReplaceDocument with the refreshed version and new document ID to apply it.
func (p *ProjectClient) UploadReplacement(ctx context.Context, document, name, mime string, data []byte) (Upload, error) {
	return p.client.upload(ctx, p.path("createProjectUpload"), name, mime, data, "", document)
}
func (p *ProjectClient) AttachDocument(ctx context.Context, upload, role string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := p.client.Do(ctx, "POST", p.path("attachProjectDocument"), map[string]string{"upload_id": upload, "role": role}, &out)
	return out.ID, err
}
func (p *ProjectClient) RemoveDocument(ctx context.Context, document string, version int64) error {
	return p.client.Do(ctx, "DELETE", p.path("removeProjectDocument", "document", document)+"?version="+strconv.FormatInt(version, 10), nil, nil)
}
func (p *ProjectClient) ReplaceDocument(ctx context.Context, previous, replacement string, version int64) (Project, error) {
	var out Project
	err := p.client.Do(ctx, "POST", p.path("replaceProjectDocument", "document", previous), map[string]any{"version": version, "replacement_id": replacement}, &out)
	return out, err
}
func (p *ProjectClient) Estimate(ctx context.Context, revision int64, reason string) (ProjectAnalysis, error) {
	var out ProjectAnalysis
	err := p.client.Do(ctx, "POST", p.path("estimateProject"), map[string]any{"revision": revision, "lane": p.client.Lane, "reprocess_reason": reason}, &out)
	return out, err
}

// Reconcile needs an explicitly approved quote for changed inputs. With an
// existing valid result the server returns that run without a new reservation.
func (p *ProjectClient) Reconcile(ctx context.Context, revision int64, quote *Quote, reason string) (ProjectAnalysis, error) {
	var out ProjectAnalysis
	body := map[string]any{"revision": revision, "lane": p.client.Lane, "reprocess_reason": reason}
	if quote != nil {
		body["quote_id"] = quote.ID
		body["quote_signature"] = quote.Signature
	}
	err := p.client.Do(ctx, "POST", p.path("reconcileProject"), body, &out)
	return out, err
}
func (p *ProjectClient) Runs(ctx context.Context, cursor string) (Page[Run], error) {
	var out Page[Run]
	err := p.client.Do(ctx, "GET", p.path("listProjectRuns")+"?cursor="+url.QueryEscape(cursor), nil, &out)
	return out, err
}
func (p *ProjectClient) GetRun(ctx context.Context, id string) (Run, error) {
	var out Run
	err := p.client.Do(ctx, "GET", p.path("projectGetRun", "id", id), nil, &out)
	return out, err
}
func (p *ProjectClient) Wait(ctx context.Context, id string, onProgress func(Run)) (Run, error) {
	return p.client.wait(ctx, id, onProgress, p.GetRun)
}
func (p *ProjectClient) Findings(ctx context.Context, id string) ([]Finding, error) {
	return p.client.findings(ctx, p.path("projectListFindings", "id", id))
}
func (p *ProjectClient) Export(ctx context.Context, id, format string) ([]byte, error) {
	if format != "xlsx" && format != "csv" && format != "pdf" {
		return nil, fmt.Errorf("invalid export format")
	}
	return p.client.request(ctx, "GET", p.path("projectExportRun", "id", id)+"?format="+format, nil)
}
func (p *ProjectClient) Cancel(ctx context.Context, id string) (Run, error) {
	var out Run
	err := p.client.Do(ctx, "POST", p.path("projectCancelRun", "id", id), map[string]any{}, &out)
	return out, err
}
func (p *ProjectClient) ApproveQuote(ctx context.Context, id string, quote Quote) (Run, error) {
	var out Run
	err := p.client.Do(ctx, "POST", p.path("projectApproveRunQuote", "id", id), map[string]string{"quote_id": quote.ID, "quote_signature": quote.Signature}, &out)
	return out, err
}
func (p *ProjectClient) Rules(ctx context.Context, cursor string) (Page[ProjectRule], error) {
	var out Page[ProjectRule]
	err := p.client.Do(ctx, "GET", p.path("projectListRules")+"?cursor="+url.QueryEscape(cursor), nil, &out)
	return out, err
}
func (p *ProjectClient) CreateRule(ctx context.Context, title, body string) (ProjectRule, error) {
	var out ProjectRule
	err := p.client.Do(ctx, "POST", p.path("projectCreateRule"), map[string]string{"title": title, "body": body}, &out)
	return out, err
}
func (p *ProjectClient) VersionRule(ctx context.Context, id, title, body string) (ProjectRule, error) {
	var out ProjectRule
	err := p.client.Do(ctx, "POST", p.path("projectCreateRuleVersion", "id", id), map[string]string{"title": title, "body": body}, &out)
	return out, err
}
func (p *ProjectClient) PromoteRule(ctx context.Context, id, to string) (ProjectRule, error) {
	var out ProjectRule
	err := p.client.Do(ctx, "POST", p.path("projectPromoteRule", "id", id), map[string]string{"to": to}, &out)
	return out, err
}
func (p *ProjectClient) Activity(ctx context.Context) (Page[ProjectEvent], error) {
	var out Page[ProjectEvent]
	err := p.client.Do(ctx, "GET", p.path("projectActivity"), nil, &out)
	return out, err
}
