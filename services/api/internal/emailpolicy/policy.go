// Package emailpolicy checks a pinned, offline list. Addresses never leave the API.
package emailpolicy

import (
	_ "embed"
	"strings"

	"golang.org/x/net/idna"
)

//go:embed domains.txt
var domains string

var blocked = func() map[string]bool {
	m := make(map[string]bool)
	for _, d := range strings.Fields(domains) {
		m[d] = true
	}
	return m
}()

// Disposable includes subdomains, without confusing suffixes such as
// notmailinator.example with mailinator.com. Apple private relay is not disposable.
func Disposable(address string) bool {
	_, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(address)), "@")
	if !ok {
		return false
	}
	domain, err := idna.Lookup.ToASCII(strings.TrimSuffix(domain, "."))
	if err != nil {
		return false
	}
	for strings.Contains(domain, ".") {
		if blocked[domain] {
			return true
		}
		_, domain, _ = strings.Cut(domain, ".")
	}
	return false
}
