// Package publicid renders database uuids as the prefixed identifiers of the
// public API ("usr_01JBXKEN8R4S9T0V1W2X3Y4Z5A"): a three-letter prefix, an
// underscore and the 128 bits of the uuid in Crockford base32 (26 characters,
// the ULID alphabet), which is what the OpenAPI patterns require. UUIDv7
// values stay sortable after encoding.
package publicid

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Prefixes used by the account and authentication endpoints.
const (
	PrefixUser      = "usr"
	PrefixOrg       = "org"
	PrefixChallenge = "chl"
	PrefixExport    = "exp"
)

// ErrInvalid is returned by Decode for malformed ids.
var ErrInvalid = errors.New("publicid: invalid identifier")

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// encodedLen is 128 bits in 5-bit groups, rounded up (ULID style).
const encodedLen = 26

var decodeTable = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xFF
	}
	for i := 0; i < len(alphabet); i++ {
		t[alphabet[i]] = byte(i)
		t[strings.ToLower(alphabet)[i]] = byte(i)
	}
	// Crockford aliases for commonly confused letters.
	for _, c := range "oO" {
		t[c] = 0
	}
	for _, c := range "iIlL" {
		t[c] = 1
	}
	return t
}()

// Encode renders id with the given prefix.
func Encode(prefix string, id uuid.UUID) string {
	return prefix + "_" + encode(id)
}

func encode(id uuid.UUID) string {
	var out [encodedLen]byte
	// Treat the 16 bytes as a 128-bit big-endian integer; the first symbol
	// carries 3 bits, the rest 5 bits each (26 × 5 = 130 ≥ 128).
	var n [16]byte = id
	hi := uint64(n[0])<<56 | uint64(n[1])<<48 | uint64(n[2])<<40 | uint64(n[3])<<32 | uint64(n[4])<<24 | uint64(n[5])<<16 | uint64(n[6])<<8 | uint64(n[7])
	lo := uint64(n[8])<<56 | uint64(n[9])<<48 | uint64(n[10])<<40 | uint64(n[11])<<32 | uint64(n[12])<<24 | uint64(n[13])<<16 | uint64(n[14])<<8 | uint64(n[15])
	for i := encodedLen - 1; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// Decode parses an identifier produced by Encode with the expected prefix.
func Decode(prefix, s string) (uuid.UUID, error) {
	body, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(body) != encodedLen {
		return uuid.Nil, ErrInvalid
	}
	var hi, lo uint64
	for i := 0; i < encodedLen; i++ {
		v := decodeTable[body[i]]
		if v == 0xFF {
			return uuid.Nil, ErrInvalid
		}
		if i == 0 && v > 7 {
			return uuid.Nil, ErrInvalid // overflow beyond 128 bits
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	var id uuid.UUID
	for i := 0; i < 8; i++ {
		id[i] = byte(hi >> (56 - 8*i))
		id[8+i] = byte(lo >> (56 - 8*i))
	}
	return id, nil
}
