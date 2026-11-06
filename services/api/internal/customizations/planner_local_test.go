package customizations

import (
	"net/url"
	"os"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/config"
)

// Opt-in real provider check, never silently replaced by a fixture response.
func TestRealLocalPlanner(t *testing.T) {
	endpoint := os.Getenv("DM_TEST_UI_PLANNER_LOCAL")
	if endpoint == "" {
		t.Skip("set DM_TEST_UI_PLANNER_LOCAL for a real local chat endpoint")
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.LLM.Mode = config.LLMModeLocal
	cfg.LLM.LocalBaseURL = base
	t.Setenv("DM_MODEL_UI_PLANNER_LOCAL", "qwen3.5:0.8b")
	result, err := NewPlanner(cfg)(t.Context(), "Prefiro o modo escuro e uma lista compacta, mantendo as demais preferências.", Default())
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "dark" || result.Density != "compact" {
		t.Fatalf("unexpected preferences: %+v", result)
	}
}
