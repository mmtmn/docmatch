package projects

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

type Service struct {
	pool    *pgxpool.Pool
	auth    apix.Resolver
	uploads *uploads.Service
	engine  *Engine
}

func New(pool *pgxpool.Pool, auth apix.Resolver, uploadSvc *uploads.Service) *Service {
	if auth == nil {
		auth = apix.Unauthenticated()
	}
	return &Service{pool: pool, auth: auth, uploads: uploadSvc}
}

func member(ctx context.Context, tx pgx.Tx, p apix.Principal) (string, error) {
	if p.UserID != nil {
		return tenancy.Check(ctx, tx, p.OrgID, *p.UserID)
	}
	if p.APIKeyID != nil {
		return "admin", nil
	} // resolver already checked the key and its creator
	return "", apix.ErrUnauthenticated
}

func missing() error { return apix.E(contracts.ErrNotFound, nil) }
func conflict() error {
	return apix.E(contracts.ErrConflict, nil).With(map[string]any{"reason": "O projeto mudou. Atualize antes de salvar."})
}
func validName(s string) bool {
	return utf8.RuneCountInString(s) >= 1 && utf8.RuneCountInString(s) <= contracts.TextProjectName && strings.IndexFunc(s, unicode.IsControl) < 0
}

func cleanName(s string) string {
	v, err := httpx.NormalizeLine(s)
	if err != nil {
		return ""
	}
	return v
}

func validateSettings(v Settings) error {
	if utf8.RuneCountInString(v.RulesText) > contracts.TextRulesText || len(v.RuleIDs) > contracts.IDsProjectRulesMax {
		return apix.Validation(apix.FieldError{Path: "/settings", Code: "too_long"})
	}
	if v.LLMMode != "" && v.LLMMode != "off" && v.LLMMode != "openrouter" && v.LLMMode != "local" {
		return apix.Validation(apix.FieldError{Path: "/settings/llm_mode", Code: "invalid"})
	}
	for _, id := range v.RuleIDs {
		if _, err := publicid.Decode("rul", id); err != nil {
			return apix.Validation(apix.FieldError{Path: "/settings/rule_ids", Code: "invalid"})
		}
	}
	return nil
}

// access must run inside the transaction performing the action. Only a manager,
// the creator, or an explicitly shared member sees a project; a missing and a
// private project deliberately have the same response.
func access(ctx context.Context, tx pgx.Tx, p apix.Principal, id uuid.UUID, edit bool) (Project, string, error) {
	role, err := member(ctx, tx, p)
	if err != nil {
		return Project{}, "", err
	}
	var out Project
	var data []byte
	err = tx.QueryRow(ctx, `SELECT name,visibility,version,revision,settings,owner_id,created_at,updated_at,archived_at
 FROM projects WHERE org_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, p.OrgID, id).
		Scan(&out.Name, &out.Visibility, &out.Version, &out.Revision, &data, &out.OwnerID, &out.CreatedAt, &out.UpdatedAt, &out.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, "", missing()
	}
	if err != nil {
		return Project{}, "", err
	}
	out.Capabilities = tenancy.Permissions(role)
	allowed := out.Capabilities.ManageAccount || out.Visibility == "team" || p.UserID != nil && out.OwnerID != nil && *p.UserID == *out.OwnerID
	if !allowed && p.UserID != nil {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_members WHERE org_id=$1 AND project_id=$2 AND user_id=$3)`, p.OrgID, id, *p.UserID).Scan(&allowed)
		if err != nil {
			return Project{}, "", err
		}
	}
	if !allowed {
		return Project{}, "", missing()
	}
	if edit && (!out.Capabilities.Reconcile || out.ArchivedAt != nil) {
		return Project{}, "", apix.ErrForbidden
	}
	out.ID = publicid.Encode(Prefix, id)
	if out.Capabilities.ReadResults {
		out.Settings = &Settings{}
		if err = json.Unmarshal(data, out.Settings); err != nil {
			return Project{}, "", err
		}
	}
	return out, role, nil
}

func event(ctx context.Context, tx pgx.Tx, p apix.Principal, id uuid.UUID, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO project_events(org_id,project_id,actor_id,action) VALUES($1,$2,$3,$4)`, p.OrgID, id, p.UserID, action)
	return err
}

func (s *Service) Create(ctx context.Context, p apix.Principal, name string) (Project, error) {
	name = cleanName(name)
	if !validName(name) {
		return Project{}, apix.Validation(apix.FieldError{Path: "/name", Code: "invalid", Max: contracts.TextProjectName})
	}
	id := uuid.New()
	var out Project
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		role, err := member(ctx, tx, p)
		if err != nil {
			return err
		}
		if !tenancy.Permissions(role).CreateProject {
			return apix.ErrForbidden
		}
		if _, err = tx.Exec(ctx, `INSERT INTO projects(id,org_id,owner_id,name) VALUES($1,$2,$3,$4)`, id, p.OrgID, p.UserID, name); err != nil {
			return err
		}
		if err = event(ctx, tx, p, id, "project.created"); err != nil {
			return err
		}
		out, _, err = access(ctx, tx, p, id, false)
		return err
	})
	return out, err
}

func (s *Service) List(ctx context.Context, p apix.Principal, archived bool, limit, offset int) ([]Project, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, apix.Validation(apix.FieldError{Path: "/limit", Code: "invalid"})
	}
	out := []Project{}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		role, err := member(ctx, tx, p)
		if err != nil {
			return err
		}
		cap := tenancy.Permissions(role)
		rows, err := tx.Query(ctx, `SELECT p.id,p.name,p.visibility,p.version,p.revision,p.created_at,p.updated_at,p.archived_at,
 (SELECT count(*) FROM project_documents d WHERE d.org_id=p.org_id AND d.project_id=p.id AND d.removed_at IS NULL AND d.pending_replacement_id IS NULL)
 FROM projects p WHERE p.org_id=$1 AND p.deleted_at IS NULL AND (p.archived_at IS NOT NULL)=$4
 AND ($3 OR p.owner_id=$2 OR p.visibility='team' OR EXISTS(SELECT 1 FROM project_members m WHERE m.org_id=p.org_id AND m.project_id=p.id AND m.user_id=$2))
 ORDER BY p.updated_at DESC,p.id LIMIT $5 OFFSET $6`, p.OrgID, p.UserID, cap.ManageAccount, archived, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Project
			var id uuid.UUID
			if err := rows.Scan(&id, &v.Name, &v.Visibility, &v.Version, &v.Revision, &v.CreatedAt, &v.UpdatedAt, &v.ArchivedAt, &v.DocumentCount); err != nil {
				return err
			}
			v.ID = publicid.Encode(Prefix, id)
			v.Capabilities = cap
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) Get(ctx context.Context, p apix.Principal, id uuid.UUID) (Detail, error) {
	out := Detail{Documents: []Document{}}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, role, err := access(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		out.Project = v
		if v.Capabilities.ManageAccount {
			rows, err := tx.Query(ctx, `SELECT user_id FROM project_members WHERE org_id=$1 AND project_id=$2 ORDER BY user_id`, p.OrgID, id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var user uuid.UUID
				if err := rows.Scan(&user); err != nil {
					rows.Close()
					return err
				}
				out.SharedWith = append(out.SharedWith, publicid.Encode("usr", user))
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT d.id,d.upload_id,d.replaces_id,u.file_name,d.role,
 CASE WHEN u.deleted_at IS NOT NULL OR u.expires_at<=now() OR (u.status='pending' AND u.ticket_expires_at<=now()) THEN 'expired'
 WHEN u.status='clean' THEN 'ready' WHEN u.status IN ('pending','scanning') THEN u.status ELSE 'rejected' END,
 u.size_bytes,d.created_at,u.expires_at,d.removed_at,d.pending_replacement_id
 FROM project_documents d JOIN uploads u ON u.org_id=d.org_id AND u.id=d.upload_id
 WHERE d.org_id=$1 AND d.project_id=$2 AND ($3 OR d.created_by=$4)
 ORDER BY d.created_at,d.id`, p.OrgID, id, role != "uploader", p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var doc Document
			var did, uid uuid.UUID
			var replaces, pending *uuid.UUID
			if err := rows.Scan(&did, &uid, &replaces, &doc.FileName, &doc.Role, &doc.Status, &doc.SizeBytes, &doc.CreatedAt, &doc.ExpiresAt, &doc.RemovedAt, &pending); err != nil {
				return err
			}
			doc.ID = publicid.Encode("doc", did)
			doc.UploadID = publicid.Encode("upl", uid)
			if replaces != nil {
				v := publicid.Encode("doc", *replaces)
				doc.ReplacesID = &v
			}
			if pending != nil {
				v := publicid.Encode("doc", *pending)
				doc.PendingReplacementID = &v
			}
			if doc.RemovedAt == nil && pending == nil {
				out.DocumentCount++
			}
			out.Documents = append(out.Documents, doc)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) Update(ctx context.Context, p apix.Principal, id uuid.UUID, patch Patch) (Detail, error) {
	if patch.Name != nil {
		v := cleanName(*patch.Name)
		patch.Name = &v
		if !validName(v) {
			return Detail{}, apix.Validation(apix.FieldError{Path: "/name", Code: "invalid", Max: contracts.TextProjectName})
		}
	}
	if patch.Settings != nil {
		text, err := httpx.ValidateText(patch.Settings.RulesText, contracts.TextRulesText, false)
		if err != nil {
			return Detail{}, apix.Validation(apix.FieldError{Path: "/settings/rules_text", Code: "invalid", Max: contracts.TextRulesText})
		}
		patch.Settings.RulesText = text
		if err := validateSettings(*patch.Settings); err != nil {
			return Detail{}, err
		}
	}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		if !v.Capabilities.Reconcile {
			return apix.ErrForbidden
		}
		if patch.Version != v.Version {
			return conflict()
		}
		if patch.Name == nil && patch.Archived == nil && patch.Settings == nil {
			return apix.Validation(apix.FieldError{Path: "/", Code: "required"})
		}
		var settings []byte
		if patch.Settings != nil {
			settings, err = json.Marshal(patch.Settings)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE projects SET name=COALESCE($3,name),
 revision=revision+CASE WHEN $4::jsonb IS NOT NULL AND settings IS DISTINCT FROM $4::jsonb THEN 1 ELSE 0 END,
 settings=COALESCE($4::jsonb,settings),archived_at=CASE WHEN $5::boolean IS NULL THEN archived_at WHEN $5 THEN COALESCE(archived_at,now()) ELSE NULL END,
 version=version+1,updated_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, id, patch.Name, settings, patch.Archived)
		if err != nil {
			return err
		}
		return event(ctx, tx, p, id, "project.updated")
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, p, id)
}

func (s *Service) Delete(ctx context.Context, p apix.Principal, id uuid.UUID, version int64) error {
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		if !v.Capabilities.Reconcile {
			return apix.ErrForbidden
		}
		if v.Version != version {
			return conflict()
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET deleted_at=now(),version=version+1 WHERE org_id=$1 AND id=$2`, p.OrgID, id); err != nil {
			return err
		}
		return event(ctx, tx, p, id, "project.deleted")
	})
}

func (s *Service) Share(ctx context.Context, p apix.Principal, id uuid.UUID, version int64, visibility string, users []uuid.UUID) error {
	if visibility != "private" && visibility != "team" || len(users) > contracts.IDsProjectMembersMax {
		return apix.Validation(apix.FieldError{Path: "/visibility", Code: "invalid"})
	}
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		if !v.Capabilities.ManageAccount {
			return apix.ErrForbidden
		}
		if v.Version != version {
			return conflict()
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_members WHERE org_id=$1 AND project_id=$2`, p.OrgID, id); err != nil {
			return err
		}
		for _, user := range users {
			if _, err := tenancy.Check(ctx, tx, p.OrgID, user); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO project_members(org_id,project_id,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.OrgID, id, user); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET visibility=$3,version=version+1,updated_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, id, visibility); err != nil {
			return err
		}
		return event(ctx, tx, p, id, "project.sharing_changed")
	})
}

func (s *Service) Activity(ctx context.Context, p apix.Principal, id uuid.UUID) ([]Event, error) {
	out := []Event{}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,action,created_at FROM project_events WHERE org_id=$1 AND project_id=$2 AND ($3 OR actor_id=$4) ORDER BY id DESC LIMIT 100`, p.OrgID, id, v.Capabilities.ReadResults, p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Event
			if err := rows.Scan(&v.ID, &v.Action, &v.CreatedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func respond(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, http.StatusOK, v)
}
