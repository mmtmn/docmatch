package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
)

// Errors returned by Open and UnwrapDataKey.
var (
	ErrSealedTooShort = errors.New("crypto: sealed blob too short")
	ErrOpenFailed     = errors.New("crypto: authentication failed")
	ErrBadKeyLength   = errors.New("crypto: key must be 32 bytes")
)

// DataKeyLen is the length of a tenant data key (AES-256).
const DataKeyLen = 32

// wrapVersion prefixes wrapped data keys so the format can evolve.
const wrapVersion = 0x01

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != subkeyLen {
		return nil, ErrBadKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext with AES-256-GCM under key, binding aad. The
// result is nonce ‖ ciphertext ‖ tag with a fresh random nonce.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := RandomBytes(gcm.NonceSize())
	out := make([]byte, 0, len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

// Open reverses Seal. Any modification of the blob or a different aad fails
// with ErrOpenFailed.
func Open(key, blob, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize()+gcm.Overhead() {
		return nil, ErrSealedTooShort
	}
	nonce, ct := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrOpenFailed
	}
	return pt, nil
}

// NewDataKey returns a fresh random tenant data key.
func NewDataKey() []byte { return RandomBytes(DataKeyLen) }

// WrapDataKey encrypts a tenant data key under the key-encryption key (the
// PurposeKEK subkey), binding it to the tenant id so a wrapped key copied
// between rows does not unwrap.
func WrapDataKey(kek, dataKey []byte, tenantID string) ([]byte, error) {
	if len(dataKey) != DataKeyLen {
		return nil, ErrBadKeyLength
	}
	sealed, err := Seal(kek, dataKey, []byte("tenant-key:"+tenantID))
	if err != nil {
		return nil, err
	}
	return append([]byte{wrapVersion}, sealed...), nil
}

// UnwrapDataKey reverses WrapDataKey.
func UnwrapDataKey(kek, wrapped []byte, tenantID string) ([]byte, error) {
	if len(wrapped) == 0 || wrapped[0] != wrapVersion {
		return nil, errors.New("crypto: unknown wrapped key version")
	}
	return Open(kek, wrapped[1:], []byte("tenant-key:"+tenantID))
}
