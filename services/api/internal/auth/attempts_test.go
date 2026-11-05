package auth

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
)

// staleReads reproduces the worst interleaving of concurrent verifications:
// every request reads the challenge before any attempt is recorded.
type staleReads struct{ *MemoryStore }

func (s staleReads) ChallengeByID(ctx context.Context, id uuid.UUID) (Challenge, bool, error) {
	ch, ok, err := s.MemoryStore.ChallengeByID(ctx, id)
	ch.Attempts = 0
	return ch, ok, err
}

func codeOf(t *testing.T, body map[string]any) string {
	t.Helper()
	c, _ := body["code"].(string)
	return c
}

// A read-then-increment cap lets every stale reader compare its guess. The
// attempt is claimed atomically before the comparison, so at most
// ChallengeAttempts guesses are ever evaluated.
func TestEmailVerifyAttemptCapSurvivesStaleReads(t *testing.T) {
	h := newHarness(t)
	h.svc.d.Store = staleReads{h.store}
	cid := decode(t, h.post(EmailStartPath, `{"email":"ana@example.com"}`))["challenge_id"].(string)

	invalid, capped := 0, 0
	for range 20 {
		switch codeOf(t, decode(t, h.post(EmailVerifyPath, `{"challenge_id":"`+cid+`","code":"000000"}`))) {
		case string(contracts.ErrEmailCodeInvalid):
			invalid++
		case string(contracts.ErrTooManyAttempts):
			capped++
		}
	}
	if invalid != ChallengeAttempts || capped != 20-ChallengeAttempts {
		t.Fatalf("comparações avaliadas = %d (quer %d), recusadas = %d", invalid, ChallengeAttempts, capped)
	}
}

func TestEmailVerifyAttemptCapUnderParallelRequests(t *testing.T) {
	h := newHarness(t)
	cid := decode(t, h.post(EmailStartPath, `{"email":"bia@example.com"}`))["challenge_id"].(string)

	const n = 32
	codes := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := h.post(EmailVerifyPath, `{"challenge_id":"`+cid+`","code":"000000"}`)
			codes[i] = codeOf(t, decode(t, rec))
		}()
	}
	wg.Wait()
	invalid := 0
	for _, c := range codes {
		if c == string(contracts.ErrEmailCodeInvalid) {
			invalid++
		}
	}
	if invalid > ChallengeAttempts {
		t.Fatalf("%d palpites avaliados em paralelo; o teto é %d", invalid, ChallengeAttempts)
	}
	id := mustDecode(t, cid)
	if ch, _, _ := h.store.ChallengeByID(context.Background(), id); ch.Attempts != ChallengeAttempts {
		t.Fatalf("attempts = %d, quer %d", ch.Attempts, ChallengeAttempts)
	}
}
