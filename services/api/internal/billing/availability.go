package billing

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Stripe activation is external to a deployment. Cache a short, bounded readiness
// check so a live account opens after approval and closes on provider failure.
// Portal access and webhook processing remain independent of payment readiness.
type paymentAvailability struct {
	mu      sync.Mutex
	until   time.Time
	enabled bool
}

func (s *Service) paymentsAvailable(ctx context.Context) bool {
	if !s.ready() {
		return false
	}
	if !s.live() {
		return true
	}
	s.availability.mu.Lock()
	defer s.availability.mu.Unlock()
	if s.now().Before(s.availability.until) {
		return s.availability.enabled
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var account struct {
		ChargesEnabled bool `json:"charges_enabled"`
		Capabilities   struct {
			CardPayments string `json:"card_payments"`
		} `json:"capabilities"`
	}
	err := s.stripe.call(checkCtx, http.MethodGet, "/account", nil, "", &account)
	s.availability.enabled = err == nil && account.ChargesEnabled && account.Capabilities.CardPayments == "active"
	ttl := time.Minute
	if err != nil {
		ttl = 10 * time.Second
	}
	s.availability.until = s.now().Add(ttl)
	return s.availability.enabled
}
