// Package credtest is the conformance suite every credits.Store must pass.
// The in-memory store runs it in unit tests; the pgx store runs it in the
// integration tests, so both backends prove the same invariants:
//
//	balance = Σ grants − Σ settled − Σ expired
//	held ≤ balance, held = Σ open holds
//	100 parallel Reserve calls never overdraw
//	Settle(actual > hold) is capped at the hold
//	a failure mid-operation rolls everything back
package credtest

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/credits"
)

// Factory builds a fresh store and a function that creates an organization
// the store accepts (the pgx store needs the orgs row to exist).
type Factory func(t *testing.T) (store credits.Store, newOrg func(t *testing.T) uuid.UUID)

// Run executes the suite against the factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("GrantReserveSettleRelease", func(t *testing.T) { testFlow(t, factory) })
	t.Run("ReserveRefusesOverdraw", func(t *testing.T) { testOverdraw(t, factory) })
	t.Run("SettleIsCappedAtHold", func(t *testing.T) { testSettleCap(t, factory) })
	t.Run("GrantIdempotency", func(t *testing.T) { testIdempotency(t, factory) })
	t.Run("ExpireGrantsRespectsHolds", func(t *testing.T) { testExpiry(t, factory) })
	t.Run("ParallelReserveNeverOverdraws", func(t *testing.T) { testParallelReserve(t, factory) })
	t.Run("CrashSafetyRollsBack", func(t *testing.T) { testCrashSafety(t, factory) })
	t.Run("EveryFailurePointRollsBack", func(t *testing.T) { testEveryFailurePoint(t, factory) })
	t.Run("InconsistentStoreIsRefused", func(t *testing.T) { testInconsistentStore(t, factory) })
	t.Run("ClosedHoldsAreFinal", func(t *testing.T) { testClosedHolds(t, factory) })
	t.Run("RandomSequenceKeepsInvariants", func(t *testing.T) { testRandomSequence(t, factory) })
	t.Run("OrgsAreIsolated", func(t *testing.T) { testIsolation(t, factory) })
}

// Invariants recomputes the account from ledger history and open holds and
// compares it with the stored account.
func Invariants(t *testing.T, svc *credits.Service, orgID uuid.UUID, granted, settled, expired int64, openHolds int64) {
	t.Helper()
	acct, err := svc.Balance(context.Background(), orgID)
	if err != nil {
		t.Fatal(err)
	}
	if want := granted - settled - expired; acct.Balance != want {
		t.Fatalf("balance = %d, esperado Σgrants−Σsettled−Σexpired = %d", acct.Balance, want)
	}
	if acct.Held != openHolds {
		t.Fatalf("held = %d, esperado Σ holds abertos = %d", acct.Held, openHolds)
	}
	if acct.Held < 0 || acct.Held > acct.Balance {
		t.Fatalf("invariante 0 ≤ held ≤ balance violada: %+v", acct)
	}
	rows, err := svc.RecentLedger(context.Background(), orgID, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return
	}
	if rows[0].BalanceAfter != acct.Balance || rows[0].HeldAfter != acct.Held {
		t.Fatalf("último lançamento (%d/%d) difere da conta (%d/%d)", rows[0].BalanceAfter, rows[0].HeldAfter, acct.Balance, acct.Held)
	}
	// Seq is a gapless, strictly descending sequence and the deltas replay
	// exactly to each snapshot.
	var balance, held int64
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r.Seq != int64(len(rows)-i) {
			t.Fatalf("seq %d na posição %d: sequência com lacuna ou fora de ordem", r.Seq, i)
		}
		balance += r.Amount
		held += r.HeldDelta
		if r.BalanceAfter != balance || r.HeldAfter != held {
			t.Fatalf("lançamento seq %d: deltas acumulados (%d/%d) ≠ snapshot (%d/%d)", r.Seq, balance, held, r.BalanceAfter, r.HeldAfter)
		}
	}
}

func testFlow(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)

	acct, err := svc.Balance(ctx, org)
	if err != nil || acct.Balance != 0 || acct.Held != 0 {
		t.Fatalf("conta nova deveria ser zero: %+v %v", acct, err)
	}
	g, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 1000, Source: "plano"})
	if err != nil || g.Amount != 1000 || g.Remaining != 1000 || g.OrgID != org {
		t.Fatalf("Grant: %+v %v", g, err)
	}
	Invariants(t, svc, org, 1000, 0, 0, 0)

	hold, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 300, Reference: "run-1"})
	if err != nil || hold.Amount != 300 || hold.Status != credits.HoldHeld {
		t.Fatalf("Reserve: %+v %v", hold, err)
	}
	Invariants(t, svc, org, 1000, 0, 0, 300)
	acct, _ = svc.Balance(ctx, org)
	if acct.Available() != 700 {
		t.Fatalf("Available = %d", acct.Available())
	}

	st, err := svc.Settle(ctx, org, hold.ID, 120)
	if err != nil || st.Debited != 120 || st.Released != 180 || st.Hold.Status != credits.HoldSettled || st.Hold.SettledAmount == nil || *st.Hold.SettledAmount != 120 {
		t.Fatalf("Settle: %+v %v", st, err)
	}
	Invariants(t, svc, org, 1000, 120, 0, 0)

	hold2, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 500})
	if err != nil {
		t.Fatal(err)
	}
	Invariants(t, svc, org, 1000, 120, 0, 500)
	rel, err := svc.Release(ctx, org, hold2.ID)
	if err != nil || rel.Status != credits.HoldReleased || rel.SettledAmount != nil {
		t.Fatalf("Release: %+v %v", rel, err)
	}
	Invariants(t, svc, org, 1000, 120, 0, 0)

	// Settling exactly the hold and settling zero both work.
	h3, _ := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 80})
	if st, err := svc.Settle(ctx, org, h3.ID, 80); err != nil || st.Debited != 80 || st.Released != 0 {
		t.Fatalf("Settle exato: %+v %v", st, err)
	}
	h4, _ := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 80})
	if st, err := svc.Settle(ctx, org, h4.ID, 0); err != nil || st.Debited != 0 || st.Released != 80 {
		t.Fatalf("Settle zero: %+v %v", st, err)
	}
	Invariants(t, svc, org, 1000, 200, 0, 0)

	rows, err := svc.RecentLedger(ctx, org, 100)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[credits.LedgerKind]int{}
	for _, r := range rows {
		kinds[r.Kind]++
	}
	if kinds[credits.LedgerGrant] != 1 || kinds[credits.LedgerHold] != 4 || kinds[credits.LedgerSettle] != 3 || kinds[credits.LedgerRelease] != 1 {
		t.Fatalf("lançamentos inesperados: %v", kinds)
	}
	if rows[0].Kind != credits.LedgerSettle {
		t.Fatalf("RecentLedger deveria vir do mais novo: %v", rows[0].Kind)
	}
	if rows, err := svc.RecentLedger(ctx, org, 0); err != nil || len(rows) != 9 {
		t.Fatalf("limite padrão: %d %v", len(rows), err)
	}
}

func testOverdraw(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)
	if _, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 1}); !errors.Is(err, credits.ErrInsufficient) {
		t.Fatalf("conta vazia deveria recusar: %v", err)
	}
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantTopup, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 101}); !errors.Is(err, credits.ErrInsufficient) {
		t.Fatalf("acima do saldo deveria recusar: %v", err)
	}
	var ie *credits.InsufficientError
	_, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 500})
	if !errors.As(err, &ie) || ie.Requested != 500 || ie.Available != 100 || ie.OrgID != org || ie.Error() == "" {
		t.Fatalf("InsufficientError esperado com detalhes: %v", err)
	}
	if _, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 100}); err != nil {
		t.Fatalf("exatamente o saldo deveria passar: %v", err)
	}
	if _, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 1}); !errors.Is(err, credits.ErrInsufficient) {
		t.Fatalf("saldo todo reservado deveria recusar: %v", err)
	}
	Invariants(t, svc, org, 100, 0, 0, 100)
}

func testSettleCap(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 500}); err != nil {
		t.Fatal(err)
	}
	hold, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 200})
	if err != nil {
		t.Fatal(err)
	}
	st, err := svc.Settle(ctx, org, hold.ID, 999)
	if err != nil {
		t.Fatal(err)
	}
	if st.Debited != 200 || st.Released != 0 {
		t.Fatalf("liquidação acima da reserva deveria ser limitada: %+v", st)
	}
	Invariants(t, svc, org, 500, 200, 0, 0)
}

func testIdempotency(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)
	p := credits.GrantParams{Kind: credits.GrantTopup, Amount: 250, Source: "fatura-1", IdempotencyKey: "in_123"}
	g1, err := svc.Grant(ctx, org, p)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := svc.Grant(ctx, org, p)
	if err != nil || g2.ID != g1.ID {
		t.Fatalf("segunda concessão deveria devolver a primeira: %+v %+v %v", g1, g2, err)
	}
	Invariants(t, svc, org, 250, 0, 0, 0)
	// Same key in another org is a different grant.
	other := newOrg(t)
	if g3, err := svc.Grant(ctx, other, p); err != nil || g3.ID == g1.ID {
		t.Fatalf("chave é por organização: %+v %v", g3, err)
	}
	// Without a key every call grants.
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantBonus, Amount: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantBonus, Amount: 10}); err != nil {
		t.Fatal(err)
	}
	Invariants(t, svc, org, 270, 0, 0, 0)
}

func testExpiry(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 100, ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantTopup, Amount: 50, ExpiresAt: &future}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantBonus, Amount: 25}); err != nil {
		t.Fatal(err)
	}
	// A hold of 130 covers the whole expired grant and part of the next:
	// only 20 of the 100 expired credits can be retired right now.
	hold, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 130})
	if err != nil {
		t.Fatal(err)
	}
	exp, err := svc.ExpireGrants(ctx, org, now)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Expired != 45 || exp.Grants != 0 || exp.Deferred != 55 {
		t.Fatalf("expiração parcial inesperada: %+v", exp)
	}
	Invariants(t, svc, org, 175, 0, 45, 130)
	// Settling consumes the expired grant first (earliest expiry), then the rest.
	st, err := svc.Settle(ctx, org, hold.ID, 60)
	if err != nil || st.Debited != 60 {
		t.Fatalf("Settle: %+v %v", st, err)
	}
	Invariants(t, svc, org, 175, 60, 45, 0)
	exp, err = svc.ExpireGrants(ctx, org, now)
	if err != nil {
		t.Fatal(err)
	}
	// The expired grant had 55 left; 60 were consumed from it first, so nothing remains to expire.
	if exp.Expired != 0 || exp.Deferred != 0 {
		t.Fatalf("segunda passada: %+v", exp)
	}
	// Nothing expires before its time; everything expires once due.
	if exp, err := svc.ExpireGrants(ctx, org, now); err != nil || exp.Expired != 0 {
		t.Fatalf("nada vencido: %+v %v", exp, err)
	}
	exp, err = svc.ExpireGrants(ctx, org, future.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if exp.Expired != 45 || exp.Grants != 1 {
		t.Fatalf("recarga vencida deveria expirar inteira: %+v", exp)
	}
	Invariants(t, svc, org, 175, 60, 90, 0)
	acct, _ := svc.Balance(ctx, org)
	if acct.Balance != 25 {
		t.Fatalf("só o bônus sem validade deveria restar: %+v", acct)
	}
}

func testParallelReserve(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 500}); err != nil {
		t.Fatal(err)
	}
	const callers = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	var holds []uuid.UUID
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 10})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
				holds = append(holds, h.ID)
			case errors.Is(err, credits.ErrInsufficient):
				refused++
			default:
				t.Errorf("erro inesperado: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 50 || refused != 50 {
		t.Fatalf("500 créditos / 100 reservas de 10: %d aceitas, %d recusadas", ok, refused)
	}
	Invariants(t, svc, org, 500, 0, 0, 500)
	// Parallel settle/release of every hold keeps the books straight.
	for i, id := range holds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = svc.Settle(ctx, org, id, 7)
			} else {
				_, err = svc.Release(ctx, org, id)
			}
			if err != nil {
				t.Errorf("fechar reserva: %v", err)
			}
		}()
	}
	wg.Wait()
	Invariants(t, svc, org, 500, 25*7, 0, 0)
}

// failingTx wraps a Tx and fails the named method, simulating a crash
// between two writes of the same operation. With lie=true the method
// returns a plausible but wrong answer instead of an error, which exercises
// the service's own consistency checks.
type failingTx struct {
	credits.Tx
	failOn string
	lie    bool
}

var errInjected = errors.New("falha injetada")

// txMethods lists every Tx method the wrapper can fail.
var txMethods = []string{
	"Account", "AccountForUpdate", "UpdateAccount", "GrantByIdempotencyKey", "InsertGrant",
	"OpenGrantsForUpdate", "ExpiredGrantsForUpdate", "SetGrantRemaining", "MarkGrantExpired",
	"InsertHold", "HoldForUpdate", "CloseHold", "AppendLedger", "RecentLedger",
}

func (f *failingTx) hit(name string) bool { return f.failOn == name && !f.lie }

func (f *failingTx) Account(ctx context.Context, orgID uuid.UUID) (credits.Account, bool, error) {
	if f.hit("Account") {
		return credits.Account{}, false, errInjected
	}
	return f.Tx.Account(ctx, orgID)
}

func (f *failingTx) AccountForUpdate(ctx context.Context, orgID uuid.UUID) (credits.Account, error) {
	if f.hit("AccountForUpdate") {
		return credits.Account{}, errInjected
	}
	if f.failOn == "AccountForUpdate" && f.lie {
		return credits.Account{OrgID: orgID}, nil // claims an empty account
	}
	return f.Tx.AccountForUpdate(ctx, orgID)
}

func (f *failingTx) UpdateAccount(ctx context.Context, orgID uuid.UUID, balance, held int64) (credits.Account, error) {
	if f.hit("UpdateAccount") {
		return credits.Account{}, errInjected
	}
	return f.Tx.UpdateAccount(ctx, orgID, balance, held)
}

func (f *failingTx) GrantByIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (credits.Grant, bool, error) {
	if f.hit("GrantByIdempotencyKey") {
		return credits.Grant{}, false, errInjected
	}
	return f.Tx.GrantByIdempotencyKey(ctx, orgID, key)
}

func (f *failingTx) InsertGrant(ctx context.Context, g credits.Grant) (credits.Grant, error) {
	if f.hit("InsertGrant") {
		return credits.Grant{}, errInjected
	}
	return f.Tx.InsertGrant(ctx, g)
}

func (f *failingTx) OpenGrantsForUpdate(ctx context.Context, orgID uuid.UUID) ([]credits.Grant, error) {
	if f.hit("OpenGrantsForUpdate") {
		return nil, errInjected
	}
	if f.failOn == "OpenGrantsForUpdate" && f.lie {
		return nil, nil // claims there are no grants to consume
	}
	return f.Tx.OpenGrantsForUpdate(ctx, orgID)
}

func (f *failingTx) ExpiredGrantsForUpdate(ctx context.Context, orgID uuid.UUID, now time.Time) ([]credits.Grant, error) {
	if f.hit("ExpiredGrantsForUpdate") {
		return nil, errInjected
	}
	return f.Tx.ExpiredGrantsForUpdate(ctx, orgID, now)
}

func (f *failingTx) SetGrantRemaining(ctx context.Context, grantID uuid.UUID, remaining int64) error {
	if f.hit("SetGrantRemaining") {
		return errInjected
	}
	return f.Tx.SetGrantRemaining(ctx, grantID, remaining)
}

func (f *failingTx) MarkGrantExpired(ctx context.Context, grantID uuid.UUID, remaining int64, at time.Time) error {
	if f.hit("MarkGrantExpired") {
		return errInjected
	}
	return f.Tx.MarkGrantExpired(ctx, grantID, remaining, at)
}

func (f *failingTx) InsertHold(ctx context.Context, h credits.Hold) (credits.Hold, error) {
	if f.hit("InsertHold") {
		return credits.Hold{}, errInjected
	}
	return f.Tx.InsertHold(ctx, h)
}

func (f *failingTx) HoldForUpdate(ctx context.Context, orgID, holdID uuid.UUID) (credits.Hold, bool, error) {
	if f.hit("HoldForUpdate") {
		return credits.Hold{}, false, errInjected
	}
	return f.Tx.HoldForUpdate(ctx, orgID, holdID)
}

func (f *failingTx) CloseHold(ctx context.Context, orgID, holdID uuid.UUID, status credits.HoldStatus, settled *int64) (credits.Hold, error) {
	if f.hit("CloseHold") {
		return credits.Hold{}, errInjected
	}
	return f.Tx.CloseHold(ctx, orgID, holdID, status, settled)
}

func (f *failingTx) AppendLedger(ctx context.Context, e credits.LedgerEntry) (credits.LedgerEntry, error) {
	if f.hit("AppendLedger") {
		return credits.LedgerEntry{}, errInjected
	}
	return f.Tx.AppendLedger(ctx, e)
}

func (f *failingTx) RecentLedger(ctx context.Context, orgID uuid.UUID, limit int) ([]credits.LedgerEntry, error) {
	if f.hit("RecentLedger") {
		return nil, errInjected
	}
	return f.Tx.RecentLedger(ctx, orgID, limit)
}

type failingStore struct {
	credits.Store
	failOn string
	lie    bool
}

func (f *failingStore) InOrgTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context, tx credits.Tx) error) error {
	return f.Store.InOrgTx(ctx, orgID, func(ctx context.Context, tx credits.Tx) error {
		return fn(ctx, &failingTx{Tx: tx, failOn: f.failOn, lie: f.lie})
	})
}

// state is the observable account state used to detect partial writes.
type state struct {
	balance, held int64
	ledger        int
}

func observe(t *testing.T, svc *credits.Service, org uuid.UUID) state {
	t.Helper()
	acct, err := svc.Balance(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := svc.RecentLedger(context.Background(), org, 10000)
	if err != nil {
		t.Fatal(err)
	}
	return state{balance: acct.Balance, held: acct.Held, ledger: len(rows)}
}

// testEveryFailurePoint fails each Tx method in turn and runs every
// operation: the injected error must surface unchanged and the state must
// be exactly what it was before the call. Operations that do not use the
// failing method succeed and move the baseline.
func testEveryFailurePoint(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	ctx := context.Background()
	healthy := credits.New(store)
	for _, method := range txMethods {
		t.Run(method, func(t *testing.T) {
			org := newOrg(t)
			if _, err := healthy.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 1000}); err != nil {
				t.Fatal(err)
			}
			past := time.Now().UTC().Add(-time.Hour)
			if _, err := healthy.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantBonus, Amount: 30, ExpiresAt: &past}); err != nil {
				t.Fatal(err)
			}
			hold, err := healthy.Reserve(ctx, org, credits.ReserveParams{Amount: 1010})
			if err != nil {
				t.Fatal(err)
			}
			hold2, err := healthy.Reserve(ctx, org, credits.ReserveParams{Amount: 5})
			if err != nil {
				t.Fatal(err)
			}
			broken := credits.New(&failingStore{Store: store, failOn: method})
			ops := []struct {
				name string
				call func() error
			}{
				{"Balance", func() error { _, err := broken.Balance(ctx, org); return err }},
				{"Grant", func() error {
					_, err := broken.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantTopup, Amount: 7, IdempotencyKey: "k-" + method})
					return err
				}},
				{"Reserve", func() error { _, err := broken.Reserve(ctx, org, credits.ReserveParams{Amount: 3}); return err }},
				{"ExpireGrants", func() error { _, err := broken.ExpireGrants(ctx, org, time.Now().UTC()); return err }},
				{"Settle", func() error { _, err := broken.Settle(ctx, org, hold.ID, 20); return err }},
				{"Release", func() error { _, err := broken.Release(ctx, org, hold2.ID); return err }},
				{"RecentLedger", func() error { _, err := broken.RecentLedger(ctx, org, 5); return err }},
			}
			for _, op := range ops {
				before := observe(t, healthy, org)
				err := op.call()
				switch {
				case err == nil:
					continue // method not on this operation's path
				case errors.Is(err, errInjected):
					if after := observe(t, healthy, org); after != before {
						t.Fatalf("%s falhando em %s: estado mudou de %+v para %+v", op.name, method, before, after)
					}
				default:
					t.Fatalf("%s falhando em %s: erro inesperado %v", op.name, method, err)
				}
			}
		})
	}
}

// testInconsistentStore makes the store lie (empty account, no grants) and
// expects the service to refuse rather than corrupt the books.
func testInconsistentStore(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	ctx := context.Background()
	healthy := credits.New(store)
	org := newOrg(t)
	if _, err := healthy.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	hold, err := healthy.Reserve(ctx, org, credits.ReserveParams{Amount: 40})
	if err != nil {
		t.Fatal(err)
	}
	before := observe(t, healthy, org)
	noGrants := credits.New(&failingStore{Store: store, failOn: "OpenGrantsForUpdate", lie: true})
	if _, err := noGrants.Settle(ctx, org, hold.ID, 10); !errors.Is(err, credits.ErrInconsistent) {
		t.Fatalf("Settle sem concessões deveria ser inconsistente: %v", err)
	}
	emptyAccount := credits.New(&failingStore{Store: store, failOn: "AccountForUpdate", lie: true})
	if _, err := emptyAccount.Settle(ctx, org, hold.ID, 10); !errors.Is(err, credits.ErrInconsistent) {
		t.Fatalf("Settle com conta vazia deveria ser inconsistente: %v", err)
	}
	if _, err := emptyAccount.Release(ctx, org, hold.ID); !errors.Is(err, credits.ErrInconsistent) {
		t.Fatalf("Release com conta vazia deveria ser inconsistente: %v", err)
	}
	if after := observe(t, healthy, org); after != before {
		t.Fatalf("estado mudou: %+v → %+v", before, after)
	}
}

func testCrashSafety(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	ctx := context.Background()
	org := newOrg(t)
	healthy := credits.New(store)
	if _, err := healthy.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 300}); err != nil {
		t.Fatal(err)
	}
	hold, err := healthy.Reserve(ctx, org, credits.ReserveParams{Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := healthy.Balance(ctx, org)

	for _, failOn := range []string{"AppendLedger", "CloseHold", "UpdateAccount"} {
		broken := credits.New(&failingStore{Store: store, failOn: failOn})
		if failOn != "CloseHold" { // Grant and Reserve never close holds
			if _, err := broken.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantBonus, Amount: 50}); !errors.Is(err, errInjected) {
				t.Fatalf("%s: Grant deveria falhar com a falha injetada: %v", failOn, err)
			}
			if _, err := broken.Reserve(ctx, org, credits.ReserveParams{Amount: 50}); !errors.Is(err, errInjected) {
				t.Fatalf("%s: Reserve deveria falhar: %v", failOn, err)
			}
			if _, err := broken.ExpireGrants(ctx, org, time.Now().Add(time.Hour)); err != nil && !errors.Is(err, errInjected) {
				t.Fatalf("%s: ExpireGrants deveria falhar com a falha injetada ou não ter nada a expirar: %v", failOn, err)
			}
		}
		if _, err := broken.Settle(ctx, org, hold.ID, 10); !errors.Is(err, errInjected) {
			t.Fatalf("%s: Settle deveria falhar: %v", failOn, err)
		}
		if _, err := broken.Release(ctx, org, hold.ID); !errors.Is(err, errInjected) {
			t.Fatalf("%s: Release deveria falhar: %v", failOn, err)
		}
		after, _ := healthy.Balance(ctx, org)
		if after.Balance != before.Balance || after.Held != before.Held {
			t.Fatalf("%s: estado mudou apesar da falha: antes %+v depois %+v", failOn, before, after)
		}
		rows, _ := healthy.RecentLedger(ctx, org, 10)
		if len(rows) != 2 {
			t.Fatalf("%s: lançamentos parciais ficaram gravados: %d", failOn, len(rows))
		}
	}
	// The hold is still open and usable after all the failed attempts.
	if st, err := healthy.Settle(ctx, org, hold.ID, 100); err != nil || st.Debited != 100 {
		t.Fatalf("reserva deveria continuar aberta: %+v %v", st, err)
	}
	Invariants(t, healthy, org, 300, 100, 0, 0)
}

func testClosedHolds(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	h, _ := svc.Reserve(ctx, org, credits.ReserveParams{Amount: 40})
	if _, err := svc.Settle(ctx, org, h.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Settle(ctx, org, h.ID, 10); !errors.Is(err, credits.ErrHoldClosed) {
		t.Fatalf("liquidar duas vezes: %v", err)
	}
	if _, err := svc.Release(ctx, org, h.ID); !errors.Is(err, credits.ErrHoldClosed) {
		t.Fatalf("liberar após liquidar: %v", err)
	}
	if _, err := svc.Settle(ctx, org, uuid.New(), 1); !errors.Is(err, credits.ErrHoldNotFound) {
		t.Fatalf("reserva inexistente: %v", err)
	}
	if _, err := svc.Release(ctx, org, uuid.New()); !errors.Is(err, credits.ErrHoldNotFound) {
		t.Fatalf("liberar inexistente: %v", err)
	}
	Invariants(t, svc, org, 100, 10, 0, 0)
}

func testRandomSequence(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	org := newOrg(t)
	rng := rand.New(rand.NewPCG(42, 7)) //nolint:gosec // deterministic test sequence
	var granted, settled, expired, openHeld int64
	open := map[uuid.UUID]int64{}
	steps := 300
	if testing.Short() {
		steps = 80
	}
	for i := range steps {
		switch rng.IntN(5) {
		case 0:
			amt := int64(rng.IntN(200) + 1)
			var exp *time.Time
			if rng.IntN(3) == 0 {
				e := time.Now().UTC().Add(-time.Minute)
				exp = &e
			}
			if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantTopup, Amount: amt, ExpiresAt: exp}); err != nil {
				t.Fatal(err)
			}
			granted += amt
		case 1, 2:
			amt := int64(rng.IntN(150) + 1)
			h, err := svc.Reserve(ctx, org, credits.ReserveParams{Amount: amt})
			if errors.Is(err, credits.ErrInsufficient) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			open[h.ID] = amt
			openHeld += amt
		case 3:
			for id, amt := range open {
				actual := int64(rng.IntN(int(amt) + 50))
				st, err := svc.Settle(ctx, org, id, actual)
				if err != nil {
					t.Fatal(err)
				}
				settled += st.Debited
				openHeld -= amt
				delete(open, id)
				break
			}
		case 4:
			if rng.IntN(2) == 0 {
				for id, amt := range open {
					if _, err := svc.Release(ctx, org, id); err != nil {
						t.Fatal(err)
					}
					openHeld -= amt
					delete(open, id)
					break
				}
			} else {
				exp, err := svc.ExpireGrants(ctx, org, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				expired += exp.Expired
			}
		}
		if i%10 == 0 {
			Invariants(t, svc, org, granted, settled, expired, openHeld)
		}
	}
	Invariants(t, svc, org, granted, settled, expired, openHeld)
}

func testIsolation(t *testing.T, factory Factory) {
	store, newOrg := factory(t)
	svc := credits.New(store)
	ctx := context.Background()
	a, b := newOrg(t), newOrg(t)
	if _, err := svc.Grant(ctx, a, credits.GrantParams{Kind: credits.GrantPlan, Amount: 100}); err != nil {
		t.Fatal(err)
	}
	ha, err := svc.Reserve(ctx, a, credits.ReserveParams{Amount: 10})
	if err != nil {
		t.Fatal(err)
	}
	if acct, err := svc.Balance(ctx, b); err != nil || acct.Balance != 0 {
		t.Fatalf("org B deveria estar vazia: %+v %v", acct, err)
	}
	if _, err := svc.Settle(ctx, b, ha.ID, 1); !errors.Is(err, credits.ErrHoldNotFound) {
		t.Fatalf("org B não pode liquidar reserva de A: %v", err)
	}
	if _, err := svc.Release(ctx, b, ha.ID); !errors.Is(err, credits.ErrHoldNotFound) {
		t.Fatalf("org B não pode liberar reserva de A: %v", err)
	}
	if rows, err := svc.RecentLedger(ctx, b, 10); err != nil || len(rows) != 0 {
		t.Fatalf("razão de B deveria estar vazio: %d %v", len(rows), err)
	}
	Invariants(t, svc, a, 100, 0, 0, 10)
}
