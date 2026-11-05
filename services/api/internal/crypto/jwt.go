package crypto

import (
	"context"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"time"
)

// JWT errors.
var (
	ErrJWTMalformed   = errors.New("crypto: malformed JWT")
	ErrJWTAlgorithm   = errors.New("crypto: unsupported JWT algorithm")
	ErrJWTSignature   = errors.New("crypto: JWT signature does not verify")
	ErrJWTExpired     = errors.New("crypto: JWT expired")
	ErrJWTNotYetValid = errors.New("crypto: JWT issued in the future")
	ErrJWTIssuer      = errors.New("crypto: JWT issuer mismatch")
	ErrJWTAudience    = errors.New("crypto: JWT audience mismatch")
	ErrJWTNonce       = errors.New("crypto: JWT nonce mismatch")
	ErrJWTNoKey       = errors.New("crypto: JWT signing key not found")
)

// maxJWTBytes bounds tokens accepted from the network.
const maxJWTBytes = 16 * 1024

// JWTHeader is the JOSE header subset the API uses.
type JWTHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
	Typ string `json:"typ,omitempty"`
}

// Audience accepts the aud claim as a string or an array of strings.
type Audience []string

// UnmarshalJSON implements json.Unmarshaler.
func (a *Audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = Audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = Audience(many)
	return nil
}

// Contains reports whether aud lists v.
func (a Audience) Contains(v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

// FlexBool accepts true, false, "true" and "false" (Apple sends strings).
type FlexBool bool

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexBool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "true":
		*f = true
	case "false", "null", "":
		*f = false
	default:
		return errors.New("crypto: boolean claim is neither true nor false")
	}
	return nil
}

// IDTokenClaims are the OpenID Connect claims the login flows read.
type IDTokenClaims struct {
	Issuer         string   `json:"iss"`
	Subject        string   `json:"sub"`
	Audience       Audience `json:"aud"`
	Expiry         int64    `json:"exp"`
	IssuedAt       int64    `json:"iat"`
	Nonce          string   `json:"nonce"`
	Email          string   `json:"email"`
	EmailVerified  FlexBool `json:"email_verified"`
	Name           string   `json:"name"`
	GivenName      string   `json:"given_name"`
	FamilyName     string   `json:"family_name"`
	IsPrivateEmail FlexBool `json:"is_private_email"`
}

// KeyFunc resolves the public key for a token header (JWKS lookup).
type KeyFunc func(ctx context.Context, kid, alg string) (stdcrypto.PublicKey, error)

// ParseJWT splits a compact JWS and decodes its header without verifying.
func ParseJWT(token string) (header JWTHeader, signingInput string, payload, sig []byte, err error) {
	if len(token) > maxJWTBytes {
		return header, "", nil, nil, ErrJWTMalformed
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return header, "", nil, nil, ErrJWTMalformed
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return header, "", nil, nil, ErrJWTMalformed
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		return header, "", nil, nil, ErrJWTMalformed
	}
	payload, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return header, "", nil, nil, ErrJWTMalformed
	}
	sig, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return header, "", nil, nil, ErrJWTMalformed
	}
	return header, parts[0] + "." + parts[1], payload, sig, nil
}

// VerifyJWT checks the signature of a compact JWS with RS256 or ES256 and
// returns the raw payload. "none" and every other algorithm are rejected
// before any key lookup.
func VerifyJWT(ctx context.Context, token string, keys KeyFunc) (JWTHeader, []byte, error) {
	header, input, payload, sig, err := ParseJWT(token)
	if err != nil {
		return header, nil, err
	}
	if header.Alg != "RS256" && header.Alg != "ES256" {
		return header, nil, ErrJWTAlgorithm
	}
	key, err := keys(ctx, header.Kid, header.Alg)
	if err != nil {
		return header, nil, err
	}
	digest := sha256.Sum256([]byte(input))
	switch k := key.(type) {
	case *rsa.PublicKey:
		if header.Alg != "RS256" || rsa.VerifyPKCS1v15(k, stdcrypto.SHA256, digest[:], sig) != nil {
			return header, nil, ErrJWTSignature
		}
	case *ecdsa.PublicKey:
		if header.Alg != "ES256" || k.Curve != elliptic.P256() || len(sig) != 64 {
			return header, nil, ErrJWTSignature
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(k, digest[:], r, s) {
			return header, nil, ErrJWTSignature
		}
	default:
		return header, nil, ErrJWTAlgorithm
	}
	return header, payload, nil
}

// IDTokenExpectations are the claim checks applied by VerifyIDToken.
type IDTokenExpectations struct {
	// Issuers lists the accepted iss values (Google uses two spellings).
	Issuers []string
	// Audience is the client id the token must be issued to.
	Audience string
	// Nonce, when non-empty, must equal the nonce claim.
	Nonce string
	// Skew tolerates clock differences (default 2 minutes).
	Skew time.Duration
	Now  time.Time
}

// VerifyIDToken verifies the signature and the standard claims of an OpenID
// Connect ID token and returns its claims.
func VerifyIDToken(ctx context.Context, token string, keys KeyFunc, exp IDTokenExpectations) (IDTokenClaims, error) {
	var claims IDTokenClaims
	_, payload, err := VerifyJWT(ctx, token, keys)
	if err != nil {
		return claims, err
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, ErrJWTMalformed
	}
	skew := exp.Skew
	if skew <= 0 {
		skew = 2 * time.Minute
	}
	now := exp.Now
	if now.IsZero() {
		now = time.Now()
	}
	issuerOK := false
	for _, iss := range exp.Issuers {
		if claims.Issuer == iss {
			issuerOK = true
		}
	}
	if !issuerOK {
		return claims, ErrJWTIssuer
	}
	if exp.Audience == "" || !claims.Audience.Contains(exp.Audience) {
		return claims, ErrJWTAudience
	}
	if claims.Expiry == 0 || now.After(time.Unix(claims.Expiry, 0).Add(skew)) {
		return claims, ErrJWTExpired
	}
	if claims.IssuedAt != 0 && time.Unix(claims.IssuedAt, 0).After(now.Add(skew)) {
		return claims, ErrJWTNotYetValid
	}
	if exp.Nonce != "" && !EqualString(claims.Nonce, exp.Nonce) {
		return claims, ErrJWTNonce
	}
	if claims.Subject == "" {
		return claims, ErrJWTMalformed
	}
	return claims, nil
}

// SignES256 produces a compact JWS with ES256 (the Apple client secret).
func SignES256(key *ecdsa.PrivateKey, kid string, claims any) (string, error) {
	if key == nil || key.Curve != elliptic.P256() {
		return "", errors.New("crypto: ES256 needs a P-256 private key")
	}
	hb, err := json.Marshal(JWTHeader{Alg: "ES256", Kid: kid, Typ: "JWT"})
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParseECPrivateKey reads a PEM-encoded P-256 private key (PKCS#8 as Apple
// issues, or SEC 1). Literal "\n" sequences are unescaped, which is how the
// key travels in a single-line environment variable.
func ParseECPrivateKey(pemText string) (*ecdsa.PrivateKey, error) {
	pemText = strings.ReplaceAll(strings.TrimSpace(pemText), `\n`, "\n")
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("crypto: private key is not PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("crypto: private key is not an EC key")
		}
		return ec, nil
	}
	ec, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("crypto: private key is neither PKCS#8 nor SEC 1")
	}
	return ec, nil
}
