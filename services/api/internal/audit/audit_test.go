package audit_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

var (
	actor  = audit.Actor{Type: audit.ActorUser, ID: "user-1"}
	target = audit.Target{Type: "run", ID: "run-1"}
)

func record(t *testing.T, svc *audit.Service, org *uuid.UUID, n int) []audit.Entry {
	t.Helper()
	out := make([]audit.Entry, 0, n)
	for i := range n {
		e, err := svc.Record(context.Background(), org, actor, "run.create", target, map[string]any{"i": i, "nested": map[string]any{"b": 1, "a": "x"}})
		if err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
		out = append(out, e)
	}
	return out
}

func TestRecordBuildsChain(t *testing.T) {
	t.Parallel()
	store := audit.NewMemoryStore()
	svc := audit.New(store)
	org := uuid.New()
	entries := record(t, svc, &org, 5)
	for i, e := range entries {
		if e.Seq != int64(i+1) || e.ChainID != org || e.OrgID == nil || *e.OrgID != org || len(e.Hash) != 32 || e.ID == uuid.Nil {
			t.Fatalf("entrada %d inesperada: %+v", i, e)
		}
		if i == 0 && e.PrevHash != nil {
			t.Fatal("primeira entrada não tem prev_hash")
		}
		if i > 0 && !bytes.Equal(e.PrevHash, entries[i-1].Hash) {
			t.Fatalf("entrada %d não encadeia com a anterior", i)
		}
		if !bytes.Equal(audit.Hash(e), e.Hash) {
			t.Fatalf("hash da entrada %d não confere", i)
		}
		if e.MetaText != `{"i":`+string(rune('0'+i))+`,"nested":{"a":"x","b":1}}` {
			t.Fatalf("meta canônica inesperada: %s", e.MetaText)
		}
		if e.CreatedAt.Location() != time.UTC || e.CreatedAt.Nanosecond()%1000 != 0 {
			t.Fatalf("created_at precisa ser UTC com microssegundos: %v", e.CreatedAt)
		}
	}
	rep, err := svc.Verify(context.Background(), &org, 1, 0)
	if err != nil || rep.Checked != 5 || rep.FirstSeq != 1 || rep.LastSeq != 5 || rep.BrokenSeq != 0 {
		t.Fatalf("Verify: %+v %v", rep, err)
	}
}

func TestChainsAreIndependent(t *testing.T) {
	t.Parallel()
	store := audit.NewMemoryStore()
	svc := audit.New(store)
	a, b := uuid.New(), uuid.New()
	record(t, svc, &a, 3)
	record(t, svc, &b, 2)
	sys := record(t, svc, nil, 2)
	if sys[0].ChainID != audit.SystemChain || sys[0].OrgID != nil || sys[1].Seq != 2 {
		t.Fatalf("cadeia de sistema inesperada: %+v", sys)
	}
	for _, org := range []*uuid.UUID{&a, &b, nil} {
		if _, err := svc.Verify(context.Background(), org, 1, 0); err != nil {
			t.Fatalf("Verify %v: %v", org, err)
		}
	}
	rep, _ := svc.Verify(context.Background(), &b, 1, 0)
	if rep.Checked != 2 {
		t.Fatalf("cadeia B deveria ter 2 entradas: %+v", rep)
	}
	if audit.ChainID(nil) != uuid.Nil || audit.ChainID(&a) != a {
		t.Fatal("ChainID")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		tamper func(store *audit.MemoryStore, org *uuid.UUID)
		broken int64
		reason string
	}{
		{"meta alterada", func(s *audit.MemoryStore, org *uuid.UUID) {
			s.Tamper(org, 3, func(e *audit.Entry) { e.MetaText = `{"i":99}` })
		}, 3, "hash does not match"},
		{"ação alterada", func(s *audit.MemoryStore, org *uuid.UUID) {
			s.Tamper(org, 2, func(e *audit.Entry) { e.Action = "run.delete" })
		}, 2, "hash does not match"},
		{"ator alterado", func(s *audit.MemoryStore, org *uuid.UUID) {
			s.Tamper(org, 4, func(e *audit.Entry) { e.Actor.ID = "outro" })
		}, 4, "hash does not match"},
		{"alvo alterado", func(s *audit.MemoryStore, org *uuid.UUID) {
			s.Tamper(org, 4, func(e *audit.Entry) { e.Target.ID = "outro" })
		}, 4, "hash does not match"},
		{"data alterada", func(s *audit.MemoryStore, org *uuid.UUID) {
			s.Tamper(org, 1, func(e *audit.Entry) { e.CreatedAt = e.CreatedAt.Add(time.Microsecond) })
		}, 1, "hash does not match"},
		{"hash recalculado sem encadear", func(s *audit.MemoryStore, org *uuid.UUID) {
			s.Tamper(org, 2, func(e *audit.Entry) { e.Action = "x"; e.Hash = audit.Hash(*e) })
		}, 3, "prev_hash does not match"},
		{"linha removida no meio", func(s *audit.MemoryStore, org *uuid.UUID) { s.Remove(org, 3) }, 3, "expected seq 3"},
		{"última linha removida", func(s *audit.MemoryStore, org *uuid.UUID) { s.Remove(org, 5) }, 5, "row missing at the end"},
		{"linha reordenada", func(s *audit.MemoryStore, org *uuid.UUID) {
			var e2, e3 audit.Entry
			s.Tamper(org, 2, func(e *audit.Entry) { e2 = *e })
			s.Tamper(org, 3, func(e *audit.Entry) { e3 = *e })
			s.Tamper(org, 2, func(e *audit.Entry) { *e = e3; e.Seq = 2 })
			s.Tamper(org, 3, func(e *audit.Entry) { *e = e2; e.Seq = 3 })
		}, 2, "prev_hash does not match"},
		{"prev_hash forjado", func(s *audit.MemoryStore, org *uuid.UUID) {
			s.Tamper(org, 1, func(e *audit.Entry) { e.PrevHash = []byte("x"); e.Hash = audit.Hash(*e) })
		}, 1, "prev_hash does not match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := audit.NewMemoryStore()
			svc := audit.New(store)
			org := uuid.New()
			record(t, svc, &org, 5)
			tc.tamper(store, &org)
			to := int64(0)
			if strings.Contains(tc.name, "última") {
				to = 5
			}
			rep, err := svc.Verify(context.Background(), &org, 1, to)
			if !errors.Is(err, audit.ErrTampered) {
				t.Fatalf("esperava ErrTampered, veio %v (%+v)", err, rep)
			}
			if rep.BrokenSeq != tc.broken || !strings.Contains(rep.Reason, tc.reason) {
				t.Fatalf("relatório = %+v, esperado seq %d (%s)", rep, tc.broken, tc.reason)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("erro não descreve o problema: %v", err)
			}
		})
	}
}

func TestVerifyRanges(t *testing.T) {
	t.Parallel()
	store := audit.NewMemoryStore()
	svc := audit.New(store)
	org := uuid.New()
	ctx := context.Background()
	if rep, err := svc.Verify(ctx, &org, 1, 0); err != nil || rep.Checked != 0 {
		t.Fatalf("cadeia vazia: %+v %v", rep, err)
	}
	record(t, svc, &org, 10)
	rep, err := svc.Verify(ctx, &org, 4, 7)
	if err != nil || rep.Checked != 4 || rep.FirstSeq != 4 || rep.LastSeq != 7 {
		t.Fatalf("faixa parcial: %+v %v", rep, err)
	}
	if rep, err := svc.Verify(ctx, &org, -5, 0); err != nil || rep.Checked != 10 {
		t.Fatalf("from negativo vira 1: %+v %v", rep, err)
	}
	if _, err := svc.Verify(ctx, &org, 5, 4); err == nil {
		t.Fatal("faixa invertida deveria falhar")
	}
	if rep, err := svc.Verify(ctx, &org, 3, 50); !errors.Is(err, audit.ErrTampered) || rep.BrokenSeq != 11 {
		t.Fatalf("faixa além do fim deveria acusar linha ausente: %+v %v", rep, err)
	}
	// Tampering before the range is caught through the anchor.
	store.Tamper(&org, 3, func(e *audit.Entry) { e.Hash = []byte(strings.Repeat("z", 32)) })
	if rep, err := svc.Verify(ctx, &org, 4, 6); !errors.Is(err, audit.ErrTampered) || rep.BrokenSeq != 4 {
		t.Fatalf("âncora alterada deveria quebrar a faixa seguinte: %+v %v", rep, err)
	}
	store.Remove(&org, 3)
	if rep, err := svc.Verify(ctx, &org, 4, 6); !errors.Is(err, audit.ErrTampered) || !strings.Contains(rep.Reason, "anchor") {
		t.Fatalf("âncora ausente: %+v %v", rep, err)
	}
}

func TestRecordValidation(t *testing.T) {
	t.Parallel()
	svc := audit.New(audit.NewMemoryStore())
	ctx := context.Background()
	org := uuid.New()
	cases := []struct {
		name   string
		actor  audit.Actor
		action string
		target audit.Target
		meta   any
		want   error
	}{
		{"ator inválido", audit.Actor{Type: "robot", ID: "x"}, "a", target, nil, audit.ErrInvalidActor},
		{"ator sem id", audit.Actor{Type: audit.ActorUser, ID: " "}, "a", target, nil, audit.ErrInvalidActor},
		{"sem ação", actor, "", target, nil, audit.ErrInvalidEntry},
		{"sem tipo de alvo", actor, "a", audit.Target{}, nil, audit.ErrInvalidEntry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := svc.Record(ctx, &org, tc.actor, tc.action, tc.target, tc.meta); !errors.Is(err, tc.want) {
				t.Fatalf("erro = %v, esperado %v", err, tc.want)
			}
		})
	}
	if _, err := svc.Record(ctx, &org, actor, "a", target, make(chan int)); err == nil {
		t.Fatal("meta não serializável deveria falhar")
	}
	if _, err := svc.Record(ctx, &org, actor, "a", target, []int{1}); err == nil {
		t.Fatal("meta precisa ser objeto JSON")
	}
	e, err := svc.Record(ctx, &org, actor, "a", target, nil)
	if err != nil || e.MetaText != "{}" {
		t.Fatalf("meta nula vira {}: %+v %v", e, err)
	}
	for _, a := range []audit.ActorType{audit.ActorUser, audit.ActorAPIKey, audit.ActorSystem, audit.ActorAdmin} {
		if !a.Valid() {
			t.Errorf("%s deveria ser válido", a)
		}
	}
	nilSvc := audit.New(nil)
	if _, err := nilSvc.Record(ctx, &org, actor, "a", target, nil); !errors.Is(err, audit.ErrNilStore) {
		t.Fatal(err)
	}
	if _, err := nilSvc.Verify(ctx, &org, 1, 0); !errors.Is(err, audit.ErrNilStore) {
		t.Fatal(err)
	}
}

func TestRecordCarriesRequestIDAndClock(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 11, 4, 12, 0, 0, 123456789, time.UTC)
	svc := audit.New(audit.NewMemoryStore(), audit.WithClock(func() time.Time { return fixed }), audit.WithClock(nil))
	ctx := httpx.WithRequestID(context.Background(), "req-42")
	e, err := svc.Record(ctx, nil, audit.Actor{Type: audit.ActorSystem, ID: "worker"}, "partitions.ensure", audit.Target{Type: "db"}, map[string]int{"created": 2})
	if err != nil {
		t.Fatal(err)
	}
	if e.RequestID != "req-42" || !e.CreatedAt.Equal(fixed.Truncate(time.Microsecond)) {
		t.Fatalf("request id/relógio: %+v", e)
	}
}

func TestHashIsDeterministicAndFieldSensitive(t *testing.T) {
	t.Parallel()
	base := audit.Entry{ChainID: uuid.New(), Seq: 7, Actor: actor, Action: "a", Target: target, MetaText: "{}", PrevHash: []byte("p"), CreatedAt: time.Now()}
	h := audit.Hash(base)
	if !bytes.Equal(h, audit.Hash(base)) {
		t.Fatal("hash não determinístico")
	}
	mutations := []func(e *audit.Entry){
		func(e *audit.Entry) { e.Seq++ },
		func(e *audit.Entry) { e.ChainID = uuid.New() },
		func(e *audit.Entry) { e.Actor.Type = audit.ActorAdmin },
		func(e *audit.Entry) { e.Actor.ID += "x" },
		func(e *audit.Entry) { e.Action += "x" },
		func(e *audit.Entry) { e.Target.Type += "x" },
		func(e *audit.Entry) { e.Target.ID += "x" },
		func(e *audit.Entry) { e.MetaText = `{"a":1}` },
		func(e *audit.Entry) { e.PrevHash = nil },
		func(e *audit.Entry) { e.CreatedAt = e.CreatedAt.Add(time.Microsecond) },
	}
	for i, m := range mutations {
		e := base
		m(&e)
		if bytes.Equal(audit.Hash(e), h) {
			t.Fatalf("mutação %d não alterou o hash", i)
		}
	}
	// Field boundaries are length-prefixed: moving a newline between fields changes the hash.
	a := base
	a.Action, a.Target.Type = "x\n", "y"
	b := base
	b.Action, b.Target.Type = "x", "\ny"
	if bytes.Equal(audit.Hash(a), audit.Hash(b)) {
		t.Fatal("campos deveriam ter fronteiras inequívocas")
	}
	if !strings.HasPrefix(string(audit.Canonical(base)), "17:"+audit.HashVersion) {
		t.Fatalf("forma canônica inesperada: %s", audit.Canonical(base))
	}
}

func TestConcurrentRecordsAreGapless(t *testing.T) {
	t.Parallel()
	store := audit.NewMemoryStore()
	svc := audit.New(store)
	org := uuid.New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Record(context.Background(), &org, actor, "a", target, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rep, err := svc.Verify(context.Background(), &org, 1, 50)
	if err != nil || rep.Checked != 50 {
		t.Fatalf("50 registros concorrentes: %+v %v", rep, err)
	}
}

func TestMemoryStoreSemantics(t *testing.T) {
	t.Parallel()
	store := audit.NewMemoryStore()
	org := uuid.New()
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.InChainTx(cancelled, &org, func(context.Context, audit.Tx) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("contexto cancelado: %v", err)
	}
	err := store.InChainTx(ctx, &org, func(ctx context.Context, tx audit.Tx) error {
		if _, _, _, err := tx.Tail(ctx, uuid.New()); err == nil {
			t.Error("Tail de outra cadeia deveria falhar")
		}
		if _, err := tx.Append(ctx, audit.Entry{ChainID: uuid.New(), Seq: 1}); err == nil {
			t.Error("Append em outra cadeia deveria falhar")
		}
		if _, err := tx.Append(ctx, audit.Entry{ChainID: org, Seq: 0}); err == nil {
			t.Error("Append sem seq/hash deveria falhar")
		}
		if _, err := tx.Canonical(ctx, []byte("{nope")); err == nil {
			t.Error("JSON inválido deveria falhar")
		}
		c, err := tx.Canonical(ctx, []byte(` {"z": 1.50, "a": [1, 2], "m": {"y": null, "x": "é"}} `))
		if err != nil || c != `{"a":[1,2],"m":{"x":"é","y":null},"z":1.50}` {
			t.Errorf("canônico = %q (%v)", c, err)
		}
		e, err := tx.Append(ctx, audit.Entry{ChainID: org, Seq: 1, Hash: bytes.Repeat([]byte{1}, 32)})
		if err != nil || e.ID == uuid.Nil {
			t.Errorf("Append: %+v %v", e, err)
		}
		return errors.New("desfaz")
	})
	if err == nil || store.Len() != 0 {
		t.Fatalf("transação com erro deveria restaurar: %v len=%d", err, store.Len())
	}
	func() {
		defer func() { _ = recover() }()
		_ = store.InChainTx(ctx, &org, func(ctx context.Context, tx audit.Tx) error {
			_, _ = tx.Append(ctx, audit.Entry{ChainID: org, Seq: 1, Hash: bytes.Repeat([]byte{1}, 32)})
			panic("boom")
		})
	}()
	if store.Len() != 0 {
		t.Fatal("pânico deveria restaurar")
	}
	if store.Tamper(&org, 1, func(*audit.Entry) {}) || store.Remove(&org, 1) {
		t.Fatal("Tamper/Remove em entrada inexistente devolvem false")
	}
}
