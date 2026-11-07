// Package crypto holds the cryptographic primitives the authentication and
// account code share: a keyring that derives purpose-bound subkeys from
// DM_SESSION_SECRET with HKDF, AES-256-GCM sealing (cookies, wrapped tenant
// data keys), Argon2id password hashing, random tokens with SHA-256 digests,
// HMAC-signed expiring tokens (download links) and a minimal JWS
// implementation with a JWKS cache for the OpenID Connect providers.
//
// Everything here is deterministic given its inputs and free of I/O except
// the JWKS fetch, so the callers (session, auth, account) stay testable with
// in-memory stores.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

// Purposes label the subkeys derived from the master secret. Changing a label
// changes the key, so they are versioned.
const (
	// PurposeKEK wraps the per-tenant data keys (crypto-shredding).
	PurposeKEK = "docmatch/kek/v1"
	// PurposeAppleToken protects revocable Apple grants independently of tenant data.
	PurposeAppleToken = "docmatch/apple-token/v1"
	// PurposeCookie seals the short-lived OAuth state cookies.
	PurposeCookie = "docmatch/cookie/v1"
	// PurposeEmailCode keys the HMAC of the six-digit login codes.
	PurposeEmailCode = "docmatch/email-code/v1"
	// PurposeSignedURL signs expiring download tokens.
	PurposeSignedURL = "docmatch/signed-url/v1"
)

// MinMasterBytes is the shortest master secret the keyring accepts
// (DM_SESSION_SECRET decodes to at least this many bytes).
const MinMasterBytes = 32

// keyringSalt separates this keyring from any other HKDF use of the secret.
const keyringSalt = "docmatch-keyring-v1"

// subkeyLen is the length of every derived key (AES-256, HMAC-SHA256).
const subkeyLen = 32

// ErrShortSecret is returned by NewKeyring for a master secret below MinMasterBytes.
var ErrShortSecret = errors.New("crypto: master secret shorter than 32 bytes")

// Keyring derives and caches purpose-bound subkeys from one master secret.
type Keyring struct {
	master []byte
	mu     sync.Mutex
	cache  map[string][]byte
}

// NewKeyring validates the master secret and returns a keyring over a copy of it.
func NewKeyring(master []byte) (*Keyring, error) {
	if len(master) < MinMasterBytes {
		return nil, ErrShortSecret
	}
	cp := make([]byte, len(master))
	copy(cp, master)
	return &Keyring{master: cp, cache: map[string][]byte{}}, nil
}

// Key returns the 32-byte subkey for purpose (HKDF-SHA256). The result is
// shared with other callers of the same purpose; never modify it.
func (k *Keyring) Key(purpose string) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	if key, ok := k.cache[purpose]; ok {
		return key
	}
	r := hkdf.New(sha256.New, k.master, []byte(keyringSalt), []byte(purpose))
	key := make([]byte, subkeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		panic("crypto: hkdf: " + err.Error())
	}
	k.cache[purpose] = key
	return key
}

// RandomBytes returns n bytes from crypto/rand; it panics when the system
// random source is unavailable, which no code path can recover from.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand indisponível: " + err.Error())
	}
	return b
}

// RandomToken returns n random bytes encoded as unpadded base64url (43
// characters for n = 32): safe in cookies, query strings and e-mails.
func RandomToken(n int) string {
	return base64.RawURLEncoding.EncodeToString(RandomBytes(n))
}

// RandomDigits returns a uniformly random string of n decimal digits, the
// shape of a login code the user types.
func RandomDigits(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '0' + randomBelow(10)
	}
	return string(out)
}

// randomBelow returns a uniform value in [0, n) for n ≤ 256 using rejection
// sampling, so the digits of a code are unbiased.
func randomBelow(n byte) byte {
	limit := byte(256 - (256 % int(n))) //nolint:gosec // n ≤ 10, the value is in [246, 256)
	for {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("crypto/rand indisponível: " + err.Error())
		}
		if b[0] < limit {
			return b[0] % n
		}
	}
}

// Digest returns the SHA-256 of token: what is stored for high-entropy
// tokens (session secrets, magic-link tokens).
func Digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// MAC returns HMAC-SHA256(key, parts...) with each part length-prefixed, so
// concatenation ambiguities cannot produce the same tag for different inputs.
func MAC(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(p))) //nolint:gosec // inputs are far below 4 GiB
		_, _ = m.Write(n[:])
		_, _ = m.Write(p)
	}
	return m.Sum(nil)
}

// Equal compares two byte strings in constant time (length included).
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// EqualString is Equal for strings.
func EqualString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ValidToken reports whether s looks like a RandomToken of n bytes: the
// right length and only base64url characters. Used to reject garbage before
// hashing.
func ValidToken(s string, n int) bool {
	if len(s) != base64.RawURLEncoding.EncodedLen(n) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}
