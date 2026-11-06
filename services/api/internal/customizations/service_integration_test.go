//go:build integration

package customizations

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }
func TestWorkspaceIsolationApprovalAndRollback(t *testing.T) {
	d := testdb.Get(t)
	ctx := t.Context()
	cat, err := account.LoadEntitlements("config/pricing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.LLM.Mode = "off"
	cfg.Billing.StripeSecretKey = config.Secret("sk_live_fixture")
	s := New(cfg, cat, d.App)
	makeOrg := func(plan string, paid, exempt bool) session.Principal {
		p := session.Principal{OrgID: uuid.New(), UserID: uuid.New()}
		for _, query := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO orgs(id,name,slug,plan_code) VALUES($1,'Workspace',$2,$3)`, []any{p.OrgID, p.OrgID.String(), plan}},
			{`INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, []any{p.UserID, p.UserID.String() + "@example.test"}},
			{`INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, []any{p.OrgID, p.UserID}},
			{`INSERT INTO credit_accounts(org_id,token_exempt) VALUES($1,$2)`, []any{p.OrgID, exempt}},
		} {
			if _, err := d.Admin.Exec(ctx, query.sql, query.args...); err != nil {
				t.Fatal(err)
			}
		}
		if paid {
			_, err := d.Admin.Exec(ctx, `INSERT INTO subscriptions(org_id,plan_code,status,livemode,current_period_end) VALUES($1,$2,'active',true,now()+interval '1 month')`, p.OrgID, plan)
			if err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	unpaid := makeOrg("starter", false, false)
	if _, err := s.Propose(ctx, unpaid, "Usar tema escuro", uuid.New()); err == nil {
		t.Fatal("unpaid customization")
	}
	qa := makeOrg("business", false, true)
	key := uuid.New()
	first, err := s.Propose(ctx, qa, "Usar tema escuro", key)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "planned" || first.Manifest.Mode != "dark" {
		t.Fatal("not planned")
	}
	again, err := s.Propose(ctx, qa, "Usar tema escuro", key)
	if err != nil || again.ID != first.ID {
		t.Fatal("idempotency failed", err)
	}
	if _, err := s.Propose(ctx, qa, "Usar tema claro", key); err == nil {
		t.Fatal("key reused with different body")
	}
	stale, err := s.Propose(ctx, qa, "Chamar achados de pendências", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := publicid.Decode("cst", first.ID)
	staleID, _ := publicid.Decode("cst", stale.ID)
	if _, err := s.Apply(ctx, unpaid, id, 0); err == nil {
		t.Fatal("tenant bypass")
	}
	before, _ := s.Get(ctx, qa)
	if before.Manifest.Mode != "system" {
		t.Fatal("applied without approval")
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Apply(ctx, qa, id, 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	applied, err := s.Get(ctx, qa)
	if err != nil || applied.Version != 2 || applied.Manifest.Mode != "dark" {
		t.Fatal("apply failed", err)
	}
	if _, err := s.Apply(ctx, qa, staleID, 0); err == nil {
		t.Fatal("stale preview applied")
	}
	restored, err := s.Apply(ctx, qa, uuid.Nil, 1)
	if err != nil || restored.Version != 3 || restored.Manifest.Mode != "system" {
		t.Fatal("rollback failed", err)
	}
	if _, err := s.Request(ctx, unpaid, id); err == nil {
		t.Fatal("request leaked")
	}
	// RLS denies even a direct query outside the service's explicit predicates.
	err = db.WithOrgTx(ctx, d.App, unpaid.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM workspace_versions WHERE org_id=$1`, qa.OrgID).Scan(&n)
		if n != 0 {
			return errors.New("RLS leak")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []string{"starter", "pro", "business"} {
		p := makeOrg(plan, true, false)
		if _, err := s.Propose(ctx, p, "Usar tema escuro", uuid.New()); err != nil {
			t.Fatal(plan, err)
		}
		if plan == "starter" {
			if _, err := s.Propose(ctx, p, "Usar tema claro", uuid.New()); err == nil {
				t.Fatal("monthly quota bypass")
			}
		}
	}

	// Provider outages do not consume the only monthly request of the entry plan.
	retryOrg := makeOrg("starter", true, false)
	s.planner = func(context.Context, string, Manifest) (Manifest, error) { return Manifest{}, errPlannerUnavailable }
	unavailable, err := s.Propose(ctx, retryOrg, "Personalizar meu espaço", uuid.New())
	if err != nil || unavailable.Status != "failed" {
		t.Fatal("provider failure", err)
	}
	s.planner = NewPlanner(cfg)
	if _, err := s.Propose(ctx, retryOrg, "Usar tema escuro", uuid.New()); err != nil {
		t.Fatal("failed request consumed monthly quota", err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE credit_accounts SET billing_blocked=true WHERE org_id=$1`, qa.OrgID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, qa, uuid.Nil, 1); err == nil {
		t.Fatal("blocked workspace changed")
	}
}
