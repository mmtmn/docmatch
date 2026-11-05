// Package ports holds the four host ports of a local installation (site, API, Postgres, Mailpit),
// parses what the user types for them and checks whether they are free on 127.0.0.1.
package ports

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Names of the published ports, in display order.
const (
	Web      = "web"
	API      = "api"
	Postgres = "postgres"
	Mailpit  = "mailpit"
)

// Order is the display order.
var Order = []string{Web, API, Postgres, Mailpit}

// Set maps a port name to its number.
type Set map[string]int

// Defaults are the ports of `make local`.
func Defaults() Set {
	return Set{Web: 4321, API: 8080, Postgres: 5432, Mailpit: 8025}
}

// Clone copies s.
func (s Set) Clone() Set {
	out := Set{}
	for k, v := range s {
		out[k] = v
	}
	return out
}

// Errors returned by the parsers.
var (
	ErrRange     = errors.New("porta fora do intervalo 1024-65535")
	ErrNotNumber = errors.New("porta precisa ser um número")
	ErrDuplicate = errors.New("porta repetida")
	ErrUnknown   = errors.New("nome de porta desconhecido")
)

// Parse reads one port: an integer between 1024 and 65535 (no privileged ports: the installer
// never asks for admin rights).
func Parse(s string) (int, error) {
	s = strings.TrimSpace(s)
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, ErrNotNumber
	}
	if n < 1024 || n > 65535 {
		return 0, ErrRange
	}
	return n, nil
}

// ParseList reads "web=14321,api=18080" (any subset, comma or space separated) over base.
func ParseList(base Set, s string) (Set, error) {
	out := base.Clone()
	for _, item := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("%q: use nome=porta (web, api, postgres, mailpit)", item)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if _, known := base[name]; !known {
			return nil, fmt.Errorf("%q: %w", name, ErrUnknown)
		}
		n, err := Parse(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = n
	}
	return out, out.Validate()
}

// Validate checks ranges and that no two ports repeat.
func (s Set) Validate() error {
	seen := map[int]string{}
	for _, name := range Order {
		n, ok := s[name]
		if !ok {
			continue
		}
		if n < 1024 || n > 65535 {
			return fmt.Errorf("%s: %w", name, ErrRange)
		}
		if other, dup := seen[n]; dup {
			return fmt.Errorf("%s e %s: %w", other, name, ErrDuplicate)
		}
		seen[n] = name
	}
	return nil
}

// String renders "web=4321 api=8080 postgres=5432 mailpit=8025".
func (s Set) String() string {
	parts := make([]string, 0, len(s))
	for _, name := range Order {
		if n, ok := s[name]; ok {
			parts = append(parts, name+"="+strconv.Itoa(n))
		}
	}
	return strings.Join(parts, " ")
}

// Prober tells whether a port is free on the loopback interface.
type Prober func(port int) bool

// Free tries to listen on 127.0.0.1:port; a listener elsewhere on 0.0.0.0 also makes it fail.
func Free(port int) bool {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// Suggest returns the first free port after port that is not in taken.
func Suggest(port int, taken map[int]bool, free Prober) int {
	for p := port + 1; p <= 65535 && p < port+2000; p++ {
		if !taken[p] && free(p) {
			return p
		}
	}
	return 0
}

// Resolve replaces busy ports (not owned by us) with free suggestions; it returns the new set
// and, sorted, the names that changed.
func Resolve(s Set, ours map[int]bool, free Prober) (Set, []string) {
	out := s.Clone()
	taken := map[int]bool{}
	for _, n := range s {
		taken[n] = true
	}
	var changed []string
	for _, name := range Order {
		n := out[name]
		if ours[n] || free(n) {
			continue
		}
		if alt := Suggest(n, taken, free); alt != 0 {
			out[name] = alt
			taken[alt] = true
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return out, changed
}
