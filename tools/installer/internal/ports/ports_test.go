package ports

import (
	"errors"
	"net"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]int{"4321": 4321, "1024": 1024, "65535": 65535} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; quero %d", in, got, err, want)
		}
	}
	for in, want := range map[string]error{"80": ErrRange, "65536": ErrRange, "abc": ErrNotNumber, "": ErrNotNumber, "12.5": ErrNotNumber} {
		if _, err := Parse(in); !errors.Is(err, want) {
			t.Errorf("Parse(%q) erro = %v; quero %v", in, err, want)
		}
	}
}

func TestParseList(t *testing.T) {
	got, err := ParseList(Defaults(), "web=14321, api=18080 postgres=15432,mailpit=18025")
	if err != nil {
		t.Fatal(err)
	}
	want := Set{Web: 14321, API: 18080, Postgres: 15432, Mailpit: 18025}
	if got.String() != want.String() {
		t.Fatalf("got %s, want %s", got, want)
	}
	partial, err := ParseList(Defaults(), "api=9090")
	if err != nil || partial[API] != 9090 || partial[Web] != 4321 {
		t.Fatalf("parcial: %v %v", partial, err)
	}
	for _, bad := range []string{"web", "site=1", "web=80", "web=8080"} {
		if _, err := ParseList(Defaults(), bad); err == nil {
			t.Errorf("ParseList(%q) devia falhar", bad)
		}
	}
	if _, err := ParseList(Defaults(), "web=8080"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("porta repetida: %v", err)
	}
}

func TestStringOrder(t *testing.T) {
	if got := Defaults().String(); got != "web=4321 api=8080 postgres=5432 mailpit=8025" {
		t.Fatal(got)
	}
}

func TestSuggestAndResolve(t *testing.T) {
	busy := map[int]bool{4321: true, 4322: true, 8080: true}
	free := func(p int) bool { return !busy[p] }
	if got := Suggest(4321, map[int]bool{}, free); got != 4323 {
		t.Fatalf("Suggest = %d", got)
	}
	// web skips the busy 4322; api takes the next free one.
	s, changed := Resolve(Defaults(), nil, free)
	if s[Web] != 4323 || s[API] != 8081 || s[Postgres] != 5432 {
		t.Fatalf("Resolve = %s", s)
	}
	if len(changed) != 2 || changed[0] != API || changed[1] != Web {
		t.Fatalf("changed = %v", changed)
	}
	// Ports our own containers hold stay.
	s, changed = Resolve(Defaults(), map[int]bool{4321: true, 8080: true}, free)
	if s[Web] != 4321 || s[API] != 8080 || len(changed) != 0 {
		t.Fatalf("ours: %s %v", s, changed)
	}
	// A suggestion never collides with another port of the set.
	s, _ = Resolve(Set{Web: 5000, API: 5002, Postgres: 5432, Mailpit: 8025}, nil, func(p int) bool { return p != 5000 && p != 5001 })
	if s[Web] == s[API] || s[Web] != 5003 {
		t.Fatalf("colisão: %s", s)
	}
}

func TestFree(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port
	if Free(port) {
		t.Fatalf("porta %d em uso deveria estar ocupada", port)
	}
	_ = l.Close()
	if !Free(port) {
		t.Fatalf("porta %d liberada deveria estar livre", port)
	}
}
