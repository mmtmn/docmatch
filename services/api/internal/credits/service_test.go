package credits_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/credits/credtest"
)

func memoryFactory(t *testing.T) (credits.Store, func(t *testing.T) uuid.UUID) {
	t.Helper()
	return credits.NewMemoryStore(), func(t *testing.T) uuid.UUID { return uuid.New() }
}

func TestMemoryStoreConformance(t *testing.T) {
	t.Parallel()
	credtest.Run(t, memoryFactory)
}

func TestServiceValidation(t *testing.T) {
	t.Parallel()
	svc := credits.New(credits.NewMemoryStore())
	ctx := context.Background()
	org := uuid.New()
	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"grant zero", func() error {
			_, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 0})
			return err
		}, credits.ErrInvalidAmount},
		{"grant negativo", func() error {
			_, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: -5})
			return err
		}, credits.ErrInvalidAmount},
		{"grant tipo", func() error { _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: "x", Amount: 5}); return err }, credits.ErrInvalidKind},
		{"reserve zero", func() error { _, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 0}); return err }, credits.ErrInvalidAmount},
		{"settle negativo", func() error { _, err := svc.Settle(ctx, org, uuid.New(), -1); return err }, credits.ErrInvalidAmount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.call(); !errors.Is(err, tc.want) {
				t.Fatalf("erro = %v, esperado %v", err, tc.want)
			}
		})
	}
	for _, k := range []credits.GrantKind{credits.GrantPlan, credits.GrantTopup, credits.GrantBonus, credits.GrantRefund, credits.GrantAdjustment} {
		if !k.Valid() {
			t.Errorf("%s deveria ser válido", k)
		}
	}
	if credits.GrantKind("").Valid() {
		t.Error("tipo vazio não é válido")
	}
}

func TestNilStore(t *testing.T) {
	t.Parallel()
	svc := credits.New(nil)
	ctx := context.Background()
	org := uuid.New()
	if _, err := svc.Balance(ctx, org); !errors.Is(err, credits.ErrNilStore) {
		t.Fatal(err)
	}
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 1}); !errors.Is(err, credits.ErrNilStore) {
		t.Fatal(err)
	}
	if _, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 1}); !errors.Is(err, credits.ErrNilStore) {
		t.Fatal(err)
	}
	if _, err := svc.Settle(ctx, org, uuid.New(), 1); !errors.Is(err, credits.ErrNilStore) {
		t.Fatal(err)
	}
	if _, err := svc.Release(ctx, org, uuid.New()); !errors.Is(err, credits.ErrNilStore) {
		t.Fatal(err)
	}
	if _, err := svc.ExpireGrants(ctx, org, time.Now()); !errors.Is(err, credits.ErrNilStore) {
		t.Fatal(err)
	}
	if _, err := svc.RecentLedger(ctx, org, 1); !errors.Is(err, credits.ErrNilStore) {
		t.Fatal(err)
	}
}

func TestWithClock(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	store := credits.NewMemoryStore()
	svc := credits.New(store, credits.WithClock(func() time.Time { return fixed }), credits.WithClock(nil))
	org := uuid.New()
	g, err := svc.Grant(context.Background(), org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 1})
	if err != nil || !g.GrantedAt.Equal(fixed) {
		t.Fatalf("relógio injetado ignorado: %+v %v", g, err)
	}
	rows, _ := svc.RecentLedger(context.Background(), org, 1)
	if len(rows) != 1 || !rows[0].CreatedAt.Equal(fixed) {
		t.Fatalf("lançamento deveria usar o relógio: %+v", rows)
	}
}

func TestMemoryStoreTransactionSemantics(t *testing.T) {
	t.Parallel()
	store := credits.NewMemoryStore()
	ctx := context.Background()
	org := uuid.New()
	if err := store.InOrgTx(ctx, uuid.Nil, func(context.Context, credits.Tx) error { return nil }); err == nil {
		t.Fatal("org nula deveria falhar")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.InOrgTx(cancelled, org, func(context.Context, credits.Tx) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("contexto cancelado: %v", err)
	}
	// Rows of another org are invisible and untouchable.
	other := uuid.New()
	err := store.InOrgTx(ctx, org, func(ctx context.Context, tx credits.Tx) error {
		if _, _, err := tx.Account(ctx, other); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("Account de outra org: %v", err)
		}
		if _, err := tx.AccountForUpdate(ctx, other); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("AccountForUpdate de outra org: %v", err)
		}
		if _, err := tx.UpdateAccount(ctx, other, 1, 0); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("UpdateAccount de outra org: %v", err)
		}
		if _, _, err := tx.GrantByIdempotencyKey(ctx, other, "k"); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("GrantByIdempotencyKey de outra org: %v", err)
		}
		if _, err := tx.InsertGrant(ctx, credits.Grant{OrgID: other, Amount: 1, Remaining: 1}); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("InsertGrant de outra org: %v", err)
		}
		if _, err := tx.OpenGrantsForUpdate(ctx, other); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("OpenGrantsForUpdate de outra org: %v", err)
		}
		if _, err := tx.ExpiredGrantsForUpdate(ctx, other, time.Now()); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("ExpiredGrantsForUpdate de outra org: %v", err)
		}
		if err := tx.SetGrantRemaining(ctx, uuid.New(), 0); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("SetGrantRemaining inexistente: %v", err)
		}
		if err := tx.MarkGrantExpired(ctx, uuid.New(), 0, time.Now()); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("MarkGrantExpired inexistente: %v", err)
		}
		if _, err := tx.InsertHold(ctx, credits.Hold{OrgID: other, Amount: 1}); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("InsertHold de outra org: %v", err)
		}
		if _, _, err := tx.HoldForUpdate(ctx, other, uuid.New()); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("HoldForUpdate de outra org: %v", err)
		}
		if _, err := tx.CloseHold(ctx, other, uuid.New(), credits.HoldReleased, nil); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("CloseHold de outra org: %v", err)
		}
		if _, err := tx.AppendLedger(ctx, credits.LedgerEntry{OrgID: other}); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("AppendLedger de outra org: %v", err)
		}
		if _, err := tx.RecentLedger(ctx, other, 1); !errors.Is(err, credits.ErrWrongOrg) {
			t.Errorf("RecentLedger de outra org: %v", err)
		}
		// Constraint checks mirror the database.
		if _, err := tx.UpdateAccount(ctx, org, 5, 10); err == nil {
			t.Error("held > balance deveria falhar")
		}
		if _, err := tx.InsertGrant(ctx, credits.Grant{OrgID: org, Amount: 0, Remaining: 0}); err == nil {
			t.Error("grant zero deveria falhar")
		}
		if _, err := tx.InsertHold(ctx, credits.Hold{OrgID: org, Amount: 0}); err == nil {
			t.Error("hold zero deveria falhar")
		}
		g, err := tx.InsertGrant(ctx, credits.Grant{OrgID: org, Kind: credits.GrantPlan, Amount: 10, Remaining: 10, IdempotencyKey: "dup"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.InsertGrant(ctx, credits.Grant{OrgID: org, Kind: credits.GrantPlan, Amount: 10, Remaining: 10, IdempotencyKey: "dup"}); err == nil {
			t.Error("chave de idempotência duplicada deveria falhar")
		}
		if err := tx.SetGrantRemaining(ctx, g.ID, 11); err == nil {
			t.Error("remaining > amount deveria falhar")
		}
		h, err := tx.InsertHold(ctx, credits.Hold{OrgID: org, Amount: 5})
		if err != nil {
			t.Fatal(err)
		}
		bad := int64(6)
		if _, err := tx.CloseHold(ctx, org, h.ID, credits.HoldSettled, &bad); err == nil {
			t.Error("settled > amount deveria falhar")
		}
		if _, err := tx.CloseHold(ctx, org, uuid.New(), credits.HoldSettled, nil); !errors.Is(err, credits.ErrHoldClosed) {
			t.Errorf("fechar reserva inexistente: %v", err)
		}
		return errors.New("desfaz tudo")
	})
	if err == nil {
		t.Fatal("erro deveria propagar")
	}
	accounts, grants, holds, ledger := store.Snapshot()
	if len(accounts) != 0 || len(grants) != 0 || len(holds) != 0 || len(ledger) != 0 {
		t.Fatalf("estado deveria ter sido restaurado: %d %d %d %d", len(accounts), len(grants), len(holds), len(ledger))
	}
	// Panics roll back too.
	func() {
		defer func() { _ = recover() }()
		_ = store.InOrgTx(ctx, org, func(ctx context.Context, tx credits.Tx) error {
			_, _ = tx.AccountForUpdate(ctx, org)
			panic("boom")
		})
	}()
	accounts, _, _, _ = store.Snapshot()
	if len(accounts) != 0 {
		t.Fatal("pânico deveria restaurar o estado")
	}
}

func TestMemoryStoreGrantOrder(t *testing.T) {
	t.Parallel()
	store := credits.NewMemoryStore()
	ctx := context.Background()
	org := uuid.New()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	exp := t1.Add(48 * time.Hour)
	err := store.InOrgTx(ctx, org, func(ctx context.Context, tx credits.Tx) error {
		mk := func(granted time.Time, expires *time.Time) credits.Grant {
			g, err := tx.InsertGrant(ctx, credits.Grant{OrgID: org, Kind: credits.GrantPlan, Amount: 1, Remaining: 1, GrantedAt: granted, ExpiresAt: expires})
			if err != nil {
				t.Fatal(err)
			}
			return g
		}
		noExpLate := mk(t2, nil)
		noExpEarly := mk(t1, nil)
		expLate := mk(t2, &exp)
		expEarly := mk(t1, &exp)
		sameAll1 := mk(t1, &exp)
		got, err := tx.OpenGrantsForUpdate(ctx, org)
		if err != nil {
			return err
		}
		if len(got) != 5 {
			t.Fatalf("len = %d", len(got))
		}
		// Expiring grants first (oldest granted first), ties broken by id; then the perpetual ones.
		if got[0].ExpiresAt == nil || got[1].ExpiresAt == nil || got[2].ExpiresAt == nil {
			t.Fatalf("concessões com validade deveriam vir primeiro: %+v", got)
		}
		if got[2].ID != expLate.ID {
			t.Fatalf("a mais recente com validade deveria ser a terceira: %+v", got[2])
		}
		first := map[uuid.UUID]bool{expEarly.ID: true, sameAll1.ID: true}
		if !first[got[0].ID] || !first[got[1].ID] || got[0].ID.String() > got[1].ID.String() {
			t.Fatalf("empate deveria ser resolvido por id: %v %v", got[0].ID, got[1].ID)
		}
		if got[3].ID != noExpEarly.ID || got[4].ID != noExpLate.ID {
			t.Fatalf("perpétuas por data de concessão: %v %v", got[3].ID, got[4].ID)
		}
		// Ledger rows without a timestamp get one.
		e, err := tx.AppendLedger(ctx, credits.LedgerEntry{OrgID: org, Kind: credits.LedgerAdjust})
		if err != nil || e.CreatedAt.IsZero() || e.ID == uuid.Nil {
			t.Fatalf("AppendLedger: %+v %v", e, err)
		}
		h, err := tx.InsertHold(ctx, credits.Hold{OrgID: org, Amount: 1})
		if err != nil || h.CreatedAt.IsZero() {
			t.Fatalf("InsertHold: %+v %v", h, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInsufficientErrorIs(t *testing.T) {
	t.Parallel()
	err := &credits.InsufficientError{OrgID: uuid.New(), Requested: 5, Available: 1}
	if !errors.Is(err, credits.ErrInsufficient) || errors.Is(err, credits.ErrHoldClosed) {
		t.Fatal("Is incorreto")
	}
}
