package crypto

import (
	"context"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// JWKS fetch limits.
const (
	maxJWKSBytes = 256 * 1024
	// DefaultJWKSTTL is how long a fetched key set is trusted.
	DefaultJWKSTTL = time.Hour
	// minJWKSRefresh bounds refreshes triggered by unknown kids, so a flood
	// of forged tokens cannot turn the API into a fetch loop.
	minJWKSRefresh = time.Minute
	jwksTimeout    = 10 * time.Second
)

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// ParseJWKSet decodes a JSON Web Key Set into public keys by kid. Unknown
// key types are skipped; keys without kid are skipped too (tokens carry one).
func ParseJWKSet(data []byte) (map[string]stdcrypto.PublicKey, error) {
	var set jwkSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("crypto: jwks: %w", err)
	}
	out := map[string]stdcrypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Kid == "" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(n) == 0 || len(e) == 0 {
				continue
			}
			exp := new(big.Int).SetBytes(e)
			if !exp.IsInt64() || exp.Int64() < 3 {
				continue
			}
			out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exp.Int64())}
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if err1 != nil || err2 != nil || len(x) > 32 || len(y) > 32 {
				continue
			}
			// Uncompressed point 0x04 ‖ X ‖ Y with the coordinates left-padded
			// to 32 bytes; the parser rejects points off the curve.
			point := make([]byte, 65)
			point[0] = 4
			copy(point[1+32-len(x):33], x)
			copy(point[33+32-len(y):], y)
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
			if err != nil {
				continue
			}
			out[k.Kid] = pub
		}
	}
	return out, nil
}

// JWKS caches the key set of an identity provider and refreshes it when it
// ages past the TTL or when a token names an unknown kid (rate limited).
type JWKS struct {
	url    string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu          sync.Mutex
	keys        map[string]stdcrypto.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// JWKSOption configures NewJWKS.
type JWKSOption func(*JWKS)

// WithJWKSTTL overrides the cache TTL.
func WithJWKSTTL(d time.Duration) JWKSOption { return func(j *JWKS) { j.ttl = d } }

// WithJWKSClock substitutes the time source (tests).
func WithJWKSClock(now func() time.Time) JWKSOption { return func(j *JWKS) { j.now = now } }

// NewJWKS returns a cache for url. client may be nil (a 10 s timeout client is used).
func NewJWKS(url string, client *http.Client, opts ...JWKSOption) *JWKS {
	if client == nil {
		client = &http.Client{Timeout: jwksTimeout}
	}
	j := &JWKS{url: url, client: client, ttl: DefaultJWKSTTL, now: time.Now, keys: map[string]stdcrypto.PublicKey{}}
	for _, o := range opts {
		o(j)
	}
	return j
}

// Key implements KeyFunc: it returns the cached key for kid, refreshing the
// set first when the cache is stale or the kid is unknown.
func (j *JWKS) Key(ctx context.Context, kid, _ string) (stdcrypto.PublicKey, error) {
	if kid == "" {
		return nil, ErrJWTNoKey
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	stale := j.fetchedAt.IsZero() || now.Sub(j.fetchedAt) > j.ttl
	key, ok := j.keys[kid]
	if ok && !stale {
		return key, nil
	}
	if !stale && now.Sub(j.lastAttempt) < minJWKSRefresh {
		return nil, ErrJWTNoKey
	}
	if err := j.refreshLocked(ctx, now); err != nil {
		if key, ok := j.keys[kid]; ok {
			return key, nil // keep serving the last good set while the provider is down
		}
		return nil, err
	}
	if key, ok := j.keys[kid]; ok {
		return key, nil
	}
	return nil, ErrJWTNoKey
}

func (j *JWKS) refreshLocked(ctx context.Context, now time.Time) error {
	j.lastAttempt = now
	ctx, cancel := context.WithTimeout(ctx, jwksTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return fmt.Errorf("crypto: jwks: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return fmt.Errorf("crypto: jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("crypto: jwks: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return fmt.Errorf("crypto: jwks: %w", err)
	}
	if len(data) > maxJWKSBytes {
		return errors.New("crypto: jwks: response too large")
	}
	keys, err := ParseJWKSet(data)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("crypto: jwks: no usable keys")
	}
	j.keys = keys
	j.fetchedAt = now
	return nil
}

// Len reports how many keys are cached (tests).
func (j *JWKS) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.keys)
}
