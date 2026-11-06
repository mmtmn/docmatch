package workspace

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResumeUsesProjectResultWithoutNewRunOrApproval(t *testing.T) {
	var starts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing key")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/projects/prj_saved":
			_, _ = fmt.Fprint(w, `{"id":"prj_saved","name":"Novembro","revision":2,"capabilities":{"reconcile":true}}`)
		case "/v1/projects/prj_saved/runs/estimate":
			_, _ = fmt.Fprint(w, `{"state":"unchanged","revision":2,"run":{"id":"run_done","status":"completed"}}`)
		case "/v1/projects/prj_saved/runs/run_done":
			_, _ = fmt.Fprint(w, `{"id":"run_done","status":"completed"}`)
		case "/v1/projects/prj_saved/runs/run_done/export":
			_, _ = fmt.Fprint(w, "PK-test-report")
		case "/v1/projects/prj_saved/runs":
			starts.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	var output bytes.Buffer
	directory := t.TempDir()
	err := Run(t.Context(), Options{API: server.URL, Key: "test-key", ProjectID: "prj_saved", Plain: true, Out: &output, Output: directory})
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 0 {
		t.Fatal("reused result started a new run")
	}
	if !strings.Contains(output.String(), "sem nova cobrança") {
		t.Fatal(output.String())
	}
	data, err := os.ReadFile(filepath.Join(directory, "run_done.xlsx"))
	if err != nil || string(data) != "PK-test-report" {
		t.Fatalf("export %q: %v", data, err)
	}
}

func TestChangedProjectRequiresExplicitApproval(t *testing.T) {
	var starts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/projects/prj_saved":
			_, _ = fmt.Fprint(w, `{"id":"prj_saved","name":"Novembro","revision":3,"capabilities":{"reconcile":true}}`)
		case "/v1/projects/prj_saved/runs/estimate":
			_, _ = fmt.Fprint(w, `{"state":"changed","revision":3,"quote":{"id":"q1","cap_credits":100,"expected_credits":80,"signature":"signed"}}`)
		default:
			starts.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	var output bytes.Buffer
	err := Run(t.Context(), Options{API: server.URL, Key: "key", ProjectID: "prj_saved", Plain: true, Out: &output})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("approval: %v", err)
	}
	if starts.Load() != 0 {
		t.Fatal("started without approval")
	}
	if !strings.Contains(output.String(), "limite: 100") {
		t.Fatal(output.String())
	}
}

func TestAPIKeyCannotSelectAnotherAccount(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	var output bytes.Buffer
	err := Run(t.Context(), Options{API: server.URL, Key: "key", AccountID: "org_other", ProjectID: "prj_saved", Plain: true, Out: &output})
	if err == nil || requests.Load() != 0 {
		t.Fatalf("redirected key: %v, %d", err, requests.Load())
	}
}
