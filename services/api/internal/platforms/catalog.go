// Package platforms owns public distribution destinations and opt-in release notices.
package platforms

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/url"
)

// CatalogJSON is shared with the static website build. Only public destinations belong here.
//
//go:embed catalog.json
var CatalogJSON []byte

type Platform struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

// ParseCatalog rejects accidental unpublished links, credentials and non-store destinations.
func ParseCatalog(data []byte) ([]Platform, error) {
	var items []Platform
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&items); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("platforms: trailing data")
	}
	hosts := map[string]string{"android": "play.google.com", "ios": "apps.apple.com", "github": "github.com", "chrome": "chromewebstore.google.com", "firefox": "addons.mozilla.org", "safari": "apps.apple.com"}
	seen := map[string]bool{}
	for _, item := range items {
		host, ok := hosts[item.ID]
		if !ok || seen[item.ID] || item.Name == "" {
			return nil, errors.New("platforms: invalid or duplicate platform")
		}
		seen[item.ID] = true
		if item.Status == "coming_soon" {
			if item.URL != "" {
				return nil, errors.New("platforms: coming soon has a destination")
			}
			continue
		}
		if item.Status != "available" && item.Status != "beta" {
			return nil, errors.New("platforms: invalid status")
		}
		u, err := url.Parse(item.URL)
		if err != nil {
			return nil, errors.New("platforms: invalid destination")
		}
		validHost := u.Host == host || (item.Status == "beta" && (item.ID == "ios" || item.ID == "safari") && u.Host == "testflight.apple.com")
		if u.Scheme != "https" || !validHost || u.User != nil || u.Fragment != "" || u.Path == "" || u.Path == "/" {
			return nil, errors.New("platforms: expected an official HTTPS destination")
		}
	}
	if len(seen) != len(hosts) {
		return nil, errors.New("platforms: incomplete catalog")
	}
	return items, nil
}
