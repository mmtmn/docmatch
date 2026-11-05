// Package apix holds what every business route group of the API shares:
// public identifiers (prefix + ULID-style encoding of the database uuid),
// the authenticated principal, the pt-BR error envelope built from
// packages/contracts/errors.yaml, cursor pagination and the Idempotency-Key
// protocol of the endpoints that spend credits.
package apix

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Identifier prefixes (packages/contracts/README.md).
const (
	PrefixRun      = "run"
	PrefixUpload   = "upl"
	PrefixFinding  = "fnd"
	PrefixMatch    = "mtc"
	PrefixQuote    = "qte"
	PrefixWorkflow = "wfl"
	PrefixRule     = "rul"
	PrefixOrg      = "org"
	PrefixUser     = "usr"
	PrefixAPIKey   = "key"
)

// ErrInvalidID is returned by Decode for malformed identifiers.
var ErrInvalidID = errors.New("apix: identificador inválido")

// crockford is the ULID alphabet (no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// encodedLen is the ULID length: 128 bits in 26 characters of 5 bits.
const encodedLen = 26

var decodeTable = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xFF
	}
	for i := 0; i < len(crockford); i++ {
		t[crockford[i]] = byte(i)
	}
	// Lowercase and the usual confusables decode too (Crockford's rules).
	for i := 0; i < len(crockford); i++ {
		c := crockford[i]
		if c >= 'A' && c <= 'Z' {
			t[c+('a'-'A')] = byte(i)
		}
	}
	t['O'], t['o'] = 0, 0
	t['I'], t['i'], t['L'], t['l'] = 1, 1, 1, 1
	return t
}()

// Encode renders id as "<prefix>_<26 Crockford base32 chars>". Time-ordered
// uuids (uuidv7) stay sortable as strings.
func Encode(prefix string, id uuid.UUID) string {
	var out [encodedLen]byte
	// Walk the 128 bits from the least significant end, 5 bits at a time.
	var acc uint64
	bits := 0
	pos := encodedLen - 1
	for i := len(id) - 1; i >= 0; i-- {
		acc |= uint64(id[i]) << bits
		bits += 8
		for bits >= 5 {
			out[pos] = crockford[acc&31]
			pos--
			acc >>= 5
			bits -= 5
		}
	}
	for pos >= 0 {
		out[pos] = crockford[acc&31]
		acc >>= 5
		pos--
	}
	return prefix + "_" + string(out[:])
}

// Decode parses an identifier produced by Encode with the given prefix.
func Decode(prefix, s string) (uuid.UUID, error) {
	body, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(body) != encodedLen {
		return uuid.Nil, ErrInvalidID
	}
	// The first character carries only 3 significant bits (128 = 25*5 + 3).
	if decodeTable[body[0]] > 7 {
		return uuid.Nil, ErrInvalidID
	}
	var id uuid.UUID
	var acc uint64
	bits := 0
	pos := len(id) - 1
	for i := len(body) - 1; i >= 0; i-- {
		v := decodeTable[body[i]]
		if v == 0xFF {
			return uuid.Nil, ErrInvalidID
		}
		acc |= uint64(v) << bits
		bits += 5
		for bits >= 8 && pos >= 0 {
			id[pos] = byte(acc & 0xFF)
			pos--
			acc >>= 8
			bits -= 8
		}
	}
	return id, nil
}

// EncodePtr is Encode for optional ids (nil stays nil in JSON).
func EncodePtr(prefix string, id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := Encode(prefix, *id)
	return &s
}
