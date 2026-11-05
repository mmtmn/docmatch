package uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
)

// Errors.
var (
	ErrUploadNotFound = errors.New("uploads: upload não encontrado")
	ErrNotScanning    = errors.New("uploads: upload não está em verificação")
)

// DefaultTicketTTL is how long a presigned ticket stays valid.
const DefaultTicketTTL = time.Hour

// Recorder appends audit entries (audit.Service).
type Recorder interface {
	Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error)
}

// Deps are the collaborators of the service.
type Deps struct {
	Store   Store
	Stores  storage.Stores
	Limits  *limits.Limits
	Pricing *pricing.Pricing
	Scanner ScanRunner
	Jobs    jobargs.Enqueuer
	Audit   Recorder
	Logger  *slog.Logger
	Now     func() time.Time
	// TicketTTL overrides DefaultTicketTTL.
	TicketTTL time.Duration
}

// Service implements the upload flow.
type Service struct {
	d Deps
}

// New validates the dependencies and returns the service.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Stores.Documents == nil || d.Stores.Media == nil || d.Pricing == nil || d.Jobs == nil {
		return nil, errors.New("uploads: store, stores, pricing e jobs são obrigatórios")
	}
	if d.Limits == nil {
		d.Limits = limits.Must()
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.TicketTTL <= 0 {
		d.TicketTTL = DefaultTicketTTL
	}
	if d.Scanner == nil {
		d.Scanner = QueueScanner{}
	}
	return &Service{d: d}, nil
}

// TicketRequest is openapi UploadRequest.
type TicketRequest struct {
	Kind      Kind   `json:"kind"`
	FileName  string `json:"file_name"`
	SizeBytes int64  `json:"size_bytes"`
	Mime      string `json:"mime"`
	SHA256    string `json:"sha256,omitempty"`
	Role      string `json:"role,omitempty"`
}

// DocumentTicket is openapi DocumentUploadTicket.
type DocumentTicket struct {
	ID           string            `json:"id"`
	Kind         Kind              `json:"kind"`
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Fields       map[string]string `json:"fields"`
	ExpiresAt    string            `json:"expires_at"`
	MaxSizeBytes int64             `json:"max_size_bytes"`
}

// MediaTicket is openapi MediaUploadTicket.
type MediaTicket struct {
	ID           string            `json:"id"`
	Kind         Kind              `json:"kind"`
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	ExpiresAt    string            `json:"expires_at"`
	MaxSizeBytes int64             `json:"max_size_bytes"`
}

var (
	mimeRe = regexp.MustCompile(`^[a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+-]+$`)
	roleRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
)

// ValidateFileName applies the contract: NFC, no control characters, no
// path separators, at most files.uploaded_file_name_bytes bytes.
func ValidateFileName(name string, maxBytes int) (string, *apix.Error) {
	n, err := httpx.NormalizeLine(name)
	if err != nil {
		return "", apix.E(contracts.ErrUploadRejectedName, err)
	}
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\") || len(n) > maxBytes {
		return "", apix.Ef(contracts.ErrUploadRejectedName, "nome inválido ou longo demais")
	}
	return n, nil
}

// MaxSize is the per-file ceiling for a kind under a plan.
func (s *Service) MaxSize(kind Kind, plan pricing.Plan) int64 {
	if kind == KindMedia {
		return s.d.Limits.Files.MediaFileMax()
	}
	limit := s.d.Limits.Files.DocumentFileMax()
	if planMax := plan.DocumentFileMB << 20; planMax > 0 && planMax < limit {
		return planMax
	}
	return limit
}

// CreateTicket validates the request against limits and the plan, records
// the pending upload and returns the presigned ticket.
func (s *Service) CreateTicket(ctx context.Context, p apix.Principal, req TicketRequest) (any, Upload, error) {
	plan, ok := s.d.Pricing.Plan(p.Plan)
	if !ok {
		return nil, Upload{}, apix.Ef(contracts.ErrPlanUnknown, "plano %q", p.Plan)
	}
	if req.Kind != KindDocument && req.Kind != KindMedia {
		return nil, Upload{}, apix.Validation(apix.FieldError{Path: "/kind", Code: "invalid"})
	}
	name, aerr := ValidateFileName(req.FileName, s.d.Limits.Files.UploadedFileNameBytes)
	if aerr != nil {
		return nil, Upload{}, aerr
	}
	if req.SizeBytes < 1 {
		return nil, Upload{}, apix.Validation(apix.FieldError{Path: "/size_bytes", Code: "min"})
	}
	maxSize := s.MaxSize(req.Kind, plan)
	if req.SizeBytes > maxSize {
		return nil, Upload{}, apix.Ef(contracts.ErrFileTooLarge, "%d > %d", req.SizeBytes, maxSize).With(map[string]any{"max_size_bytes": maxSize})
	}
	mime := strings.ToLower(strings.TrimSpace(req.Mime))
	if !mimeRe.MatchString(mime) {
		return nil, Upload{}, apix.Validation(apix.FieldError{Path: "/mime", Code: "invalid"})
	}
	if !AcceptedMime(req.Kind, mime) {
		return nil, Upload{}, apix.Ef(contracts.ErrUploadRejectedType, "tipo %q", mime)
	}
	var sum []byte
	if req.SHA256 != "" {
		if !sha256Re.MatchString(req.SHA256) {
			return nil, Upload{}, apix.Validation(apix.FieldError{Path: "/sha256", Code: "invalid"})
		}
		sum, _ = hex.DecodeString(req.SHA256)
	}
	if req.Role != "" && !roleRe.MatchString(req.Role) {
		return nil, Upload{}, apix.Validation(apix.FieldError{Path: "/role", Code: "invalid"})
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, Upload{}, err
	}
	now := s.d.Now().UTC()
	ticketExpiry := now.Add(s.d.TicketTTL)
	retention := now.Add(time.Duration(plan.RetentionDays) * 24 * time.Hour)
	u := Upload{
		ID: id, OrgID: p.OrgID, UserID: p.UserID, Kind: req.Kind, Role: req.Role, FileName: name,
		DeclaredMime: mime, SizeBytes: req.SizeBytes, SHA256: sum, Status: StatusPending,
		CreatedAt: now, TicketExpiry: &ticketExpiry, ExpiresAt: &retention,
	}
	var ticket any
	switch req.Kind {
	case KindDocument:
		st := s.d.Stores.Documents
		u.Store, u.StorageKey = st.Name(), storage.QuarantineKey(p.OrgID, id)
		post, err := st.PresignPost(ctx, u.StorageKey, maxSize, mime, s.d.TicketTTL)
		if err != nil {
			return nil, Upload{}, apix.E(contracts.ErrStorageUnavailable, err)
		}
		ticket = DocumentTicket{ID: u.PublicID(), Kind: KindDocument, Method: "POST", URL: post.URL, Fields: post.Fields, ExpiresAt: ticketExpiry.Format(time.RFC3339), MaxSizeBytes: maxSize}
	default:
		st := s.d.Stores.Media
		u.Store, u.StorageKey = st.Name(), storage.MediaKey(p.OrgID, id)
		put, err := st.PresignPut(ctx, u.StorageKey, req.SizeBytes, mime, s.d.TicketTTL)
		if err != nil {
			return nil, Upload{}, apix.E(contracts.ErrStorageUnavailable, err)
		}
		ticket = MediaTicket{ID: u.PublicID(), Kind: KindMedia, Method: "PUT", URL: put.URL, Headers: put.Headers, ExpiresAt: ticketExpiry.Format(time.RFC3339), MaxSizeBytes: maxSize}
	}
	created, err := s.d.Store.CreateUpload(ctx, u)
	if err != nil {
		return nil, Upload{}, fmt.Errorf("uploads: gravar ticket: %w", err)
	}
	s.record(ctx, p.OrgID, p.Actor(), "upload.create", created, map[string]any{"kind": created.Kind, "size_bytes": created.SizeBytes, "store": created.Store})
	return ticket, created, nil
}

// CompleteRequest is openapi UploadCompleteRequest.
type CompleteRequest struct {
	SHA256 string `json:"sha256,omitempty"`
	ETag   string `json:"etag,omitempty"`
}

// Complete confirms the object exists, verifies its size and checksum and
// hands it to the scanner. done=false means the verification is still
// running (202); done=true means the upload already has a final state (200).
func (s *Service) Complete(ctx context.Context, p apix.Principal, id uuid.UUID, req CompleteRequest) (u Upload, done bool, err error) {
	u, err = s.get(ctx, p.OrgID, id)
	if err != nil {
		return Upload{}, false, err
	}
	switch u.Status {
	case StatusScanning:
		return u, false, nil
	case StatusClean, StatusRejected, StatusInfected, StatusDeleted:
		return u, true, nil
	}
	if req.SHA256 != "" && !sha256Re.MatchString(req.SHA256) {
		return Upload{}, false, apix.Validation(apix.FieldError{Path: "/sha256", Code: "invalid"})
	}
	now := s.d.Now().UTC()
	st := s.storeFor(u)
	obj, exists, err := st.Head(ctx, u.StorageKey)
	if err != nil {
		return Upload{}, false, apix.E(contracts.ErrStorageUnavailable, err)
	}
	if !exists {
		if u.TicketExpiry != nil && now.After(*u.TicketExpiry) {
			return Upload{}, false, apix.E(contracts.ErrUploadExpired, nil)
		}
		return Upload{}, false, apix.E(contracts.ErrUploadIncomplete, nil)
	}
	plan, _ := s.d.Pricing.Plan(p.Plan)
	if obj.Size > s.MaxSize(u.Kind, plan) || obj.Size < 1 {
		u.Status, u.RejectedRsn, u.CompletedAt = StatusRejected, ReasonSize, &now
		u, err = s.d.Store.UpdateUpload(ctx, u)
		if err != nil {
			return Upload{}, false, err
		}
		s.record(ctx, p.OrgID, p.Actor(), "upload.reject", u, map[string]any{"reason": ReasonSize, "size_bytes": obj.Size})
		return u, true, nil
	}
	u.SizeBytes = obj.Size

	expected := req.SHA256
	if expected == "" && len(u.SHA256) == 32 {
		expected = hex.EncodeToString(u.SHA256)
	}
	sum, err := s.checksum(ctx, st, u.StorageKey, obj.Size)
	if err != nil {
		return Upload{}, false, apix.E(contracts.ErrStorageUnavailable, err)
	}
	if expected != "" && expected != sum {
		u.Status, u.RejectedRsn, u.CompletedAt = StatusRejected, ReasonChecksum, &now
		if _, err := s.d.Store.UpdateUpload(ctx, u); err != nil {
			return Upload{}, false, err
		}
		s.record(ctx, p.OrgID, p.Actor(), "upload.reject", u, map[string]any{"reason": ReasonChecksum})
		return Upload{}, false, apix.Ef(contracts.ErrUploadChecksumMismatch, "hash informado difere do objeto")
	}
	u.SHA256, _ = hex.DecodeString(sum)
	u.Status, u.CompletedAt = StatusScanning, &now
	u, err = s.d.Store.UpdateUpload(ctx, u)
	if err != nil {
		return Upload{}, false, err
	}
	if err := s.d.Jobs.Enqueue(ctx, jobargs.ScanArgs{OrgID: u.OrgID, UploadID: u.ID}); err != nil {
		return Upload{}, false, fmt.Errorf("uploads: enfileirar verificação: %w", err)
	}
	s.record(ctx, p.OrgID, p.Actor(), "upload.complete", u, map[string]any{"size_bytes": u.SizeBytes})
	return u, false, nil
}

// checksum streams the object through SHA-256, bounded by size.
func (s *Service) checksum(ctx context.Context, st storage.Store, key string, size int64) (string, error) {
	rc, err := st.Open(ctx, key)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(rc, size+1))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("uploads: objeto com %d bytes, esperado %d", n, size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Get returns an upload of the organization.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Upload, error) {
	return s.get(ctx, orgID, id)
}

func (s *Service) get(ctx context.Context, orgID, id uuid.UUID) (Upload, error) {
	u, ok, err := s.d.Store.GetUpload(ctx, orgID, id)
	if err != nil {
		return Upload{}, fmt.Errorf("uploads: ler: %w", err)
	}
	if !ok || u.Status == StatusDeleted {
		return Upload{}, apix.E(contracts.ErrUploadNotFound, ErrUploadNotFound)
	}
	return u, nil
}

// ListReady loads uploads for a run and refuses any that is missing or not
// yet clean (upload_not_found / upload_not_ready). The result keeps the
// order of ids.
func (s *Service) ListReady(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]Upload, error) {
	rows, err := s.d.Store.ListUploads(ctx, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("uploads: listar: %w", err)
	}
	byID := make(map[uuid.UUID]Upload, len(rows))
	for _, u := range rows {
		byID[u.ID] = u
	}
	out := make([]Upload, 0, len(ids))
	for _, id := range ids {
		u, ok := byID[id]
		if !ok || u.Status == StatusDeleted {
			return nil, apix.Ef(contracts.ErrUploadNotFound, "upload %s", apix.Encode(apix.PrefixUpload, id)).With(map[string]any{"upload_id": apix.Encode(apix.PrefixUpload, id)})
		}
		if !u.Ready() {
			return nil, apix.Ef(contracts.ErrUploadNotReady, "upload %s em %s", u.PublicID(), u.Status).With(map[string]any{"upload_id": u.PublicID(), "status": u.APIStatus(s.d.Now())})
		}
		out = append(out, u)
	}
	return out, nil
}

// StoreFor returns the object store that holds u.
func (s *Service) StoreFor(u Upload) storage.Store { return s.storeFor(u) }

func (s *Service) storeFor(u Upload) storage.Store {
	if u.Kind == KindMedia {
		return s.d.Stores.Media
	}
	return s.d.Stores.Documents
}

// RunScan is the scan job: it asks the runner for a verdict and applies it.
// A nil verdict means it will arrive through the queue later.
func (s *Service) RunScan(ctx context.Context, orgID, id uuid.UUID) error {
	u, err := s.get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if u.Status != StatusScanning {
		return nil
	}
	v, err := s.d.Scanner.Scan(ctx, u, s.storeFor(u))
	if err != nil {
		return fmt.Errorf("uploads: verificar %s: %w", u.PublicID(), err)
	}
	if v == nil {
		return nil
	}
	_, err = s.apply(ctx, u, *v)
	return err
}

// ApplyVerdict records a verdict that arrived from the scanner queue.
func (s *Service) ApplyVerdict(ctx context.Context, v Verdict) (Upload, error) {
	if err := v.Validate(); err != nil {
		return Upload{}, err
	}
	id, _ := apix.Decode(apix.PrefixUpload, v.UploadID)
	u, ok, err := s.d.Store.GetUploadAny(ctx, id)
	if err != nil {
		return Upload{}, fmt.Errorf("uploads: ler: %w", err)
	}
	if !ok {
		return Upload{}, ErrUploadNotFound
	}
	if u.Status != StatusScanning {
		// A replayed verdict for a finished upload is not an error.
		return u, nil
	}
	return s.apply(ctx, u, v)
}

func (s *Service) apply(ctx context.Context, u Upload, v Verdict) (Upload, error) {
	now := s.d.Now().UTC()
	u.ScannedAt = &now
	result, _ := json.Marshal(v)
	u.ScanResult = result
	action := "upload.accept"
	meta := map[string]any{"verdict": v.Verdict, "scanner": v.Scanner.Name}
	switch v.Verdict {
	case VerdictAccept, VerdictAcceptSanitized:
		u.Status = StatusClean
		if v.Mime != "" {
			u.DetectedMime = v.Mime
		}
		if sum, err := hex.DecodeString(v.SHA256); err == nil && len(sum) == 32 {
			u.SHA256 = sum
		}
		if v.DurationMs != nil {
			u.DurationMs = v.DurationMs
		}
	default:
		u.Status = StatusRejected
		if v.ReasonCode == ReasonMalware {
			u.Status = StatusInfected
		}
		u.RejectedRsn = v.RejectedReason()
		action = "upload.reject"
		meta["reason"] = u.RejectedRsn
	}
	updated, err := s.d.Store.UpdateUpload(ctx, u)
	if err != nil {
		return Upload{}, fmt.Errorf("uploads: gravar veredito: %w", err)
	}
	s.record(ctx, u.OrgID, audit.Actor{Type: audit.ActorSystem, ID: "scanner"}, action, updated, meta)
	return updated, nil
}

func (s *Service) record(ctx context.Context, orgID uuid.UUID, actor audit.Actor, action string, u Upload, meta map[string]any) {
	if s.d.Audit == nil {
		return
	}
	org := orgID
	if _, err := s.d.Audit.Record(ctx, &org, actor, action, audit.Target{Type: "upload", ID: u.PublicID()}, meta); err != nil {
		s.d.Logger.Error("auditoria falhou", slog.String("action", action), slog.String("err", err.Error()))
	}
}
