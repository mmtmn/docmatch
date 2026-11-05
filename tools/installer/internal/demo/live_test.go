package demo

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestTwinLive runs the twin against a real local stack. It only runs when DOCMATCH_DEMO_API
// (and optionally DOCMATCH_DEMO_SITE) point at one, e.g. after `docmatch` installed it:
//
//	DOCMATCH_DEMO_API=http://localhost:8080 go test ./internal/demo -run Live -v
func TestTwinLive(t *testing.T) {
	api := os.Getenv("DOCMATCH_DEMO_API")
	if api == "" {
		t.Skip("DOCMATCH_DEMO_API não definida")
	}
	c := New(api, os.Getenv("DOCMATCH_DEMO_SITE"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := c.Twin(ctx, func(p Progress) { t.Logf("%s %d/%d %d%% %s", p.Phase, p.Done, p.Total, p.Percent, p.Message) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run %s: %d achados (gabarito %d), %s em risco, %s", res.RunID, res.Findings, res.Expected,
		FormatMoney(res.AmountAtRisk, res.Currency), res.Elapsed)
	if res.Top != nil {
		t.Logf("evidência: %s: %s", res.Top.Title, EvidenceLine(res.Top))
	}
	if res.Findings != res.Expected {
		t.Fatalf("achados = %d, gabarito = %d", res.Findings, res.Expected)
	}
}
