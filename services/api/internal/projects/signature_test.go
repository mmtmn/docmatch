package projects

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/rules"
)

func TestAnalysisChangesAreMeaningful(t *testing.T) {
	rule := rules.Rule{ID: uuid.New(), Version: 1, Body: "Compare os valores"}
	a := analysisSource{Hash: "first", Kind: "document", Role: "bank"}
	b := analysisSource{Hash: "second", Kind: "document", Role: "ledger"}
	settings := Settings{UseRules: true}
	original, err := signature([]analysisSource{a, b}, settings, []rules.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	rule.Stats.RunsEvaluated = 100
	reordered, err := signature([]analysisSource{b, a}, settings, []rules.Rule{rule})
	if err != nil || original != reordered {
		t.Fatal("usage statistics or visual order caused a new analysis", err)
	}
	duplicate, err := signature([]analysisSource{a, b, b}, settings, []rules.Rule{rule})
	if err != nil || duplicate == original {
		t.Fatal("intentional duplicate lost", err)
	}
	rule.Version++
	changed, err := signature([]analysisSource{a, b}, settings, []rules.Rule{rule})
	if err != nil || changed == original {
		t.Fatal("new rule version ignored", err)
	}
	settings.WebSearch = true
	search, err := signature([]analysisSource{a, b}, settings, []rules.Rule{rule})
	if err != nil || search == changed {
		t.Fatal("analysis option ignored", err)
	}
}
