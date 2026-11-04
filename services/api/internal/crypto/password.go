package crypto

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2Params configure Argon2id. The defaults follow the OWASP second
// recommendation (64 MiB, 3 passes, 1 lane), which is what the API uses.
type Argon2Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	SaltLen   uint32
	KeyLen    uint32
}

// DefaultArgon2 is the production parameter set.
var DefaultArgon2 = Argon2Params{MemoryKiB: 64 * 1024, Time: 3, Threads: 1, SaltLen: 16, KeyLen: 32}

// argon2Version is the Argon2 version every hash declares (0x13).
const argon2Version = 19

// ErrInvalidHash is returned when an encoded hash cannot be parsed.
var ErrInvalidHash = errors.New("crypto: invalid argon2id hash")

// HashPassword hashes password with DefaultArgon2 and returns the PHC string
// ($argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>).
func HashPassword(password string) (string, error) {
	return HashPasswordWith(password, DefaultArgon2)
}

// HashPasswordWith is HashPassword with explicit parameters (tests, upgrades).
func HashPasswordWith(password string, p Argon2Params) (string, error) {
	if p.MemoryKiB == 0 || p.Time == 0 || p.Threads == 0 || p.SaltLen < 8 || p.KeyLen < 16 {
		return "", errors.New("crypto: invalid argon2 parameters")
	}
	salt := RandomBytes(int(p.SaltLen))
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2Version, p.MemoryKiB, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// ParseArgon2 decodes a PHC string into its parameters, salt and key.
func ParseArgon2(encoded string) (p Argon2Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2Version {
		return p, nil, nil, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	if p.MemoryKiB == 0 || p.Time == 0 || p.Threads == 0 || len(salt) < 8 || len(key) < 16 {
		return p, nil, nil, ErrInvalidHash
	}
	p.SaltLen = uint32(len(salt)) //nolint:gosec // bounded by the decoded slice
	p.KeyLen = uint32(len(key))   //nolint:gosec // bounded by the decoded slice
	return p, salt, key, nil
}

// VerifyPassword recomputes the hash with the parameters stored in encoded
// and compares in constant time. A malformed hash is an error, not a
// mismatch, so callers can tell corruption from a wrong password.
func VerifyPassword(encoded, password string) (bool, error) {
	p, salt, key, err := ParseArgon2(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen)
	return Equal(got, key), nil
}

// NeedsRehash reports whether encoded was produced with parameters weaker
// than DefaultArgon2 (so a successful login can upgrade it).
func NeedsRehash(encoded string) bool {
	p, _, _, err := ParseArgon2(encoded)
	if err != nil {
		return true
	}
	d := DefaultArgon2
	return p.MemoryKiB < d.MemoryKiB || p.Time < d.Time || p.KeyLen < d.KeyLen || p.SaltLen < d.SaltLen
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// DummyHash returns a valid hash of a random password, computed once. Login
// code verifies the submitted password against it when the account does not
// exist or has no password, so the response time does not reveal which.
func DummyHash() string {
	dummyOnce.Do(func() {
		h, err := HashPassword(RandomToken(24))
		if err != nil {
			panic(err)
		}
		dummyHash = h
	})
	return dummyHash
}
