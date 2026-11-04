package crypto

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

// Errors returned by VerifyToken.
var (
	ErrTokenInvalid = errors.New("crypto: invalid signed token")
	ErrTokenExpired = errors.New("crypto: signed token expired")
)

const (
	signedVersion = 0x01
	signedHeader  = 1 + 8 // version + expiry (unix seconds, big endian)
	macLen        = 32
)

// SignToken returns base64url(version ‖ exp ‖ payload ‖ HMAC) with the
// expiry bound into the tag, for links that must stop working at a known
// time (data export downloads).
func SignToken(key, payload []byte, exp time.Time) string {
	body := make([]byte, 0, signedHeader+len(payload)+macLen)
	body = append(body, signedVersion)
	body = binary.BigEndian.AppendUint64(body, uint64(exp.Unix())) //nolint:gosec // expiries are after 1970
	body = append(body, payload...)
	body = append(body, MAC(key, body)...)
	return base64.RawURLEncoding.EncodeToString(body)
}

// VerifyToken checks the tag and the expiry and returns the payload.
func VerifyToken(key []byte, token string, now time.Time) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < signedHeader+macLen || raw[0] != signedVersion {
		return nil, ErrTokenInvalid
	}
	body, tag := raw[:len(raw)-macLen], raw[len(raw)-macLen:]
	if !Equal(MAC(key, body), tag) {
		return nil, ErrTokenInvalid
	}
	exp := time.Unix(int64(binary.BigEndian.Uint64(body[1:signedHeader])), 0) //nolint:gosec // signed by us
	if !now.Before(exp) {
		return nil, ErrTokenExpired
	}
	payload := make([]byte, len(body)-signedHeader)
	copy(payload, body[signedHeader:])
	return payload, nil
}
