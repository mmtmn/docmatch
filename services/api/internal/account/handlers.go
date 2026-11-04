package account

import (
	"errors"
	"net/http"
	"strconv"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// Mount registers the account routes. The session middleware is installed
// globally by the API handler; the protected routes add session.Require.
func (s *Service) Mount(r *httpx.Router) {
	r.Handle(http.MethodGet, "/v1/me", httpx.BodyNone, session.RequireFunc(s.getMe))
	r.Handle(http.MethodPatch, "/v1/me", httpx.BodyJSON, session.RequireFunc(s.patchMe))
	r.Handle(http.MethodDelete, "/v1/me", httpx.BodyNone, session.RequireFunc(s.deleteMe))
	r.Handle(http.MethodPut, "/v1/me/password", httpx.BodyJSON, session.RequireFunc(s.putPassword))
	r.Handle(http.MethodGet, "/v1/me/export", httpx.BodyNone, session.RequireFunc(s.getExport))
	r.HandleFunc(http.MethodGet, DownloadPath, httpx.BodyNone, s.download)
}

func principal(r *http.Request) session.Principal {
	p, _ := session.FromContext(r.Context())
	return p
}

// writeErr maps service errors onto the catalog.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *ValidationError
	switch {
	case errors.As(err, &ve):
		fields := make([]apierr.Field, 0, len(ve.Fields))
		for _, f := range ve.Fields {
			fields = append(fields, apierr.Field{Path: f.Path, Code: f.Code, Max: f.Max})
		}
		if ve.TooLong() {
			apierr.WriteDetails(w, r, contracts.ErrTextTooLong, map[string]any{"fields": fields})
			return
		}
		apierr.Validation(w, r, fields...)
	case errors.Is(err, ErrNotFound):
		apierr.Write(w, r, contracts.ErrUnauthenticated)
	case errors.Is(err, ErrDeletionPending):
		apierr.Write(w, r, contracts.ErrConflict)
	case errors.Is(err, ErrInvalidCredentials):
		apierr.Write(w, r, contracts.ErrInvalidCredentials)
	case errors.Is(err, ErrRecentLoginRequired):
		apierr.Write(w, r, contracts.ErrRecentLoginRequired)
	case errors.Is(err, ErrPasswordLength):
		apierr.Validation(w, r, apierr.Field{Path: "/new_password", Code: apierr.FieldInvalid})
	case errors.Is(err, ErrRateLimited):
		apierr.Write(w, r, contracts.ErrRateLimited)
	default:
		apierr.Internal(w, r, err)
	}
}

func (s *Service) getMe(w http.ResponseWriter, r *http.Request) {
	me, err := s.Me(r.Context(), principal(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, me)
}

func (s *Service) patchMe(w http.ResponseWriter, r *http.Request) {
	var patch MePatch
	if err := httpx.DecodeJSON(r, &patch); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	me, err := s.UpdateMe(r.Context(), principal(r), patch)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, me)
}

func (s *Service) deleteMe(w http.ResponseWriter, r *http.Request) {
	when, err := s.RequestDeletion(r.Context(), principal(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.d.Sessions.Clear(w)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"scheduled_for": when})
}

type passwordChange struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Service) putPassword(w http.ResponseWriter, r *http.Request) {
	var body passwordChange
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	if httpx.CountChars(body.CurrentPassword) > s.d.Limits.Text.PasswordMax {
		apierr.Validation(w, r, apierr.Field{Path: "/current_password", Code: apierr.FieldTooLong, Max: s.d.Limits.Text.PasswordMax})
		return
	}
	if err := s.SetPassword(r.Context(), principal(r), body.CurrentPassword, body.NewPassword); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) getExport(w http.ResponseWriter, r *http.Request) {
	st, err := s.Export(r.Context(), principal(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) download(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" || len(token) > 512 {
		apierr.Write(w, r, contracts.ErrNotFound)
		return
	}
	archive, e, err := s.OpenDownload(r.Context(), token)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrExportNotReady) {
			apierr.Write(w, r, contracts.ErrNotFound)
			return
		}
		apierr.Internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="docmatch-dados-`+e.RequestedAt.UTC().Format("20060102")+`.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(archive)
}
