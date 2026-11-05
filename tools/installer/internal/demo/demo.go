// Package demo reconciles the public Vantara twin through the API with the same calls the site
// makes (apps/web/src/lib/session.ts and pipeline.ts): the local sign-in (POST
// /v1/auth/dev/login), one presigned upload per file, the scan verdict, the estimate, the run and
// its findings. It is the installer's payoff and `docmatch demo`.
package demo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/assets"
)

// Client talks to one API as the site at Site would.
type Client struct {
	API  string // API origin, e.g. http://localhost:8080
	Site string // site origin, sent as Origin like the browser does

	http      *http.Client // carries the session cookies
	presigned *http.Client // presigned uploads: authorized by their signature, never by cookies
	// PollEvery is the interval of the scan and run polls.
	PollEvery time.Duration
	// RunTimeout bounds the wait for the run.
	RunTimeout time.Duration
}

// New makes a client for api (and the site origin site).
func New(api, site string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		API:        strings.TrimRight(api, "/"),
		Site:       strings.TrimRight(site, "/"),
		http:       &http.Client{Jar: jar, Timeout: 60 * time.Second},
		presigned:  &http.Client{Timeout: 120 * time.Second},
		PollEvery:  time.Second,
		RunTimeout: 10 * time.Minute,
	}
}

// APIError is the API's error envelope.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code)
	}
	return fmt.Sprintf("HTTP %d %s", e.Status, e.Code)
}

func newKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Client) send(ctx context.Context, method, path string, body, out any, idempotent bool) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, reader) //nolint:gosec // the local API the user installed
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Site != "" {
		req.Header.Set("Origin", c.Site)
	}
	if idempotent {
		req.Header.Set("Idempotency-Key", newKey())
	}
	res, err := c.http.Do(req) //nolint:gosec // the local API the user installed
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		apiErr := &APIError{Status: res.StatusCode}
		_ = json.Unmarshal(data, apiErr)
		return apiErr
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// call is send with the site's silent refresh: an expired access window is renewed once.
func (c *Client) call(ctx context.Context, method, path string, body, out any, idempotent bool) error {
	err := c.send(ctx, method, path, body, out, idempotent)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized && apiErr.Code == "session_expired" {
		if rerr := c.send(ctx, http.MethodPost, "/v1/auth/refresh", nil, nil, false); rerr != nil {
			return err
		}
		return c.send(ctx, method, path, body, out, idempotent)
	}
	return err
}

// Me is the signed-in account.
type Me struct {
	User struct {
		Email string `json:"email"`
	} `json:"user"`
}

// Version is GET /v1/version.
type Version struct {
	Version string `json:"version"`
	GitSHA  string `json:"git_sha"`
}

// Version asks the API who it is.
func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	err := c.send(ctx, http.MethodGet, "/v1/version", nil, &v, false)
	return v, err
}

// DevLogin signs in with the local sign-in (development API only).
func (c *Client) DevLogin(ctx context.Context) (Me, error) {
	var me Me
	err := c.send(ctx, http.MethodPost, "/v1/auth/dev/login", map[string]any{}, &me, false)
	return me, err
}

type ticket struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Fields  map[string]string `json:"fields"`
	Headers map[string]string `json:"headers"`
}

// Upload is the API's view of one file.
type Upload struct {
	ID             string `json:"id"`
	FileName       string `json:"file_name"`
	Status         string `json:"status"`
	RejectedReason string `json:"rejected_reason"`
}

// ErrRejected is a file the scanner refused.
var ErrRejected = errors.New("arquivo recusado")

// UploadFile sends one document and waits for the scan verdict.
func (c *Client) UploadFile(ctx context.Context, name, mime string, data []byte) (Upload, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	var t ticket
	err := c.call(ctx, http.MethodPost, "/v1/uploads", map[string]any{
		"kind": "document", "file_name": name, "size_bytes": len(data), "mime": mime, "sha256": digest,
	}, &t, false)
	if err != nil {
		return Upload{}, err
	}
	if err := c.putObject(ctx, t, name, mime, data); err != nil {
		return Upload{}, err
	}
	path := "/v1/uploads/" + url.PathEscape(t.ID) + "/complete"
	var up Upload
	if err := c.call(ctx, http.MethodPost, path, map[string]any{"sha256": digest}, &up, false); err != nil {
		return Upload{}, err
	}
	for i := 0; (up.Status == "pending" || up.Status == "scanning") && i < 180; i++ {
		if err := sleep(ctx, c.PollEvery); err != nil {
			return Upload{}, err
		}
		if err := c.call(ctx, http.MethodPost, path, map[string]any{}, &up, false); err != nil {
			return Upload{}, err
		}
	}
	if up.Status != "ready" {
		reason := up.RejectedReason
		if reason == "" {
			reason = up.Status
		}
		return up, fmt.Errorf("%s: %w (%s)", name, ErrRejected, reason)
	}
	return up, nil
}

func (c *Client) putObject(ctx context.Context, t ticket, name, mime string, data []byte) error {
	var req *http.Request
	var err error
	if strings.EqualFold(t.Method, http.MethodPost) {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		keys := make([]string, 0, len(t.Fields))
		for k := range t.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := form.WriteField(k, t.Fields[k]); err != nil {
				return err
			}
		}
		part, err := form.CreateFormFile("file", name) // last: S3 ignores fields after the file
		if err != nil {
			return err
		}
		if _, err := part.Write(data); err != nil {
			return err
		}
		if err := form.Close(); err != nil {
			return err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, t.URL, &body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", form.FormDataContentType())
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPut, t.URL, bytes.NewReader(data))
		if err != nil {
			return err
		}
		for k, v := range t.Headers {
			req.Header.Set(k, v)
		}
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", mime)
		}
	}
	res, err := c.presigned.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode >= 300 {
		return &APIError{Status: res.StatusCode, Code: "upload_failed"}
	}
	return nil
}

// Quote is a signed estimate.
type Quote struct {
	ID               string `json:"id"`
	ExpectedCredits  int    `json:"expected_credits"`
	CapCredits       int    `json:"cap_credits"`
	Signature        string `json:"signature"`
	RequiresApproval bool   `json:"requires_approval"`
}

// Summary is the run's result summary.
type Summary struct {
	FindingsTotal int    `json:"findings_total"`
	AmountAtRisk  int64  `json:"amount_at_risk"` // cents
	Currency      string `json:"currency"`
	LinesTotal    int    `json:"lines_total"`
	LinesMatched  int    `json:"lines_matched"`
}

// Run is the API's view of a run.
type Run struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Progress *struct {
		Stage   string `json:"stage"`
		Percent int    `json:"percent"`
		Message string `json:"message"`
	} `json:"progress"`
	Summary *Summary `json:"summary"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Evidence is one piece of proof of a finding.
type Evidence struct {
	SourceID string `json:"source_id"`
	Label    string `json:"label"`
	Excerpt  string `json:"excerpt"`
	Locator  *struct {
		Type string `json:"type"`
		Row  int    `json:"row"`
	} `json:"locator"`
}

// Finding is one divergence with its evidence.
type Finding struct {
	ID           string     `json:"id"`
	Code         string     `json:"code"`
	Title        string     `json:"title"`
	AmountAtRisk int64      `json:"amount_at_risk"` // cents
	Currency     string     `json:"currency"`
	Evidence     []Evidence `json:"evidence"`
}

func runBody(ids []string) map[string]any {
	sources := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		sources = append(sources, map[string]string{"upload_id": id})
	}
	// The site's defaults: rules on, web search off.
	return map[string]any{"sources": sources, "options": map[string]any{"web_search": false, "use_rules": true}}
}

// Estimate asks for the signed quote.
func (c *Client) Estimate(ctx context.Context, ids []string) (Quote, error) {
	var q Quote
	err := c.call(ctx, http.MethodPost, "/v1/runs/estimate", runBody(ids), &q, false)
	return q, err
}

// CreateRun starts the run of an accepted quote.
func (c *Client) CreateRun(ctx context.Context, q Quote, ids []string) (Run, error) {
	body := runBody(ids)
	body["quote_id"] = q.ID
	body["quote_signature"] = q.Signature
	var r Run
	err := c.call(ctx, http.MethodPost, "/v1/runs", body, &r, true)
	return r, err
}

// GetRun reads a run.
func (c *Client) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	err := c.call(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil, &r, false)
	return r, err
}

// Findings reads every finding of a run (pages of 100).
func (c *Client) Findings(ctx context.Context, id string) ([]Finding, error) {
	var out []Finding
	cursor := ""
	for page := 0; page < 50; page++ {
		q := url.Values{"limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Items      []Finding `json:"items"`
			NextCursor *string   `json:"next_cursor"`
		}
		if err := c.call(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/findings?"+q.Encode(), nil, &res, false); err != nil {
			return nil, err
		}
		out = append(out, res.Items...)
		if res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = *res.NextCursor
	}
	return out, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Progress is what the demo reports while it works.
type Progress struct {
	Phase   string // login | upload | estimate | run | findings
	Done    int
	Total   int
	Message string // the run's own pt-BR stage message, when it has one
	Percent int
}

// Result is the reconciled twin.
type Result struct {
	RunID        string
	Findings     int
	Expected     int
	AmountAtRisk int64 // cents
	Currency     string
	Top          *Finding
	Elapsed      time.Duration
}

// ErrRunFailed is a run that ended without results.
var ErrRunFailed = errors.New("a execução terminou sem resultado")

// Twin uploads the four twin files, estimates, runs and reads the findings.
func (c *Client) Twin(ctx context.Context, report func(Progress)) (*Result, error) {
	if report == nil {
		report = func(Progress) {}
	}
	start := time.Now()
	files, err := assets.Twin()
	if err != nil {
		return nil, err
	}
	report(Progress{Phase: "login"})
	if _, err := c.DevLogin(ctx); err != nil {
		return nil, fmt.Errorf("login local: %w", err)
	}
	ids := make([]string, 0, len(files))
	for i, f := range files {
		report(Progress{Phase: "upload", Done: i, Total: len(files)})
		up, err := c.UploadFile(ctx, f.Name, f.MIME, f.Data)
		if err != nil {
			return nil, fmt.Errorf("envio: %w", err)
		}
		ids = append(ids, up.ID)
	}
	report(Progress{Phase: "estimate", Done: len(files), Total: len(files)})
	q, err := c.Estimate(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("estimativa: %w", err)
	}
	run, err := c.CreateRun(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("execução: %w", err)
	}
	deadline := time.Now().Add(c.RunTimeout)
	for run.Status == "queued" || run.Status == "running" {
		p := Progress{Phase: "run"}
		if run.Progress != nil {
			p.Message, p.Percent = run.Progress.Message, run.Progress.Percent
		}
		report(p)
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("execução %s: %w (tempo esgotado)", run.ID, ErrRunFailed)
		}
		if err := sleep(ctx, c.PollEvery); err != nil {
			return nil, err
		}
		if run, err = c.GetRun(ctx, run.ID); err != nil {
			return nil, fmt.Errorf("execução: %w", err)
		}
	}
	if run.Status != "completed" {
		msg := run.Status
		if run.Error != nil && run.Error.Message != "" {
			msg = run.Error.Message
		}
		return nil, fmt.Errorf("execução %s: %w (%s)", run.ID, ErrRunFailed, msg)
	}
	report(Progress{Phase: "findings", Percent: 100})
	findings, err := c.Findings(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("achados: %w", err)
	}
	res := &Result{RunID: run.ID, Findings: len(findings), Expected: assets.ExpectedFindings(), Elapsed: time.Since(start)}
	if run.Summary != nil {
		res.Findings = run.Summary.FindingsTotal
		res.AmountAtRisk = run.Summary.AmountAtRisk
		res.Currency = run.Summary.Currency
	}
	res.Top = Top(findings)
	if res.AmountAtRisk == 0 {
		for _, f := range findings {
			res.AmountAtRisk += f.AmountAtRisk
		}
	}
	if res.Currency == "" && res.Top != nil {
		res.Currency = res.Top.Currency
	}
	return res, nil
}

// Top is the finding with the most money at risk that carries evidence.
func Top(findings []Finding) *Finding {
	var best *Finding
	for i := range findings {
		f := &findings[i]
		if len(f.Evidence) == 0 {
			continue
		}
		if best == nil || f.AmountAtRisk > best.AmountAtRisk {
			best = f
		}
	}
	return best
}

// EvidenceLine renders the first evidence of f in one line: its label, row and excerpt.
func EvidenceLine(f *Finding) string {
	if f == nil || len(f.Evidence) == 0 {
		return ""
	}
	e := f.Evidence[0]
	var parts []string
	if e.Label != "" {
		parts = append(parts, e.Label)
	}
	if e.Label == "" && e.Locator != nil && e.Locator.Row > 0 {
		parts = append(parts, fmt.Sprintf("linha %d", e.Locator.Row))
	}
	line := strings.Join(parts, ", ")
	excerpt := strings.Join(strings.Fields(e.Excerpt), " ")
	if excerpt != "" {
		if line != "" {
			line += ": "
		}
		line += excerpt
	}
	return line
}

// FormatMoney renders an amount in cents the pt-BR way: 5727000 -> "R$ 57.270,00".
func FormatMoney(cents int64, currency string) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	whole, frac := cents/100, cents%100
	digits := fmt.Sprintf("%d", whole)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	symbol := "R$"
	if currency != "" && currency != "BRL" {
		symbol = currency
	}
	s := fmt.Sprintf("%s %s,%02d", symbol, b.String(), frac)
	if neg {
		s = "-" + s
	}
	return s
}
