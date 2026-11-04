package ratelimit

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

// Class is a route class with its own rate policy.
type Class string

// Route classes.
const (
	// ClassAuth covers login, magic links, password reset: per IP + e-mail.
	ClassAuth Class = "auth"
	// ClassRead covers GET endpoints: per IP prefix (anonymous) or per user.
	ClassRead Class = "read"
	// ClassAPIKey covers requests authenticated with an API key: per key, by tier.
	ClassAPIKey Class = "api_key"
	// ClassEstimate covers cost estimates: per IP (anonymous) or per org.
	ClassEstimate Class = "estimate"
	// ClassUpload covers file uploads: per org, counted in files.
	ClassUpload Class = "upload"
)

// Plan is a subscription tier (mirrors config/pricing.yaml) or anonymous.
type Plan string

// Plans.
const (
	PlanAnonymous Plan = "anonymous"
	PlanStarter   Plan = "starter"
	PlanPro       Plan = "pro"
	PlanBusiness  Plan = "business"
)

// Policies holds every default rate of the API. Values mirror the plan
// definitions in config/pricing.yaml (concurrent_runs) and the limits agreed
// in the plan; a drift test keeps them aligned with the YAML.
type Policies struct {
	Auth          Policy
	ReadAnonymous Policy
	ReadUser      Policy
	APIKey        map[Plan]Policy
	Estimate      map[Plan]Policy
	Upload        Policy
	// ConcurrentRuns is the number of simultaneous runs per org, by plan.
	ConcurrentRuns map[Plan]int
	Lockout        LockoutPolicy
}

// Defaults returns the production policy table.
func Defaults() Policies {
	return Policies{
		Auth:          Policy{Limit: 5, Period: time.Minute},
		ReadAnonymous: Policy{Limit: 60, Period: time.Minute},
		ReadUser:      Policy{Limit: 300, Period: time.Minute},
		APIKey: map[Plan]Policy{
			PlanStarter:  {Limit: 600, Period: time.Minute},
			PlanPro:      {Limit: 1500, Period: time.Minute},
			PlanBusiness: {Limit: 3000, Period: time.Minute},
		},
		Estimate: map[Plan]Policy{
			PlanAnonymous: {Limit: 10, Period: time.Minute},
			PlanStarter:   {Limit: 10, Period: time.Minute},
			PlanPro:       {Limit: 30, Period: time.Minute},
			PlanBusiness:  {Limit: 120, Period: time.Minute},
		},
		Upload: Policy{Limit: 60, Period: time.Hour},
		ConcurrentRuns: map[Plan]int{
			PlanStarter:  1,
			PlanPro:      3,
			PlanBusiness: 10,
		},
		Lockout: DefaultLockoutPolicy(),
	}
}

// For resolves the policy of a class for a plan. ok is false when the
// combination has no policy (for example API keys for anonymous callers).
func (p Policies) For(class Class, plan Plan) (Policy, bool) {
	switch class {
	case ClassAuth:
		return p.Auth, true
	case ClassRead:
		if plan == PlanAnonymous {
			return p.ReadAnonymous, true
		}
		return p.ReadUser, true
	case ClassAPIKey:
		pol, ok := p.APIKey[plan]
		return pol, ok
	case ClassEstimate:
		pol, ok := p.Estimate[plan]
		return pol, ok
	case ClassUpload:
		if plan == PlanAnonymous {
			return Policy{}, false
		}
		return p.Upload, true
	}
	return Policy{}, false
}

// Validate checks every policy in the table.
func (p Policies) Validate() error {
	all := []Policy{p.Auth, p.ReadAnonymous, p.ReadUser, p.Upload}
	for _, m := range []map[Plan]Policy{p.APIKey, p.Estimate} {
		for _, pol := range m {
			all = append(all, pol)
		}
	}
	for _, pol := range all {
		if err := pol.Validate(); err != nil {
			return err
		}
	}
	for plan, n := range p.ConcurrentRuns {
		if n <= 0 {
			return errors.New("ratelimit: execuções simultâneas precisam ser positivas para o plano " + string(plan))
		}
	}
	return nil
}

// IPKey buckets an address for anonymous limits: /24 for IPv4 and /64 for
// IPv6, so one subscriber cannot dodge the limit by rotating addresses
// inside their allocation. IPv4-mapped IPv6 addresses are unmapped first.
func IPKey(ip netip.Addr) string {
	if !ip.IsValid() {
		return "ip:invalid"
	}
	ip = ip.Unmap()
	bits := 64
	if ip.Is4() {
		bits = 24
	}
	prefix, err := ip.Prefix(bits)
	if err != nil {
		return "ip:" + ip.String()
	}
	return "ip:" + prefix.Masked().String()
}

// ErrConcurrencyLimit is returned when a key already holds max slots.
var ErrConcurrencyLimit = errors.New("ratelimit: limite de execuções simultâneas atingido")

// Concurrency bounds simultaneous operations (runs) per key.
type Concurrency interface {
	// Acquire takes a slot for key if fewer than max are held; release frees it.
	Acquire(ctx context.Context, key string, max int) (release func(), err error)
	// Held reports the slots currently held by key.
	Held(ctx context.Context, key string) (int, error)
}

// MemoryConcurrency is the in-process Concurrency implementation.
type MemoryConcurrency struct {
	mu    sync.Mutex
	count map[string]int
}

// NewMemoryConcurrency returns an empty concurrency limiter.
func NewMemoryConcurrency() *MemoryConcurrency {
	return &MemoryConcurrency{count: map[string]int{}}
}

// Acquire implements Concurrency.
func (c *MemoryConcurrency) Acquire(_ context.Context, key string, max int) (func(), error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	if max <= 0 {
		return nil, ErrConcurrencyLimit
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count[key] >= max {
		return nil, ErrConcurrencyLimit
	}
	c.count[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.count[key] <= 1 {
				delete(c.count, key)
			} else {
				c.count[key]--
			}
		})
	}, nil
}

// Held implements Concurrency.
func (c *MemoryConcurrency) Held(_ context.Context, key string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count[key], nil
}
