// Package billing implements Stripe Checkout, customer portal and signed payment events.
package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const stripeVersion = "2026-10-28.endive"
const stripeBaseURL = "https://api.stripe.com/v1"

// Stripe uses its published API endpoint in production; tests substitute a local server.
type Stripe struct {
	Key, BaseURL string
	HTTP         *http.Client
}

func (s *Stripe) call(ctx context.Context, method, path string, form url.Values, key string, out any) error {
	base := s.BaseURL
	if base == "" {
		base = stripeBaseURL
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	req.Header.Set("Stripe-Version", stripeVersion)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("billing: Stripe connection failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var detail struct {
			Error struct {
				Code  string `json:"code"`
				Type  string `json:"type"`
				Param string `json:"param"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&detail)
		return &stripeError{status: resp.StatusCode, code: detail.Error.Code, kind: detail.Error.Type, param: detail.Error.Param}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
		return errors.New("billing: invalid Stripe response")
	}
	return nil
}

type stripeError struct {
	status            int
	code, kind, param string
}

func (e *stripeError) Error() string {
	return fmt.Sprintf("billing: Stripe HTTP %d (%s/%s; parameter %s)", e.status, e.kind, e.code, e.param)
}

// ID accepts an expandable Stripe reference without depending on whether it was expanded.
type ID string

func (id *ID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*id = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*id = ID(s)
		return nil
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*id = ID(v.ID)
	return nil
}

type customer struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
	Deleted  bool              `json:"deleted"`
}
type price struct {
	ID         string            `json:"id"`
	Currency   string            `json:"currency"`
	UnitAmount int64             `json:"unit_amount"`
	Active     bool              `json:"active"`
	Livemode   bool              `json:"livemode"`
	Metadata   map[string]string `json:"metadata"`
	Recurring  *struct {
		Interval      string `json:"interval"`
		IntervalCount int    `json:"interval_count"`
	} `json:"recurring"`
}
type checkoutSession struct {
	ID            string            `json:"id"`
	URL           string            `json:"url"`
	Mode          string            `json:"mode"`
	Status        string            `json:"status"`
	PaymentStatus string            `json:"payment_status"`
	Customer      ID                `json:"customer"`
	Subscription  ID                `json:"subscription"`
	PaymentIntent ID                `json:"payment_intent"`
	Currency      string            `json:"currency"`
	AmountTotal   int64             `json:"amount_total"`
	ExpiresAt     int64             `json:"expires_at"`
	Livemode      bool              `json:"livemode"`
	Metadata      map[string]string `json:"metadata"`
}
type subscriptionItem struct {
	Price              price `json:"price"`
	Quantity           int64 `json:"quantity"`
	CurrentPeriodStart int64 `json:"current_period_start"`
	CurrentPeriodEnd   int64 `json:"current_period_end"`
}
type subscription struct {
	ID                 string            `json:"id"`
	Customer           ID                `json:"customer"`
	Status             string            `json:"status"`
	Livemode           bool              `json:"livemode"`
	Metadata           map[string]string `json:"metadata"`
	CurrentPeriodStart int64             `json:"current_period_start"`
	CurrentPeriodEnd   int64             `json:"current_period_end"`
	CancelAt           int64             `json:"cancel_at"`
	CanceledAt         int64             `json:"canceled_at"`
	CancelAtPeriodEnd  bool              `json:"cancel_at_period_end"`
	Items              struct {
		Data []subscriptionItem `json:"data"`
	} `json:"items"`
}
type invoice struct {
	ID            string `json:"id"`
	Customer      ID     `json:"customer"`
	Subscription  ID     `json:"subscription"`
	Status        string `json:"status"`
	Livemode      bool   `json:"livemode"`
	Currency      string `json:"currency"`
	AmountPaid    int64  `json:"amount_paid"`
	BillingReason string `json:"billing_reason"`
	Parent        *struct {
		SubscriptionDetails *struct {
			Subscription ID `json:"subscription"`
		} `json:"subscription_details"`
	} `json:"parent"`
	Lines struct {
		HasMore bool `json:"has_more"`
		Data    []struct {
			Price     price `json:"price"`
			Quantity  int64 `json:"quantity"`
			Amount    int64 `json:"amount"`
			Proration bool  `json:"proration"`
			Pricing   *struct {
				PriceDetails *struct {
					Price ID `json:"price"`
				} `json:"price_details"`
			} `json:"pricing"`
			Period struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"period"`
		} `json:"data"`
	} `json:"lines"`
}

func (i invoice) subscriptionID() string {
	if i.Parent != nil && i.Parent.SubscriptionDetails != nil {
		return string(i.Parent.SubscriptionDetails.Subscription)
	}
	return string(i.Subscription)
}

type event struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	APIVersion string `json:"api_version"`
	Livemode   bool   `json:"livemode"`
	Data       struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// verifySignature authenticates the original bytes, allows key rotation and bounds replay time.
func verifySignature(raw []byte, header, secrets string, now time.Time) bool {
	var timestamp string
	var signatures [][]byte
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			if timestamp != "" {
				return false
			}
			timestamp = value
		case "v1":
			b, err := hex.DecodeString(value)
			if err == nil && len(b) == sha256.Size {
				signatures = append(signatures, b)
			}
		}
	}
	n, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	delta := now.Unix() - n
	if delta < -300 || delta > 300 {
		return false
	}
	for _, secret := range strings.Split(secrets, ",") {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(raw)
		expected := mac.Sum(nil)
		for _, sig := range signatures {
			if hmac.Equal(expected, sig) {
				return true
			}
		}
	}
	return false
}
