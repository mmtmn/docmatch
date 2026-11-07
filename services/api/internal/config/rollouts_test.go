package config

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/publicid"
)

func TestAccountRolloutDefaultsAndValidation(t *testing.T) {
	id, other := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name, value           string
		dev, allowed, invalid bool
	}{
		{"dev", "", true, true, false},
		{"production", "", false, false, false},
		{"all", "*", false, true, false},
		{"paused", "none", true, false, false},
		{"selected", publicid.Encode("org", id), false, true, false},
		{"raw UUID", id.String(), false, false, true},
		{"wrong prefix", publicid.Encode("usr", id), false, false, true},
		{"mixed wildcard", "*," + publicid.Encode("org", id), false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &reader{lookup: func(string) (string, bool) { return tc.value, tc.value != "" }}
			p := r.accountRollout(VarRolloutProjects, tc.dev)
			if (r.err() != nil) != tc.invalid {
				t.Fatalf("validation: %v", r.err())
			}
			if !tc.invalid && p.Allows(id) != tc.allowed {
				t.Fatal("wrong account policy")
			}
			if tc.name == "selected" && p.Allows(other) {
				t.Fatal("allowed an unlisted account")
			}
		})
	}
}
