package customizations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/entitlement"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

type Planner func(context.Context, string, Manifest) (Manifest, error)
type Service struct {
	cfg     *config.Config
	catalog *account.Catalog
	pool    *pgxpool.Pool
	planner Planner
}

func New(cfg *config.Config, catalog *account.Catalog, pool *pgxpool.Pool) *Service {
	return &Service{cfg, catalog, pool, NewPlanner(cfg)}
}

type Access struct {
	Eligible   bool `json:"eligible"`
	CanRequest bool `json:"can_request"`
	Limit      int  `json:"limit"`
	Used       int  `json:"used"`
}
type Version struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}
type Workspace struct {
	Manifest  Manifest            `json:"manifest"`
	Version   int                 `json:"version"`
	Access    Access              `json:"access"`
	Endpoints []map[string]string `json:"endpoints"`
	History   []Version           `json:"history"`
}
type Request struct {
	ID          string    `json:"id"`
	Request     string    `json:"request"`
	Status      string    `json:"status"`
	Manifest    *Manifest `json:"manifest,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	BaseVersion int       `json:"base_version"`
	Credits     int       `json:"credits"`
	CreatedAt   time.Time `json:"created_at"`
}

func manager(ctx context.Context, tx pgx.Tx, p session.Principal) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=$1 AND m.user_id=$2 AND m.role IN ('owner','admin') AND u.email_verified_at IS NOT NULL AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL)`, p.OrgID, p.UserID).Scan(&ok)
	return ok, err
}
func (s *Service) access(ctx context.Context, tx pgx.Tx, p session.Principal) (Access, error) {
	plan, eligible, err := entitlement.Paid(ctx, tx, s.cfg, p.OrgID)
	if err != nil {
		return Access{}, err
	}
	ent, ok := s.catalog.For(plan)
	if !ok {
		return Access{}, apix.ErrForbidden
	}
	var exempt bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT token_exempt FROM credit_accounts WHERE org_id=$1),false)`, p.OrgID).Scan(&exempt); err != nil {
		return Access{}, err
	}
	if exempt || s.cfg.IsDev() {
		ent.UICustomizationsPerMonth = -1
	}
	manage, err := manager(ctx, tx, p)
	if err != nil {
		return Access{}, err
	}
	var used int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM customizations WHERE org_id=$1 AND created_at>=date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AND status!='failed'`, p.OrgID).Scan(&used)
	return Access{eligible, eligible && manage && (ent.UICustomizationsPerMonth < 0 || used < ent.UICustomizationsPerMonth), ent.UICustomizationsPerMonth, used}, err
}
func current(ctx context.Context, tx pgx.Tx, org uuid.UUID) (Manifest, int, error) {
	var data []byte
	var version int
	err := tx.QueryRow(ctx, `SELECT tokens,version FROM tenant_themes WHERE org_id=$1`, org).Scan(&data, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Default(), 0, nil
	}
	if err != nil {
		return Manifest{}, 0, err
	}
	m, err := decodeManifest(data)
	return m, version, err
}
func record(ctx context.Context, tx pgx.Tx, p session.Principal, action, id string, meta any) error {
	_, err := audit.New(pgstore.NewAuditStoreTx(tx)).Record(ctx, &p.OrgID, audit.Actor{Type: audit.ActorUser, ID: p.UserID.String()}, action, audit.Target{Type: "customization", ID: id}, meta)
	return err
}
func lock(ctx context.Context, tx pgx.Tx, org uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "workspace:"+org.String())
	return err
}
func (s *Service) Get(ctx context.Context, p session.Principal) (Workspace, error) {
	out := Workspace{History: []Version{}, Endpoints: endpoints()}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out.Access, err = s.access(ctx, tx, p)
		if err != nil {
			return err
		}
		out.Manifest, out.Version, err = current(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT version,created_at FROM workspace_versions WHERE org_id=$1 ORDER BY version DESC LIMIT 30`, p.OrgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Version
			if err := rows.Scan(&v.Version, &v.CreatedAt); err != nil {
				return err
			}
			out.History = append(out.History, v)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Service) Propose(ctx context.Context, p session.Principal, request string, key uuid.UUID) (Request, error) {
	request = strings.TrimSpace(request)
	if n := utf8.RuneCountInString(request); n < 3 || n > 2000 {
		return Request{}, apix.E(contracts.ErrValidationFailed, nil)
	}
	id := uuid.New()
	out := Request{ID: publicid.Encode("cst", id), Request: request, Status: "pending"}
	var base Manifest
	var duplicate bool
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lock(ctx, tx, p.OrgID); err != nil {
			return err
		}
		var existing uuid.UUID
		var original string
		err := tx.QueryRow(ctx, `SELECT id,request FROM customizations WHERE org_id=$1 AND request_key=$2`, p.OrgID, key).Scan(&existing, &original)
		if err == nil {
			if original != request {
				return apix.E(contracts.ErrCustomizationNotApprovable, nil)
			}
			id = existing
			duplicate = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		a, err := s.access(ctx, tx, p)
		if err != nil {
			return err
		}
		if !a.Eligible {
			return apix.E(contracts.ErrSubscriptionRequired, nil)
		}
		if !a.CanRequest {
			return apix.E(contracts.ErrCustomizationRefused, nil).With(map[string]any{"reason": "Limite mensal atingido ou perfil sem permissão para personalizar."})
		}
		// Persistent rate limit also covers failures and exempt/unlimited accounts.
		var recent int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM customizations WHERE org_id=$1 AND created_at>now()-interval '1 hour'`, p.OrgID).Scan(&recent)
		if err != nil {
			return err
		}
		if recent >= 10 {
			return apix.E(contracts.ErrRateLimited, nil)
		}
		base, out.BaseVersion, err = current(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO customizations(id,org_id,user_id,request,base_version,request_key) VALUES($1,$2,$3,$4,$5,$6) RETURNING created_at`, id, p.OrgID, p.UserID, request, out.BaseVersion, key).Scan(&out.CreatedAt)
		if err != nil {
			return err
		}
		return record(ctx, tx, p, "customization.requested", out.ID, map[string]any{"base_version": out.BaseVersion})
	})
	if err != nil {
		return out, err
	}
	if duplicate {
		return s.Request(ctx, p, id)
	}
	planned, planErr := s.planner(ctx, request, base)
	out.Status = "planned"
	if planErr != nil {
		out.Status = "rejected"
		out.Reason = "O pedido precisa usar os controles disponíveis: tema, densidade, nome dos achados, agrupamento, ordenação ou formato de exportação. Tente um exemplo abaixo."
		if errors.Is(planErr, errPlannerUnavailable) {
			out.Status = "failed"
			out.Reason = "O modelo está indisponível agora. Tente novamente ou use um dos exemplos; este pedido não conta no limite mensal."
		}
	} else if err := planned.Validate(); err != nil {
		out.Status = "rejected"
		out.Reason = "A proposta não passou na validação. Tente um pedido mais específico."
	} else if planned == base {
		out.Status = "rejected"
		out.Reason = "Este pedido não altera as preferências atuais."
	} else {
		out.Manifest = &planned
	}
	// Persist completion even if the browser closes while the provider responds.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = db.WithOrgTx(saveCtx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		result, _ := json.Marshal(map[string]string{"reason": out.Reason})
		var data []byte
		if out.Manifest != nil {
			data, _ = json.Marshal(out.Manifest)
		}
		_, err := tx.Exec(ctx, `UPDATE customizations SET status=$3,plan=$4,result=$5 WHERE id=$1 AND org_id=$2`, id, p.OrgID, out.Status, data, result)
		if err != nil {
			return err
		}
		return record(ctx, tx, p, "customization."+out.Status, out.ID, map[string]any{"base_version": out.BaseVersion})
	})
	return out, err
}
func (s *Service) Request(ctx context.Context, p session.Principal, id uuid.UUID) (Request, error) {
	out := Request{ID: publicid.Encode("cst", id)}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var plan, result []byte
		err := tx.QueryRow(ctx, `SELECT request,status,plan,result,base_version,created_at FROM customizations WHERE id=$1 AND org_id=$2`, id, p.OrgID).Scan(&out.Request, &out.Status, &plan, &result, &out.BaseVersion, &out.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return apix.E(contracts.ErrCustomizationNotFound, nil)
		}
		if err != nil {
			return err
		}
		if len(plan) > 0 && string(plan) != "null" {
			var m Manifest
			if err := json.Unmarshal(plan, &m); err != nil {
				return err
			}
			out.Manifest = &m
		}
		var why struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(result, &why)
		out.Reason = why.Reason
		return nil
	})
	return out, err
}
func (s *Service) Apply(ctx context.Context, p session.Principal, id uuid.UUID, rollback int) (Workspace, error) {
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lock(ctx, tx, p.OrgID); err != nil {
			return err
		}
		a, err := s.access(ctx, tx, p)
		if err != nil {
			return err
		}
		manage, err := manager(ctx, tx, p)
		if err != nil {
			return err
		}
		if !manage {
			return apix.ErrForbidden
		}
		if !a.Eligible {
			return apix.E(contracts.ErrSubscriptionRequired, nil)
		}
		_, version, err := current(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		var data []byte
		var requestID any
		action := "customization.applied"
		if rollback > 0 {
			err = tx.QueryRow(ctx, `SELECT manifest FROM workspace_versions WHERE org_id=$1 AND version=$2`, p.OrgID, rollback).Scan(&data)
			if errors.Is(err, pgx.ErrNoRows) {
				return apix.E(contracts.ErrCustomizationNothingToRollback, nil)
			}
			if err != nil {
				return err
			}
			action = "customization.rolled_back"
		} else {
			var status string
			var base int
			err = tx.QueryRow(ctx, `SELECT status,plan,base_version FROM customizations WHERE id=$1 AND org_id=$2 FOR UPDATE`, id, p.OrgID).Scan(&status, &data, &base)
			if errors.Is(err, pgx.ErrNoRows) {
				return apix.E(contracts.ErrCustomizationNotFound, nil)
			}
			if err != nil {
				return err
			}
			if status == "applied" {
				return nil
			}
			if status != "planned" || base != version {
				return apix.E(contracts.ErrCustomizationNotApprovable, nil)
			}
			requestID = id
		}
		if _, err := decodeManifest(data); err != nil {
			return apix.E(contracts.ErrCustomizationRefused, err)
		}
		meta, _ := json.Marshal(endpoints())
		if version == 0 { // Version 1 is the default, so the first deployment is reversible.
			defaults, _ := json.Marshal(Default())
			_, err = tx.Exec(ctx, `INSERT INTO workspace_versions(org_id,version,manifest,endpoints,created_by) VALUES($1,1,$2,$3,$4)`, p.OrgID, defaults, meta, p.UserID)
			if err != nil {
				return err
			}
			version = 1
		}
		version++
		_, err = tx.Exec(ctx, `INSERT INTO workspace_versions(org_id,version,manifest,endpoints,customization_id,created_by) VALUES($1,$2,$3,$4,$5,$6)`, p.OrgID, version, data, meta, requestID, p.UserID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO tenant_themes(org_id,tokens,version,endpoints) VALUES($1,$2,$3,$4) ON CONFLICT(org_id) DO UPDATE SET tokens=EXCLUDED.tokens,version=EXCLUDED.version,endpoints=EXCLUDED.endpoints,css=NULL,updated_at=now()`, p.OrgID, data, version, meta)
		if err != nil {
			return err
		}
		if requestID != nil {
			_, err = tx.Exec(ctx, `UPDATE customizations SET status='applied',applied_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, id)
			if err != nil {
				return err
			}
		}
		return record(ctx, tx, p, action, publicid.Encode("cst", id), map[string]any{"version": version, "restored_version": rollback})
	})
	if err != nil {
		return Workspace{}, err
	}
	return s.Get(ctx, p)
}
func (s *Service) Mount(r *httpx.Router) {
	r.Handle("GET", "/v1/workspace", httpx.BodyNone, session.RequireFunc(s.get))
	r.Handle("POST", "/v1/customizations", httpx.BodyJSON, session.RequireFunc(s.propose))
	r.Handle("GET", "/v1/customizations/{id}", httpx.BodyNone, session.RequireFunc(s.request))
	r.Handle("POST", "/v1/customizations/{id}/approve", httpx.BodyJSON, session.RequireFunc(s.apply))
	r.Handle("POST", "/v1/workspace/rollback", httpx.BodyJSON, session.RequireFunc(s.rollback))
}
func respond(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, 200, v)
}
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	v, e := s.Get(r.Context(), p)
	respond(w, r, v, e)
}
func (s *Service) propose(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Request string `json:"request"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil {
		apix.Handle(w, r, apix.E(contracts.ErrValidationFailed, nil))
		return
	}
	p, _ := session.FromContext(r.Context())
	v, e := s.Propose(r.Context(), p, body.Request, key)
	respond(w, r, v, e)
}
func (s *Service) request(w http.ResponseWriter, r *http.Request) {
	id, err := publicid.Decode("cst", r.PathValue("id"))
	if err != nil {
		apix.Handle(w, r, apix.E(contracts.ErrCustomizationNotFound, nil))
		return
	}
	p, _ := session.FromContext(r.Context())
	v, e := s.Request(r.Context(), p, id)
	respond(w, r, v, e)
}
func (s *Service) apply(w http.ResponseWriter, r *http.Request) {
	id, err := publicid.Decode("cst", r.PathValue("id"))
	if err != nil {
		apix.Handle(w, r, apix.E(contracts.ErrCustomizationNotFound, nil))
		return
	}
	p, _ := session.FromContext(r.Context())
	v, e := s.Apply(r.Context(), p, id, 0)
	respond(w, r, v, e)
}
func (s *Service) rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version int `json:"version"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	if body.Version < 1 {
		apix.Handle(w, r, apix.E(contracts.ErrValidationFailed, nil))
		return
	}
	p, _ := session.FromContext(r.Context())
	v, e := s.Apply(r.Context(), p, uuid.Nil, body.Version)
	respond(w, r, v, e)
}
