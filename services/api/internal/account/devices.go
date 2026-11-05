package account

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// Device routes: every sign-in is its own session (one per browser or app
// install, bound to that device), all on the same account. Any of them can
// see the others and sign them out.
const (
	DevicesPath = "/v1/me/sessions"
	devicePath  = "/v1/me/sessions/{id}"

	ActionSessionRevoked = "account.session_revoked"
)

// ErrDeviceNotFound is a session that does not exist or is not the caller's.
var ErrDeviceNotFound = errors.New("account: sessão não encontrada")

// DeviceView is the Device schema.
type DeviceView struct {
	ID         string    `json:"id"`
	Platform   string    `json:"platform"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	IPPrefix   string    `json:"ip_prefix,omitempty"`
	Current    bool      `json:"current"`
}

// DeviceList is the DeviceList schema.
type DeviceList struct {
	Items []DeviceView `json:"items"`
}

// Devices lists the active sessions of the caller, the current one first,
// then by last activity.
func (s *Service) Devices(ctx context.Context, p session.Principal) (DeviceList, error) {
	rows, err := s.d.Sessions.Store().ListActive(ctx, p.UserID)
	if err != nil {
		return DeviceList{}, err
	}
	out := DeviceList{Items: make([]DeviceView, 0, len(rows))}
	for _, r := range rows {
		platform, name := DescribeUserAgent(r.UserAgent)
		out.Items = append(out.Items, DeviceView{
			ID: publicid.Encode(publicid.PrefixSession, r.ID), Platform: platform, Name: name,
			CreatedAt: r.CreatedAt.UTC(), LastSeenAt: r.LastSeenAt.UTC(), IPPrefix: r.IPPrefix, Current: r.ID == p.SessionID,
		})
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		if out.Items[i].Current != out.Items[j].Current {
			return out.Items[i].Current
		}
		return out.Items[i].LastSeenAt.After(out.Items[j].LastSeenAt)
	})
	return out, nil
}

// RevokeDevice signs one of the caller's sessions out. It reports whether it
// was the session making the request (the caller then clears its cookies).
func (s *Service) RevokeDevice(ctx context.Context, p session.Principal, publicID string) (bool, error) {
	id, err := publicid.Decode(publicid.PrefixSession, publicID)
	if err != nil {
		return false, ErrDeviceNotFound
	}
	row, ok, err := s.d.Sessions.Store().ByID(ctx, id)
	if err != nil {
		return false, err
	}
	if !ok || row.UserID != p.UserID || row.RevokedAt != nil {
		return false, ErrDeviceNotFound
	}
	if _, err := s.d.Sessions.Store().Revoke(ctx, id); err != nil {
		return false, err
	}
	platform, name := DescribeUserAgent(row.UserAgent)
	if err := s.record(ctx, p.OrgID, userActor(p.UserID), ActionSessionRevoked, audit.Target{Type: "session", ID: id.String()},
		map[string]any{"platform": platform, "name": name, "by_session": p.SessionID.String()}); err != nil {
		return false, err
	}
	return id == p.SessionID, nil
}

func (s *Service) getDevices(w http.ResponseWriter, r *http.Request) {
	list, err := s.Devices(r.Context(), principal(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (s *Service) deleteDevice(w http.ResponseWriter, r *http.Request) {
	current, err := s.RevokeDevice(r.Context(), principal(r), r.PathValue("id"))
	if errors.Is(err, ErrDeviceNotFound) {
		apierr.Write(w, r, contracts.ErrNotFound)
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if current {
		s.d.Sessions.Clear(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

var (
	appUARe     = regexp.MustCompile(`^docmatch-(android|ios)/[^ ]+(?: \(([^;)]*))?`)
	browserList = []struct{ token, name string }{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	}
	osList = []struct{ token, name string }{
		{"Android", "Android"}, {"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Windows", "Windows"}, {"Mac OS X", "macOS"},
		{"CrOS", "ChromeOS"}, {"Linux", "Linux"},
	}
)

// DescribeUserAgent turns a session's User-Agent into a platform (web,
// android, ios) and a short name for the device list, the same on every
// client.
func DescribeUserAgent(ua string) (platform, name string) {
	if m := appUARe.FindStringSubmatch(ua); m != nil {
		model := strings.TrimSpace(m[2])
		if m[1] == "android" {
			if model == "" {
				return "android", "App Android"
			}
			return "android", "App Android · " + model
		}
		if model == "" {
			return "ios", "App iOS"
		}
		return "ios", "App iOS · " + model
	}
	browser, osName := "Navegador", ""
	for _, b := range browserList {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	for _, o := range osList {
		if strings.Contains(ua, o.token) {
			osName = o.name
			break
		}
	}
	if osName == "" {
		return "web", browser
	}
	return "web", browser + " · " + osName
}
