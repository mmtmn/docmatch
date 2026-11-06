//go:build integration

package apikeys

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }
func TestPaidKeysLifecycle(t *testing.T) {
	d := testdb.Get(t)
	ctx := t.Context()
	catalog, err := account.LoadEntitlements("config/pricing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []string{"starter", "pro", "business"} {
		t.Run(plan, func(t *testing.T) {
			org, user := uuid.New(), uuid.New()
			p := session.Principal{OrgID: org, UserID: user}
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := d.Admin.Exec(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO orgs(id,name,slug,plan_code) VALUES($1,'Keys',$2,$3)`, org, org.String(), plan)
			exec(`INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, user, user.String()+"@example.test")
			exec(`INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, org, user)
			cfg := &config.Config{}
			cfg.Signing.APIKeyPepper = config.Secret("test-only-pepper")
			cfg.Billing.StripeSecretKey = config.Secret("sk_live_fixture")
			s := New(cfg, catalog, d.App)
			v := CreateRequest{Label: "Server", Scopes: []string{"runs:read"}}
			if _, err := s.Create(ctx, p, v); err == nil {
				t.Fatal("unpaid account created key")
			}
			exec(`INSERT INTO subscriptions(org_id,plan_code,status,livemode,current_period_end) VALUES($1,$2,'active',true,now()+interval '1 month')`, org, plan)
			k, err := s.Create(ctx, p, v)
			if err != nil {
				t.Fatal(err)
			}
			id, err := parse(k.Secret)
			if err != nil {
				t.Fatal(err)
			}
			var stored []byte
			if err = d.Admin.QueryRow(ctx, `SELECT key_hash FROM api_keys WHERE id=$1`, id).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(stored), k.Secret) {
				t.Fatal("stored plaintext secret")
			}
			resolve := func(method, path, secret string) error {
				r := httptest.NewRequest(method, path, nil)
				r.Header.Set("Authorization", "Bearer "+secret)
				p, err := s.Resolve(apix.Unauthenticated())(r)
				if err == nil && (p.UserID != nil || p.APIKeyID == nil || p.OrgID != org) {
					t.Fatal("wrong principal")
				}
				return err
			}
			if err = resolve("GET", "/v1/runs", k.Secret); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/v1/runs/estimate", "/v1/billing/checkout", "/v1/api-keys"} {
				if err = resolve("POST", path, k.Secret); !errors.Is(err, apix.ErrScopeDenied) {
					t.Fatalf("scope bypass %s: %v", path, err)
				}
			}
			if err = resolve("GET", "/v1/runs", k.Secret[:77]+"!"); !errors.Is(err, apix.ErrInvalidAPIKey) {
				t.Fatal("invalid key accepted")
			}
			exec(`UPDATE subscriptions SET status='past_due' WHERE org_id=$1`, org)
			if err = resolve("GET", "/v1/runs", k.Secret); err == nil {
				t.Fatal("inactive subscription used key")
			}
			exec(`UPDATE subscriptions SET status='active' WHERE org_id=$1`, org)
			other := session.Principal{UserID: user, OrgID: uuid.New()}
			if err = s.Revoke(ctx, other, id); err == nil {
				t.Fatal("cross tenant revoke")
			}
			if err = s.Revoke(ctx, p, id); err != nil {
				t.Fatal(err)
			}
			if err = resolve("GET", "/v1/runs", k.Secret); !errors.Is(err, apix.ErrInvalidAPIKey) {
				t.Fatal("revoked key accepted")
			}
			ent, _ := catalog.For(plan)
			var successes atomic.Int64
			var wg sync.WaitGroup
			for range ent.APIKeys + 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := s.Create(ctx, p, v)
					if err == nil {
						successes.Add(1)
						return
					}
					var e *apix.Error
					if !errors.As(err, &e) || e.Code != contracts.ErrAPIKeysLimitReached {
						t.Errorf("create: %v", err)
					}
				}()
			}
			wg.Wait()
			if int(successes.Load()) != ent.APIKeys {
				t.Fatalf("quota race: %d != %d", successes.Load(), ent.APIKeys)
			}
		})
	}
}
