//go:build integration

package projects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

func TestProjectUploadStagesReplacementUntilVerifiedAndApplied(t *testing.T) {
	f := pipeline(t)
	s, ctx := f.s, t.Context()
	p := seed(t, uuid.Nil, "owner")
	project, err := s.Create(ctx, p, "Extratos")
	if err != nil {
		t.Fatal(err)
	}
	id := projectID(t, project)
	content := []byte("date,amount\n2026-11-01,100\n")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	request := UploadRequest{TicketRequest: uploads.TicketRequest{Kind: uploads.KindDocument, FileName: "extrato.csv", SizeBytes: int64(len(content)), Mime: "text/csv", SHA256: hash, Role: "bank"}}
	create := func(req UploadRequest) uploads.DocumentTicket {
		t.Helper()
		raw, err := s.CreateUpload(ctx, p, id, req)
		if err != nil {
			t.Fatal(err)
		}
		ticket, ok := raw.(uploads.DocumentTicket)
		if !ok {
			t.Fatalf("ticket: %T", raw)
		}
		return ticket
	}
	upload := func(ticket uploads.DocumentTicket) uuid.UUID {
		t.Helper()
		uid, _ := publicid.Decode("upl", ticket.ID)
		item, err := s.uploads.Get(ctx, p.OrgID, uid)
		if err != nil {
			t.Fatal(err)
		}
		local := s.uploads.StoreFor(item).(*storage.LocalStore)
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for k, v := range ticket.Fields {
			if err := form.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
		file, err := form.CreateFormFile("file", "extrato.csv")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, ticket.URL, &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		res := httptest.NewRecorder()
		local.ServeHTTP(res, req)
		if res.Code < 200 || res.Code >= 300 {
			t.Fatal("object upload", res.Code, res.Body.String())
		}
		got, _, err := s.CompleteUpload(ctx, p, id, uid, uploads.CompleteRequest{SHA256: hash})
		if err != nil || got.Status != uploads.APIScanning {
			t.Fatal(got, err)
		}
		return uid
	}
	accept := func(ticket uploads.DocumentTicket) {
		t.Helper()
		_, err := s.uploads.ApplyVerdict(ctx, uploads.Verdict{Version: uploads.VerdictVersion, UploadID: ticket.ID, Verdict: uploads.VerdictAccept, Mime: "text/csv", SHA256: hash, SizeBytes: int64(len(content)), Scanner: uploads.ScannerInfo{Name: "integration"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	first := create(request)
	firstID := upload(first)
	accept(first)
	before, err := s.Get(ctx, p, id)
	if err != nil || before.DocumentCount != 1 || before.Documents[0].Status != uploads.APIReady {
		t.Fatal(before, err)
	}
	request.ReplacesID = before.Documents[0].ID
	next := create(request)
	secondID := upload(next)
	staged, err := s.Get(ctx, p, id)
	if err != nil || staged.DocumentCount != 1 || staged.Revision != before.Revision {
		t.Fatal("staging changed inputs", staged, err)
	}
	// Analysis remains on the first verified source, even from another device.
	err = db.WithOrgUnit(ctx, testdb.Get(t).App, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		req, _, err := s.snapshot(ctx, tx, p, id, staged.Revision, "app")
		if err != nil {
			return err
		}
		if len(req.Sources) != 1 || req.Sources[0].UploadID != first.ID {
			t.Fatal("staged replacement entered analysis", req.Sources)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var oldDoc, newDoc uuid.UUID
	oldDoc, _ = publicid.Decode("doc", before.Documents[0].ID)
	for _, doc := range staged.Documents {
		if doc.UploadID == next.ID {
			newDoc, _ = publicid.Decode("doc", doc.ID)
			if doc.PendingReplacementID == nil {
				t.Fatal("missing pending status")
			}
		}
	}
	if err := s.ReplaceDocument(ctx, p, id, oldDoc, newDoc, staged.Version); err == nil {
		t.Fatal("unverified replacement accepted")
	}
	accept(next)
	if err := s.ReplaceDocument(ctx, p, id, oldDoc, newDoc, staged.Version); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, p, id)
	if err != nil || after.DocumentCount != 1 || after.Revision != before.Revision+1 {
		t.Fatal(after, err)
	}
	other := seed(t, uuid.Nil, "owner")
	if _, _, err := s.CompleteUpload(ctx, other, id, secondID, uploads.CompleteRequest{SHA256: hash}); err == nil {
		t.Fatal("cross-account complete")
	}
	// The previous stored source is retained for historical results.
	if old, err := s.uploads.Get(ctx, p.OrgID, firstID); err != nil || old.Status != uploads.StatusClean {
		t.Fatal(old, err)
	}
}
