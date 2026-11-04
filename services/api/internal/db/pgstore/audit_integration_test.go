//go:build integration

package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

var (
	auditActor  = audit.Actor{Type: audit.ActorUser, ID: "user-1"}
	auditTarget = audit.Target{Type: "run", ID: "run-1"}
)

func TestAuditChainOnPostgres(t *testing.T) {
	d := testdb.Get(t)
	ctx := httpx.WithRequestID(context.Background(), "req-1")
	svc := audit.New(pgstore.NewAuditStore(d.App))
	org := newOrg(t, d)

	entries := make([]audit.Entry, 0, 6)
	for i := range 6 {
		e, err := svc.Record(ctx, &org, auditActor, "run.create", auditTarget, map[string]any{"i": i, "valor": 1.50, "texto": "ação", "lista": []int{3, 1}})
		if err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
		entries = append(entries, e)
	}
	if entries[0].RequestID != "req-1" || entries[0].Seq != 1 || entries[5].Seq != 6 {
		t.Fatalf("entradas inesperadas: %+v", entries[0])
	}
	// meta is hashed as PostgreSQL canonicalizes it and read back identically.
	rep, err := svc.Verify(ctx, &org, 1, 0)
	if err != nil || rep.Checked != 6 {
		t.Fatalf("Verify: %+v %v", rep, err)
	}
	rows, err := pgstore.NewAuditStore(d.App).Range(ctx, &org, 1, 1)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].MetaText, `"valor": 1.5`) || !strings.Contains(rows[0].MetaText, `"texto": "ação"`) {
		t.Fatalf("meta canônica do PostgreSQL inesperada: %+v %v", rows, err)
	}

	// System chain (no org) works in system mode and is independent.
	if _, err := svc.Record(ctx, nil, audit.Actor{Type: audit.ActorSystem, ID: "worker"}, "partitions.ensure", audit.Target{Type: "db"}, nil); err != nil {
		t.Fatalf("cadeia de sistema: %v", err)
	}
	if rep, err := svc.Verify(ctx, nil, 1, 0); err != nil || rep.Checked < 1 {
		t.Fatalf("Verify sistema: %+v %v", rep, err)
	}

	// Tampering with superuser access is detected.
	if _, err := d.Admin.Exec(ctx, "UPDATE audit_log SET meta = '{\"i\": 42}' WHERE chain_id = $1 AND seq = 3", org); err != nil {
		t.Fatal(err)
	}
	rep, err = svc.Verify(ctx, &org, 1, 0)
	if !errors.Is(err, audit.ErrTampered) || rep.BrokenSeq != 3 || !strings.Contains(rep.Reason, "hash") {
		t.Fatalf("alteração de meta não detectada: %+v %v", rep, err)
	}
	if _, err := d.Admin.Exec(ctx, "UPDATE audit_log SET meta = $2 WHERE chain_id = $1 AND seq = 3", org, []byte(entries[2].MetaText)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(ctx, &org, 1, 0); err != nil {
		t.Fatalf("após restaurar deveria verificar: %v", err)
	}
	if _, err := d.Admin.Exec(ctx, "DELETE FROM audit_log WHERE chain_id = $1 AND seq = 4", org); err != nil {
		t.Fatal(err)
	}
	rep, err = svc.Verify(ctx, &org, 1, 0)
	if !errors.Is(err, audit.ErrTampered) || rep.BrokenSeq != 4 || !strings.Contains(rep.Reason, "expected seq 4") {
		t.Fatalf("remoção não detectada: %+v %v", rep, err)
	}
	// Partial ranges before the hole still verify.
	if rep, err := svc.Verify(ctx, &org, 2, 3); err != nil || rep.Checked != 2 {
		t.Fatalf("faixa íntegra: %+v %v", rep, err)
	}
}

func TestAuditIsolationAndAppendOnly(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	store := pgstore.NewAuditStore(d.App)
	svc := audit.New(store)
	a, b := newOrg(t, d), newOrg(t, d)
	if _, err := svc.Record(ctx, &a, auditActor, "x", auditTarget, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Range(ctx, &b, 1, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("org B não vê a cadeia de A: %d %v", len(rows), err)
	}
	// The app role cannot rewrite history.
	if _, err := d.App.Exec(ctx, "UPDATE audit_log SET action = 'y'"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("UPDATE deveria ser negado: %v", err)
	}
}

func TestAuditConcurrentAppends(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	svc := audit.New(pgstore.NewAuditStore(d.App))
	org := newOrg(t, d)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Record(ctx, &org, auditActor, "a", auditTarget, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rep, err := svc.Verify(ctx, &org, 1, 40)
	if err != nil || rep.Checked != 40 {
		t.Fatalf("40 appends concorrentes: %+v %v", rep, err)
	}
}
