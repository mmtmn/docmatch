package publicid

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var pattern = regexp.MustCompile(`^usr_[0-9A-HJKMNP-TV-Z]{26}$`)

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	for i := 0; i < 200; i++ {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		s := Encode(PrefixUser, id)
		if !pattern.MatchString(s) {
			t.Fatalf("%s não segue o padrão do contrato", s)
		}
		got, err := Decode(PrefixUser, s)
		if err != nil || got != id {
			t.Fatalf("decode(%s) = %s, %v; esperado %s", s, got, err, id)
		}
	}
	for _, id := range []uuid.UUID{uuid.Nil, uuid.Max} {
		if got, err := Decode(PrefixOrg, Encode(PrefixOrg, id)); err != nil || got != id {
			t.Fatalf("limite %s: %s %v", id, got, err)
		}
	}
	if Encode(PrefixUser, uuid.Max) != "usr_7ZZZZZZZZZZZZZZZZZZZZZZZZZ" || Encode(PrefixUser, uuid.Nil) != "usr_00000000000000000000000000" {
		t.Fatalf("extremos: %s %s", Encode(PrefixUser, uuid.Max), Encode(PrefixUser, uuid.Nil))
	}
}

func TestSortable(t *testing.T) {
	t.Parallel()
	a, _ := uuid.NewV7()
	b, _ := uuid.NewV7()
	if strings.Compare(a.String(), b.String()) != strings.Compare(encode(a), encode(b)) {
		t.Fatal("a codificação precisa preservar a ordem dos uuidv7")
	}
}

func TestDecodeErrors(t *testing.T) {
	t.Parallel()
	good := Encode(PrefixUser, uuid.Max)
	for _, bad := range []string{"", "usr_", "org_" + good[4:], good[:len(good)-1], good + "A", "usr_" + strings.Repeat("U", 26), "usr_8" + strings.Repeat("0", 25), strings.ToLower(good) + "x"} {
		if _, err := Decode(PrefixUser, bad); err == nil {
			t.Fatalf("%q deveria falhar", bad)
		}
	}
	// Crockford aliases and lower case decode to the same id.
	id, _ := uuid.NewV7()
	s := Encode(PrefixExport, id)
	if got, err := Decode(PrefixExport, strings.ToLower(s)); err != nil || got != id {
		t.Fatal("minúsculas")
	}
	alias := strings.ReplaceAll(strings.ReplaceAll(s, "0", "O"), "1", "I")
	if got, err := Decode(PrefixExport, alias); err != nil || got != id {
		t.Fatal("aliases O/I")
	}
	if PrefixChallenge != "chl" {
		t.Fatal("prefixo")
	}
}
