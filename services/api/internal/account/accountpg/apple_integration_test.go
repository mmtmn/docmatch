//go:build integration

package accountpg

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/applecredentials"
	"github.com/mmtmn/docmatch/services/api/internal/auth"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

func TestAppleDeletionRevokesWithoutKeepingUserOrBlockingErasure(t *testing.T) {
	d := getDB(t)
	ctx := t.Context()
	keys, _ := crypto.NewKeyring(bytes.Repeat([]byte{8}, 32))
	accounts := New(d.App, nil)
	user, _ := signup(t, accounts, keys, uuid.NewString()+"@example.com", auth.ProviderApple, uuid.NewString())
	failed := true
	calls := 0
	grants := applecredentials.New(d.App, keys, func(_ context.Context, client, token string) error {
		calls++
		if client != "web" || token != "latest-refresh" {
			t.Error("wrong grant")
		}
		if failed {
			return errors.New("provider unavailable")
		}
		return nil
	}, logx.Discard())
	if err := grants.Save(ctx, user.ID, "web", "old-refresh"); err != nil {
		t.Fatal(err)
	}
	if err := grants.Save(ctx, user.ID, "web", "latest-refresh"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.App.QueryRow(ctx, `SELECT count(*) FROM apple_credentials`).Scan(&count); err != nil || count != 0 {
		t.Fatal("credential visible outside system transaction", count, err)
	}
	var sealed []byte
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM apple_credentials WHERE user_id=$1`, user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT sealed_token FROM apple_credentials WHERE user_id=$1`, user.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("latest-refresh")) {
		t.Fatal("plaintext token stored")
	}
	if _, err := crypto.Open(keys.Key(crypto.PurposeAppleToken), sealed, []byte("other-row")); err == nil {
		t.Fatal("ciphertext not bound to its row")
	}
	if err := grants.Sweep(ctx); err != nil || calls != 0 {
		t.Fatal("active grant revoked", err)
	}
	if ok, err := accounts.RequestDeletion(ctx, user.ID, time.Now().Add(-time.Second)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := grants.Save(ctx, user.ID, "ios", "late-login"); err == nil {
		t.Fatal("late login resurrected grant")
	}
	if err := grants.Sweep(ctx); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
	rep, err := accounts.Erase(ctx, user.ID, time.Now())
	if err != nil || !rep.Erased {
		t.Fatal(rep, err)
	}
	if _, found, err := accounts.UserByID(ctx, user.ID); err != nil || found {
		t.Fatal("user retained", err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM apple_credentials WHERE user_id IS NULL AND attempts=1`).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry lost", count, err)
	}
	if err := grants.Sweep(ctx); err != nil || calls != 1 {
		t.Fatal("retry backoff ignored", calls, err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE apple_credentials SET next_attempt_at=now() WHERE user_id IS NULL`); err != nil {
		t.Fatal(err)
	}
	failed = false
	if err := grants.Sweep(ctx); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM apple_credentials`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked token retained", count, err)
	}
}

func TestAppleRevocationExpiresAndLegacyAccountsStillDelete(t *testing.T) {
	d := getDB(t)
	ctx := t.Context()
	keys, _ := crypto.NewKeyring(make([]byte, 32))
	accounts := New(d.App, nil)
	for _, withGrant := range []bool{false, true} {
		user, _ := signup(t, accounts, keys, uuid.NewString()+"@example.com", auth.ProviderApple, uuid.NewString())
		grants := applecredentials.New(d.App, keys, nil, logx.Discard())
		if withGrant {
			if err := grants.Save(ctx, user.ID, "web", "refresh-token"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := accounts.RequestDeletion(ctx, user.ID, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if rep, err := accounts.Erase(ctx, user.ID, time.Now()); err != nil || !rep.Erased {
			t.Fatal(rep, err)
		}
		if _, err := d.Admin.Exec(ctx, `UPDATE apple_credentials SET expires_at=now()-interval '1 second' WHERE user_id IS NULL`); err != nil {
			t.Fatal(err)
		}
		if err := grants.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM apple_credentials`).Scan(&n); err != nil || n != 0 {
			t.Fatal("expired grant retained", n, err)
		}
	}
}

func TestAnonymousAppleGrantRetriesAcrossWorkerInstances(t *testing.T) {
	d := getDB(t)
	ctx := t.Context()
	keys, _ := crypto.NewKeyring(make([]byte, 32))
	grants := applecredentials.New(d.App, keys, nil, logx.Discard())
	if err := grants.QueueRevocation(ctx, "native-app", "unused-refresh"); err != nil {
		t.Fatal(err)
	}
	if err := grants.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := d.Admin.QueryRow(ctx, `SELECT attempts FROM apple_credentials WHERE client_id='native-app' AND user_id IS NULL`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal(attempts, err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE apple_credentials SET next_attempt_at=now() WHERE client_id='native-app'`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	restarted := applecredentials.New(d.App, keys, func(_ context.Context, client, token string) error {
		calls++
		if client != "native-app" || token != "unused-refresh" {
			t.Error("grant changed")
		}
		return nil
	}, logx.Discard())
	if err := restarted.Sweep(ctx); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
}
