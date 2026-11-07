package platforms

import (
	"bytes"
	"strings"
	"testing"
)

func TestCatalog(t *testing.T) {
	items, err := ParseCatalog(CatalogJSON)
	if err != nil || len(items) != 6 {
		t.Fatalf("catalog: %v %v", items, err)
	}
	for _, bad := range []string{
		`null`, `[]`, `{} `, `[] true`,
		strings.Replace(string(CatalogJSON), `"android"`, `"unknown"`, 1),
		strings.Replace(string(CatalogJSON), `"ios"`, `"android"`, 1),
		strings.Replace(string(CatalogJSON), `"Android"`, `""`, 1),
		strings.Replace(string(CatalogJSON), `"coming_soon"`, `"oops"`, 1),
		strings.Replace(string(CatalogJSON), `"url":""`, `"url":"https://play.google.com/app"`, 1),
	} {
		if _, err := ParseCatalog([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid catalog: %s", bad)
		}
	}
	available := bytes.Replace(CatalogJSON, []byte(`"status":"coming_soon","url":""`), []byte(`"status":"available","url":"https://play.google.com/store/apps/details?id=br.com.docmatch"`), 1)
	if _, err := ParseCatalog(available); err != nil {
		t.Fatal(err)
	}
	beta := bytes.Replace(CatalogJSON, []byte(`"id":"ios","name":"iOS","status":"coming_soon","url":""`), []byte(`"id":"ios","name":"iOS","status":"beta","url":"https://testflight.apple.com/join/example"`), 1)
	if _, err := ParseCatalog(beta); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(bytes.Replace(beta, []byte(`"beta"`), []byte(`"available"`), 1)); err == nil {
		t.Fatal("TestFlight advertised as a public store release")
	}
	for _, url := range []string{"http://play.google.com/app", "https://evil.example/app", "https://user@play.google.com/app", "https://play.google.com/", "https://play.google.com/app#token", "https://play.google.com:443/app"} {
		data := bytes.Replace(available, []byte("https://play.google.com/store/apps/details?id=br.com.docmatch"), []byte(url), 1)
		if _, err := ParseCatalog(data); err == nil {
			t.Errorf("unsafe destination accepted: %s", url)
		}
	}
}
func TestAddressValidation(t *testing.T) {
	if got, err := normalizeAddress("  Ana@Example.test  "); err != nil || got != "ana@example.test" {
		t.Fatalf("%q %v", got, err)
	}
	for _, value := range []string{"", "Ana <ana@example.test>", "a@localhost", "a,b@example.test", "a\n@example.test", strings.Repeat("x", 255) + "@example.test"} {
		if _, err := normalizeAddress(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}
