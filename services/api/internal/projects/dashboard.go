package projects

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
)

//go:embed dashboard.sql
var dashboardSQL string

type DashboardMetrics struct {
	Runs          int64    `json:"runs"`
	Completed     int64    `json:"completed"`
	Failed        int64    `json:"failed"`
	Cancelled     int64    `json:"cancelled"`
	Queued        int64    `json:"queued"`
	Running       int64    `json:"running"`
	NeedsApproval int64    `json:"needs_approval"`
	CreditsSpent  int64    `json:"credits_spent"`
	LinesTotal    int64    `json:"lines_total"`
	LinesMatched  int64    `json:"lines_matched"`
	Findings      int64    `json:"findings"`
	HighFindings  int64    `json:"high_findings"`
	MedianSeconds *float64 `json:"median_seconds"`
}

type DashboardDay struct {
	Date         string `json:"date"`
	Runs         int64  `json:"runs"`
	Completed    int64  `json:"completed"`
	Failed       int64  `json:"failed"`
	CreditsSpent int64  `json:"credits_spent"`
}

type DashboardRun struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	Expired     bool      `json:"expired"`
	Findings    int64     `json:"findings"`
}

type DashboardProject struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Documents  int64     `json:"documents"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastStatus *string   `json:"last_status"`
	Findings   int64     `json:"findings"`
}

type Dashboard struct {
	GeneratedAt  time.Time        `json:"generated_at"`
	From         time.Time        `json:"from"`
	PreviousFrom time.Time        `json:"previous_from"`
	Days         int              `json:"days"`
	TimeZone     string           `json:"time_zone"`
	Current      DashboardMetrics `json:"current"`
	Previous     DashboardMetrics `json:"previous"`
	Portfolio    struct {
		Active               int64 `json:"active"`
		Archived             int64 `json:"archived"`
		Documents            int64 `json:"documents"`
		UnavailableDocuments int64 `json:"unavailable_documents"`
	} `json:"portfolio"`
	Series    []DashboardDay     `json:"series"`
	Recent    []DashboardRun     `json:"recent"`
	Attention []DashboardRun     `json:"attention"`
	Projects  []DashboardProject `json:"projects"`
}

func (s *Service) dashboard(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	days := 30
	if values, present := r.URL.Query()["days"]; present {
		if len(values) != 1 {
			apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/days", Code: "invalid"}))
			return
		}
		days, err = strconv.Atoi(values[0])
		if err != nil {
			apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/days", Code: "invalid"}))
			return
		}
	}
	value, err := s.dashboardAt(r.Context(), p, days, time.Now().UTC())
	w.Header().Set("Cache-Control", "private, no-store")
	respond(w, r, value, err)
}

// One tenant-scoped statement gives every tile the same database snapshot.
// Results are aggregate metadata only; documents and findings never leave the database.
func (s *Service) dashboardAt(ctx context.Context, p apix.Principal, days int, now time.Time) (Dashboard, error) {
	out := Dashboard{GeneratedAt: now, Days: days, TimeZone: "America/Sao_Paulo"}
	if days != 7 && days != 30 && days != 90 {
		return out, apix.Validation(apix.FieldError{Path: "/days", Code: "invalid"})
	}
	out.From = now.AddDate(0, 0, -days)
	out.PreviousFrom = out.From.AddDate(0, 0, -days)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		// Match account activity: browser and extension sessions, never anonymous or API keys.
		if p.UserID == nil {
			return apix.ErrForbidden
		}
		role, err := member(ctx, tx, p)
		if err != nil {
			return err
		}
		caps := tenancy.Permissions(role)
		if !caps.ReadResults {
			return apix.ErrForbidden
		}
		var raw []byte
		if err := tx.QueryRow(ctx, dashboardSQL, p.OrgID, p.UserID, caps.ManageAccount, role != "reconciler", out.PreviousFrom, out.From, now).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out)
	})
	if err != nil {
		return out, err
	}
	for i := range out.Projects {
		id, err := uuid.Parse(out.Projects[i].ID)
		if err != nil {
			return out, err
		}
		out.Projects[i].ID = publicid.Encode(Prefix, id)
	}
	for _, items := range [][]DashboardRun{out.Recent, out.Attention} {
		for i := range items {
			id, err := uuid.Parse(items[i].ID)
			if err != nil {
				return out, err
			}
			items[i].ID = publicid.Encode("run", id)
			if items[i].ProjectID != "" {
				id, err := uuid.Parse(items[i].ProjectID)
				if err != nil {
					return out, err
				}
				items[i].ProjectID = publicid.Encode(Prefix, id)
			}
		}
	}
	return out, nil
}
