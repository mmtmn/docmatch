package projects

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

var documentRole = regexp.MustCompile(`^([a-z][a-z0-9_]{0,39})?$`)

type AttachRequest struct {
	UploadID   string  `json:"upload_id"`
	ReplacesID *string `json:"replaces_id,omitempty"`
	Role       string  `json:"role,omitempty"`
}

// attach accepts only an upload owned by the caller (managers can import legacy
// uploads). Unique account/upload ownership prevents attaching a private file to
// a different project, even if two requests race.
func attach(ctx context.Context, tx pgx.Tx, p apix.Principal, project uuid.UUID, req AttachRequest) (uuid.UUID, error) {
	v, _, err := access(ctx, tx, p, project, false)
	if err != nil {
		return uuid.Nil, err
	}
	if !v.Capabilities.Upload || v.ArchivedAt != nil {
		return uuid.Nil, apix.ErrForbidden
	}
	if !documentRole.MatchString(req.Role) {
		return uuid.Nil, apix.Validation(apix.FieldError{Path: "/role", Code: "invalid"})
	}
	upload, err := publicid.Decode("upl", req.UploadID)
	if err != nil {
		return uuid.Nil, missing()
	}
	var owns bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(($3 OR user_id=$4),false) FROM uploads WHERE org_id=$1 AND id=$2 AND deleted_at IS NULL`, p.OrgID, upload, v.Capabilities.ManageAccount, p.UserID).Scan(&owns)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, missing()
	}
	if err != nil {
		return uuid.Nil, err
	}
	if !owns {
		return uuid.Nil, missing()
	}
	var existing, existingProject uuid.UUID
	var oldRole string
	err = tx.QueryRow(ctx, `SELECT id,project_id,role FROM project_documents WHERE org_id=$1 AND upload_id=$2`, p.OrgID, upload).Scan(&existing, &existingProject, &oldRole)
	if err == nil {
		if existingProject != project {
			return uuid.Nil, missing()
		}
		if oldRole != req.Role {
			return uuid.Nil, conflict()
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	var replaces *uuid.UUID
	if req.ReplacesID != nil {
		if !v.Capabilities.Reconcile {
			return uuid.Nil, apix.ErrForbidden
		}
		var ready bool
		if err := tx.QueryRow(ctx, `SELECT status='clean' AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>now()) FROM uploads WHERE org_id=$1 AND id=$2`, p.OrgID, upload).Scan(&ready); err != nil {
			return uuid.Nil, err
		}
		if !ready {
			return uuid.Nil, apix.E(contracts.ErrUploadNotReady, nil)
		}
		previous, err := publicid.Decode("doc", *req.ReplacesID)
		if err != nil {
			return uuid.Nil, missing()
		}
		result, err := tx.Exec(ctx, `UPDATE project_documents SET removed_at=now() WHERE org_id=$1 AND project_id=$2 AND id=$3 AND removed_at IS NULL`, p.OrgID, project, previous)
		if err != nil {
			return uuid.Nil, err
		}
		if result.RowsAffected() == 0 {
			return uuid.Nil, conflict()
		}
		replaces = &previous
	}
	id := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO project_documents(id,org_id,project_id,upload_id,replaces_id,role,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, p.OrgID, project, upload, replaces, req.Role, p.UserID)
	if db.IsUniqueViolation(err) {
		return uuid.Nil, conflict()
	}
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET revision=revision+1,version=version+1,updated_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, project); err != nil {
		return uuid.Nil, err
	}
	return id, event(ctx, tx, p, project, "document.added")
}

func (s *Service) Attach(ctx context.Context, p apix.Principal, project uuid.UUID, req AttachRequest) (string, error) {
	var id uuid.UUID
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = attach(ctx, tx, p, project, req)
		return err
	})
	if err != nil {
		return "", err
	}
	return publicid.Encode("doc", id), nil
}

// ReplaceDocument promotes an already scanned upload in this project and keeps
// the old document as history. Both IDs and the project version must still match.
func (s *Service) ReplaceDocument(ctx context.Context, p apix.Principal, project, previous, replacement uuid.UUID, version int64) error {
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, project, true)
		if err != nil {
			return err
		}
		if v.Version != version {
			return conflict()
		}
		if previous == replacement {
			return conflict()
		}
		var role string
		err = tx.QueryRow(ctx, `SELECT role FROM project_documents WHERE org_id=$1 AND project_id=$2 AND id=$3 AND removed_at IS NULL AND pending_replacement_id IS NULL`, p.OrgID, project, previous).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE project_documents d SET replaces_id=$3,role=$5,pending_replacement_id=NULL FROM uploads u
 WHERE d.org_id=$1 AND d.project_id=$2 AND d.id=$4 AND d.removed_at IS NULL AND d.replaces_id IS NULL AND (d.pending_replacement_id IS NULL OR d.pending_replacement_id=$3)
 AND u.org_id=d.org_id AND u.id=d.upload_id AND u.status='clean' AND u.deleted_at IS NULL AND (u.expires_at IS NULL OR u.expires_at>now())`, p.OrgID, project, previous, replacement, role)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return conflict()
		}
		if _, err := tx.Exec(ctx, `UPDATE project_documents SET removed_at=now() WHERE org_id=$1 AND project_id=$2 AND id=$3`, p.OrgID, project, previous); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET version=version+1,revision=revision+1,updated_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, project); err != nil {
			return err
		}
		return event(ctx, tx, p, project, "document.replaced")
	})
}

func (s *Service) RemoveDocument(ctx context.Context, p apix.Principal, project, document uuid.UUID, version int64) error {
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, project, true)
		if err != nil {
			return err
		}
		if v.Version != version {
			return conflict()
		}
		result, err := tx.Exec(ctx, `UPDATE project_documents SET removed_at=now() WHERE org_id=$1 AND project_id=$2 AND id=$3 AND removed_at IS NULL`, p.OrgID, project, document)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return missing()
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET revision=revision+1,version=version+1,updated_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, project); err != nil {
			return err
		}
		return event(ctx, tx, p, project, "document.removed")
	})
}

type UploadRequest struct {
	uploads.TicketRequest
	ReplacesID string `json:"replaces_id,omitempty"`
}

func (s *Service) CreateUpload(ctx context.Context, p apix.Principal, project uuid.UUID, req UploadRequest) (any, error) {
	if s.uploads == nil {
		return nil, apix.E(contracts.ErrServiceUnavailable, nil)
	}
	var ticket any
	err := db.WithOrgUnit(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, project, false)
		if err != nil {
			return err
		}
		if !v.Capabilities.Upload || v.ArchivedAt != nil {
			return apix.ErrForbidden
		}
		var upload uploads.Upload
		ticket, upload, err = s.uploads.CreateTicket(ctx, p, req.TicketRequest)
		if err != nil {
			return err
		}

		document, err := attach(ctx, tx, p, project, AttachRequest{UploadID: upload.PublicID(), Role: req.Role})
		if err != nil || req.ReplacesID == "" {
			return err
		}
		if !v.Capabilities.Reconcile {
			return apix.ErrForbidden
		}
		previous, err := publicid.Decode("doc", req.ReplacesID)
		if err != nil {
			return missing()
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_documents WHERE org_id=$1 AND project_id=$2 AND id=$3 AND removed_at IS NULL AND pending_replacement_id IS NULL)`, p.OrgID, project, previous).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return missing()
		}
		if _, err = tx.Exec(ctx, `UPDATE project_documents SET pending_replacement_id=$3 WHERE org_id=$1 AND id=$2`, p.OrgID, document, previous); err != nil {
			return err
		}
		// A replacement waiting for verification is not an analysis input yet.
		_, err = tx.Exec(ctx, `UPDATE projects SET revision=revision-1 WHERE org_id=$1 AND id=$2`, p.OrgID, project)
		return err
	})
	return ticket, err
}

func (s *Service) CompleteUpload(ctx context.Context, p apix.Principal, project, upload uuid.UUID, req uploads.CompleteRequest) (uploads.View, bool, error) {
	if s.uploads == nil {
		return uploads.View{}, false, apix.E(contracts.ErrServiceUnavailable, nil)
	}
	err := db.WithOrgUnit(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, project, false)
		if err != nil {
			return err
		}
		if !v.Capabilities.Upload || v.ArchivedAt != nil {
			return apix.ErrForbidden
		}
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_documents WHERE org_id=$1 AND project_id=$2 AND upload_id=$3 AND removed_at IS NULL AND ($4 OR created_by=$5))`, p.OrgID, project, upload, v.Capabilities.ManageAccount, p.UserID).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return missing()
		}
		return nil
	})
	if err != nil {
		return uploads.View{}, false, err
	}
	u, done, err := s.uploads.Complete(ctx, p, upload, req)
	return u.View(time.Now()), done, err
}
