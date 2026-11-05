package estimate

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// SignatureScheme prefixes every signature (schemas/quote.json).
const SignatureScheme = "v1="

// minKeyLen is the shortest key accepted (config enforces 16 characters;
// this guards callers that bypass config).
const minKeyLen = 16

// Signer signs and verifies quotes with HMAC-SHA256. DM_QUOTE_HMAC_KEY may
// hold several keys separated by commas: the first signs, all verify, so a
// key rotation keeps quotes issued before the change valid until they expire.
type Signer struct {
	keys [][]byte
}

// NewSigner parses the comma-separated key list.
func NewSigner(spec string) (*Signer, error) {
	var keys [][]byte
	for _, k := range strings.Split(spec, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if len(k) < minKeyLen {
			return nil, errors.New("estimate: chave de assinatura curta demais")
		}
		keys = append(keys, []byte(k))
	}
	if len(keys) == 0 {
		return nil, errors.New("estimate: DM_QUOTE_HMAC_KEY vazia")
	}
	return &Signer{keys: keys}, nil
}

// Canonical is the signed string: id|fingerprint|expected|cap|expires_at.
func Canonical(q Quote) string {
	return strings.Join([]string{q.ID, q.Fingerprint, strconv.FormatInt(q.ExpectedCredits, 10), strconv.FormatInt(q.CapCredits, 10), q.ExpiresAt}, "|")
}

func sign(key []byte, canonical string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(canonical))
	return SignatureScheme + hex.EncodeToString(mac.Sum(nil))
}

// Sign returns the signature of q with the current key.
func (s *Signer) Sign(q Quote) string { return sign(s.keys[0], Canonical(q)) }

// Verify reports whether sig was produced for q by any configured key. The
// comparison is constant-time for every key, so timing reveals nothing
// about which key (if any) matched.
func (s *Signer) Verify(q Quote, sig string) bool {
	if !strings.HasPrefix(sig, SignatureScheme) || len(sig) != len(SignatureScheme)+64 {
		return false
	}
	canonical := Canonical(q)
	ok := 0
	for _, k := range s.keys {
		ok |= subtle.ConstantTimeCompare([]byte(sign(k, canonical)), []byte(sig))
	}
	return ok == 1
}
