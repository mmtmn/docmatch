package teams

import (
	"strings"
	"testing"
)

func TestEmailMatchingNeverParsesAnotherMailbox(t *testing.T) {
	valid, err := normalizeEmail(" Ana@Example.com ")
	if err != nil || valid != "ana@example.com" {
		t.Fatal(valid, err)
	}
	for _, value := range []string{"", "Ana <ana@example.com>", "a@example.com,b@example.com", "a@localhost", "a\n@example.com", "\"a\"@example.com", strings.Repeat("a", 255) + "@example.com"} {
		if _, err := normalizeEmail(value); err == nil {
			t.Fatalf("ambiguous mailbox accepted: %q", value)
		}
	}
	for input, want := range map[string]string{"ana@example.com": "a***@example.com", "a@example.com": "***@example.com", "invalid": "***"} {
		if got := emailHint(input); got != want {
			t.Fatal(got, want)
		}
	}
}
