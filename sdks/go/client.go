// Package docmatch is the official API client. No telemetry. Apache-2.0.
package docmatch

import (
	"bytes"
	"context"
	"crypto/hmac"
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
	"strconv"
	"strings"
	"time"
)

type Client struct {
	orgID        string
	BaseURL      string
	Key          string
	Lane         string
	HTTP         *http.Client
	PollInterval time.Duration
}
type Error struct {
	Status    int             `json:"-"`
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	RequestID string          `json:"request_id"`
	Details   json.RawMessage `json:"details,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code) }
func New(baseURL, key string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid API origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname())) {
		return nil, errors.New("HTTPS required outside loopback")
	}
	jar, _ := cookiejar.New(nil)
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Key: key, Lane: "api_batch", HTTP: &http.Client{Jar: jar, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, PollInterval: time.Second}, nil
}
func loopback(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }
func key() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
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
func delay(h string, attempt int) time.Duration {
	if n, e := strconv.Atoi(h); e == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if when, e := http.ParseTime(h); e == nil && time.Until(when) > 0 {
		return time.Until(when)
	}
	b := make([]byte, 1)
	_, _ = rand.Read(b)
	return time.Duration(250*(1<<attempt)+int(b[0])) * time.Millisecond
}

// Do makes a relative API request. Writes receive a single stable idempotency key.
// Only GET and createRun are automatically retried; other writes may not be idempotent server-side.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	data, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	if !strings.HasPrefix(path, "/v1/") || strings.Contains(path, "\\") {
		return nil, errors.New("expected relative /v1/ path")
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	idem := key()
	retry := method == "GET" || (method == "POST" && (path == "/v1/runs" || strings.HasPrefix(path, "/v1/projects/") && strings.HasSuffix(path, "/runs")))
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if c.orgID != "" && !strings.HasPrefix(path, "/v1/auth/") {
			req.Header.Set("X-Docmatch-Org", c.orgID)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.Key != "" {
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		if method != "GET" {
			req.Header.Set("Idempotency-Key", idem)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			if retry && attempt < 3 && ctx.Err() == nil {
				if err = sleep(ctx, delay("", attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
		payload, readErr := io.ReadAll(io.LimitReader(res.Body, 64<<20))
		_ = res.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return payload, nil
		}
		if attempt < 3 && (res.StatusCode == 429 || (retry && res.StatusCode >= 500)) {
			if err = sleep(ctx, delay(res.Header.Get("Retry-After"), attempt)); err != nil {
				return nil, err
			}
			continue
		}
		apiErr := &Error{Status: res.StatusCode}
		_ = json.Unmarshal(payload, apiErr)
		return nil, apiErr
	}
}

// DevLogin is intentionally limited to a local API. Production uses a scoped API key.
func (c *Client) DevLogin(ctx context.Context) error {
	u, _ := url.Parse(c.BaseURL)
	if !loopback(u.Hostname()) {
		return errors.New("development login requires loopback")
	}
	c.Lane = "app"
	return c.Do(ctx, "POST", "/v1/auth/dev/login", map[string]any{}, nil)
}

type Upload struct {
	ID             string `json:"id"`
	FileName       string `json:"file_name"`
	Status         string `json:"status"`
	RejectedReason string `json:"rejected_reason"`
}
type Quote struct {
	ID              string `json:"id"`
	Signature       string `json:"signature"`
	ExpectedCredits int64  `json:"expected_credits"`
	CapCredits      int64  `json:"cap_credits"`
}
type Run struct {
	ProjectID string `json:"project_id,omitempty"`
	Sources   []struct {
		ID       string `json:"id"`
		FileName string `json:"file_name"`
	} `json:"sources"`
	ID       string          `json:"id"`
	Status   string          `json:"status"`
	Summary  json.RawMessage `json:"summary"`
	Progress *struct {
		Stage   string `json:"stage"`
		Percent int    `json:"percent"`
		Message string `json:"message"`
	} `json:"progress"`
	Error *Error `json:"error"`
}
type Finding struct {
	Explanation  string            `json:"explanation"`
	ID           string            `json:"id"`
	Code         string            `json:"code"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title"`
	Severity     string            `json:"severity"`
	AmountAtRisk int64             `json:"amount_at_risk"`
	Evidence     []json.RawMessage `json:"evidence"`
}
type Input struct {
	Sources   []Source `json:"sources"`
	RulesText string   `json:"rules_text,omitempty"`
	Options   Options  `json:"options"`
}
type Source struct {
	UploadID string `json:"upload_id"`
}
type Options struct {
	Lane      string `json:"lane"`
	UseRules  bool   `json:"use_rules"`
	WebSearch bool   `json:"web_search"`
}

func (c *Client) Input(ids ...string) Input {
	in := Input{Sources: []Source{}, Options: Options{Lane: c.Lane, UseRules: true}}
	for _, id := range ids {
		in.Sources = append(in.Sources, Source{id})
	}
	return in
}
func (c *Client) Estimate(ctx context.Context, in Input) (Quote, error) {
	var q Quote
	err := c.Do(ctx, Operations["estimateRun"].Method, Operations["estimateRun"].Path, in, &q)
	return q, err
}
func (c *Client) Run(ctx context.Context, q Quote, in Input) (Run, error) {
	var r Run
	body := struct {
		Input
		QuoteID   string `json:"quote_id"`
		Signature string `json:"quote_signature"`
	}{in, q.ID, q.Signature}
	err := c.Do(ctx, "POST", Operations["createRun"].Path, body, &r)
	return r, err
}
func (c *Client) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	err := c.Do(ctx, "GET", strings.ReplaceAll(Operations["getRun"].Path, "{id}", url.PathEscape(id)), nil, &r)
	return r, err
}
func (c *Client) Wait(ctx context.Context, id string, onProgress func(Run)) (Run, error) {
	return c.wait(ctx, id, onProgress, c.GetRun)
}
func (c *Client) wait(ctx context.Context, id string, onProgress func(Run), get func(context.Context, string) (Run, error)) (Run, error) {
	for {
		r, err := get(ctx, id)
		if err != nil {
			return r, err
		}
		if onProgress != nil {
			onProgress(r)
		}
		switch r.Status {
		case "completed":
			return r, nil
		case "failed", "cancelled", "needs_approval":
			return r, &Error{Code: r.Status, Message: "Execução interrompida ou aguardando nova aprovação."}
		}
		if err := sleep(ctx, c.PollInterval); err != nil {
			return r, err
		}
	}
}
func (c *Client) Findings(ctx context.Context, id string) ([]Finding, error) {
	return c.findings(ctx, strings.ReplaceAll(Operations["listFindings"].Path, "{id}", url.PathEscape(id)))
}
func (c *Client) findings(ctx context.Context, prefix string) ([]Finding, error) {
	all := []Finding{}
	cursor := ""
	for page := 0; page < 1000; page++ {
		var data struct {
			Items []Finding `json:"items"`
			Next  string    `json:"next_cursor"`
		}
		path := prefix + "?limit=100&cursor=" + url.QueryEscape(cursor)
		if err := c.Do(ctx, "GET", path, nil, &data); err != nil {
			return nil, err
		}
		all = append(all, data.Items...)
		if data.Next == "" {
			return all, nil
		}
		if data.Next == cursor {
			return nil, errors.New("repeated pagination cursor")
		}
		cursor = data.Next
	}
	return nil, errors.New("pagination limit exceeded")
}
func (c *Client) Export(ctx context.Context, id, format string) ([]byte, error) {
	if format != "xlsx" && format != "csv" && format != "pdf" {
		return nil, errors.New("invalid export format")
	}
	return c.request(ctx, "GET", strings.ReplaceAll(Operations["exportRun"].Path, "{id}", url.PathEscape(id))+"?format="+format, nil)
}
func (c *Client) Upload(ctx context.Context, name, mime string, data []byte) (Upload, error) {
	return c.upload(ctx, Operations["createUpload"].Path, name, mime, data, "", "")
}
func (c *Client) upload(ctx context.Context, prefix, name, mime string, data []byte, role, replaces string) (Upload, error) {
	var out Upload
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	var ticket struct {
		ID      string            `json:"id"`
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Fields  map[string]string `json:"fields"`
		Headers map[string]string `json:"headers"`
	}
	bodyFields := map[string]any{"kind": "document", "file_name": name, "mime": mime, "size_bytes": len(data), "sha256": digest}
	if role != "" {
		bodyFields["role"] = role
	}
	if replaces != "" {
		bodyFields["replaces_id"] = replaces
	}
	if err := c.Do(ctx, "POST", prefix, bodyFields, &ticket); err != nil {
		return out, err
	}
	u, err := url.Parse(ticket.URL)
	if err != nil || u.User != nil || !(u.Scheme == "https" || (u.Scheme == "http" && loopback(u.Hostname()))) {
		return out, errors.New("unsafe upload URL")
	}
	var body bytes.Buffer
	headers := ticket.Headers
	switch ticket.Method {
	case "POST":
		mw := multipart.NewWriter(&body)
		for k, v := range ticket.Fields {
			if err := mw.WriteField(k, v); err != nil {
				return out, err
			}
		}
		part, err := mw.CreateFormFile("file", name)
		if err != nil {
			return out, err
		}
		_, _ = part.Write(data)
		_ = mw.Close()
		headers = map[string]string{"Content-Type": mw.FormDataContentType()}
	case "PUT":
		body.Write(data)
	default:
		return out, errors.New("unsupported upload method")
	}
	req, err := http.NewRequestWithContext(ctx, ticket.Method, ticket.URL, &body)
	if err != nil {
		return out, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// A distinct client has no API headers or session cookies, even for same-origin storage.
	uploadHTTP := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := uploadHTTP.Do(req)
	if err != nil {
		return out, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	_ = res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return out, &Error{Status: res.StatusCode, Code: "upload_failed"}
	}
	path := prefix + "/" + url.PathEscape(ticket.ID) + "/complete"
	for polls := 0; polls < 120; polls++ {
		if err := c.Do(ctx, "POST", path, map[string]string{"sha256": digest}, &out); err != nil {
			return out, err
		}
		if out.Status == "ready" {
			return out, nil
		}
		if out.Status != "pending" && out.Status != "scanning" {
			return out, &Error{Code: "upload_rejected", Message: out.RejectedReason}
		}
		if err := sleep(ctx, c.PollInterval); err != nil {
			return out, err
		}
	}
	return out, errors.New("upload scan timeout")
}

// VerifyWebhook verifies the original bytes and a bounded timestamp before parsing JSON.
// Outbound delivery is not enabled by this helper; the endpoint must also deduplicate event IDs.
func VerifyWebhook(body []byte, header, secret string, now time.Time) bool {
	if secret == "" {
		return false
	}
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		if k == "t" {
			if timestamp != "" {
				return false
			}
			timestamp = v
		}
		if k == "v1" {
			signatures = append(signatures, v)
		}
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || unix < now.Unix()-300 || unix > now.Unix()+300 {
		return false
	}
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(timestamp + "."))
	_, _ = h.Write(body)
	for _, sig := range signatures {
		b, err := hex.DecodeString(sig)
		if err == nil && hmac.Equal(b, h.Sum(nil)) {
			return true
		}
	}
	return false
}
