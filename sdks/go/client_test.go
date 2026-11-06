package docmatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRetriesKeepIdempotencyAndDoNotDuplicateUnsafeWrites(t *testing.T) {
	attempts := 0
	id := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path == "/v1/uploads" {
			w.WriteHeader(503)
			return
		}
		if id == "" {
			id = r.Header.Get("Idempotency-Key")
		} else if id != r.Header.Get("Idempotency-Key") {
			t.Error("idempotency changed")
		}
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run_fixture","status":"queued"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "fixture")
	_, err := c.Run(context.Background(), Quote{ID: "quote", Signature: "sig"}, c.Input("upl_fixture"))
	if err != nil || attempts != 2 || id == "" {
		t.Fatal(err, attempts)
	}
	attempts = 0
	if err := c.Do(context.Background(), "POST", "/v1/uploads", map[string]any{}, nil); err == nil || attempts != 1 {
		t.Fatal("unsafe write retried")
	}
}
func TestUploadNeverSendsCredentials(t *testing.T) {
	var apiURL string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials leaked")
		}
		if r.Method != "POST" {
			t.Error("wrong method")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		w.WriteHeader(204)
	}))
	defer storage.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/uploads" {
			_, _ = w.Write([]byte(`{"id":"upl_fixture","method":"POST","url":"` + storage.URL + `","fields":{"policy":"fixture"}}`))
		} else {
			_, _ = w.Write([]byte(`{"id":"upl_fixture","status":"ready"}`))
		}
	}))
	defer srv.Close()
	apiURL = srv.URL
	c, _ := New(apiURL, "fixture-secret")
	if _, err := c.Upload(context.Background(), "a.csv", "text/csv", []byte("a,b\n1,2\n")); err != nil {
		t.Fatal(err)
	}
}
func TestWebhookAndCancellation(t *testing.T) {
	now := time.Unix(1234567890, 0)
	body := []byte(`{"id":"event"}`)
	h := hmac.New(sha256.New, []byte("secret"))
	h.Write([]byte("1234567890."))
	h.Write(body)
	header := "t=1234567890,v1=" + hex.EncodeToString(h.Sum(nil))
	if !VerifyWebhook(body, header, "secret", now) || VerifyWebhook([]byte("tampered"), header, "secret", now) || VerifyWebhook(body, header, "secret", now.Add(301*time.Second)) {
		t.Fatal("signature verification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleep(ctx, time.Hour) == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestProjectRetryReuseAndScopedResults(t *testing.T) {
	attempts := 0
	idem := ""
	received := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Docmatch-Org") != "org_team" {
			t.Error("lost selected account")
		}
		switch r.URL.Path {
		case "/v1/projects/prj_one/runs":
			attempts++
			if idem == "" {
				idem = r.Header.Get("Idempotency-Key")
			} else if idem != r.Header.Get("Idempotency-Key") {
				t.Error("changed retry key")
			}
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(503)
				return
			}
			_, _ = w.Write([]byte(`{"state":"unchanged","revision":4,"run":{"id":"run_same","status":"completed"}}`))
		case "/v1/projects/prj_one/runs/run_same/findings":
			if r.URL.Query().Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"items":[],"next_cursor":"a+b/c="}`))
			} else if r.URL.Query().Get("cursor") == "a+b/c=" {
				_, _ = w.Write([]byte(`{"items":[],"next_cursor":null}`))
			} else {
				t.Error("damaged cursor")
			}
		default:
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":"forbidden","message":"Sem acesso","details":{"reason":"membership_removed"}}`))
		}
	}))
	defer server.Close()
	c, _ := New(server.URL, "")
	project := c.WithAccount("org_team").Project("prj_one")
	result, err := project.Reconcile(context.Background(), 4, nil, "")
	if err != nil || result.State != "unchanged" || result.Run.ID != "run_same" || result.Quote != nil || attempts != 2 {
		t.Fatal(result, err, attempts)
	}
	if _, err := project.Findings(context.Background(), result.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Get(context.Background()); err == nil {
		t.Fatal("removed access accepted")
	} else if e, ok := err.(*Error); !ok || e.Code != "forbidden" || len(e.Details) == 0 {
		t.Fatal(err)
	}
	if c.orgID != "" || len(received) != 5 {
		t.Fatal("mutated original client or unexpected request", received)
	}
}

func TestProjectUploadKeepsAccountAndReplacementOutsideStorage(t *testing.T) {
	paths := []string{}
	object := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Docmatch-Org") != "" || r.Header.Get("Cookie") != "" {
			t.Error("API credentials or context reached storage")
		}
		w.WriteHeader(204)
	}))
	defer object.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Docmatch-Org") != "org_team" {
			t.Error("account changed during upload")
		}
		if len(paths) == 1 {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["replaces_id"] != "doc_old" {
				t.Error("replacement was not staged")
			}
			_, _ = w.Write([]byte(`{"id":"upl_new","method":"PUT","url":"` + object.URL + `"}`))
		} else {
			_, _ = w.Write([]byte(`{"id":"upl_new","status":"ready"}`))
		}
	}))
	defer api.Close()
	client, _ := New(api.URL, "")
	bound := client.WithAccount("org_team").Project("prj_one")
	_, err := bound.UploadReplacement(context.Background(), "doc_old", "data.csv", "text/csv", []byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/projects/prj_one/uploads" || paths[1] != "/v1/projects/prj_one/uploads/upl_new/complete" {
		t.Fatal(paths)
	}
}
