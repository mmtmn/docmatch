package platforms

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

//go:embed integrations.json
var integrationsJSON []byte

func integrationKnown(id string) bool {
	var items []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(integrationsJSON, &items) != nil {
		return false
	}
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func (s *Service) mountIntegrationInterests(r *httpx.Router) {
	limit := ratelimit.Middleware(ratelimit.NewMemory(), func(r *http.Request) (string, ratelimit.Policy, bool) {
		return string(crypto.MAC(s.privacyKey, []byte(s.clientIP(r)))), ratelimit.Policy{Limit: 30, Period: time.Minute}, true
	})
	r.Handle(http.MethodPost, "/v1/integration-interests", httpx.BodyJSON, limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Integration string `json:"integration"`
			VisitorID   string `json:"visitor_id"`
		}
		if err := httpx.DecodeJSON(r, &input); err != nil {
			apix.Handle(w, r, err)
			return
		}
		if err := s.RecordIntegrationInterest(r.Context(), input.Integration, input.VisitorID); err != nil {
			apix.Handle(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		apix.WriteJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "status": "coming_soon"})
	})))
}

// RecordIntegrationInterest deduplicates interest per person or anonymous browser.
// Identity comes from the verified session; neither email nor IP is retained.
func (s *Service) RecordIntegrationInterest(ctx context.Context, integration, visitor string) error {
	if !integrationKnown(integration) {
		return validation("integration")
	}
	id, err := uuid.Parse(visitor)
	if err != nil || id == uuid.Nil {
		return validation("visitor_id")
	}
	subject := "visitor:" + id.String()
	var user any
	if principal, ok := session.FromContext(ctx); ok {
		user = principal.UserID
		subject = "user:" + principal.UserID.String()
	}
	hash := crypto.MAC(s.privacyKey, []byte("integration-interest/v1"), []byte(subject))
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO integration_interests(integration_id,subject_hash,user_id) VALUES($1,$2,$3)
  ON CONFLICT(integration_id,subject_hash) DO UPDATE SET updated_at=now()`, integration, hash, user)
		return err
	})
}
