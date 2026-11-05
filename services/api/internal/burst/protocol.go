// Package burst is the pool of on-demand workers (docs/escala.md): heavy
// runs leave the main box for Spot instances that exist only while there is
// work. The main worker keeps the run (River job, events, credits); the job
// is handed over through Postgres to the API's internal endpoint, which a
// burst worker on the pool calls to claim a signed manifest, stream the
// harness events back and report how the job ended. A burst worker never
// connects to Postgres and reads the run's inputs through short-lived URLs.
//
// This file is the wire protocol shared by both sides: keys derived from
// DM_BURST_POOL_SECRET, signed requests, signed manifests, job tokens and
// the pinned TLS key of the internal endpoint.
package burst

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProtocolVersion is the manifest version this build speaks.
const ProtocolVersion = 1

// Request headers of a signed call.
const (
	HeaderInstance  = "X-Docmatch-Burst-Instance"
	HeaderDate      = "X-Docmatch-Burst-Date"
	HeaderSignature = "X-Docmatch-Burst-Signature"
	// MaxClockSkew bounds the age of a signed request (replays outside it fail).
	MaxClockSkew = 5 * time.Minute
)

// Internal endpoint paths (the API serves them on DM_BURST_LISTEN only).
const (
	PathPrefix = "/internal/burst/v1/"
	PathClaim  = PathPrefix + "claim"
	PathHealth = PathPrefix + "health"
)

// JobPath is the path of a job action (heartbeat, events, finish, sources/<n>).
func JobPath(jobID, action string) string { return PathPrefix + "jobs/" + jobID + "/" + action }

// Errors of the protocol.
var (
	ErrBadSignature = errors.New("burst: assinatura inválida")
	ErrExpired      = errors.New("burst: manifesto ou pedido fora da validade")
	ErrWrongWorker  = errors.New("burst: manifesto emitido para outro trabalhador")
	ErrBadSecret    = errors.New("burst: DM_BURST_POOL_SECRET ausente ou curta demais")
)

// minSecretChars matches config's minimum for DM_BURST_POOL_SECRET.
const minSecretChars = 32

// Keys are derived from the pool secret, one per purpose (HKDF-SHA256), so a
// request signature can never pass as a manifest signature.
type Keys struct {
	request  []byte
	manifest []byte
	tlsKey   ed25519.PrivateKey
}

// DeriveKeys derives the request, manifest and TLS keys from the shared secret.
func DeriveKeys(secret string) (Keys, error) {
	if len(secret) < minSecretChars {
		return Keys{}, ErrBadSecret
	}
	derive := func(info string, n int) ([]byte, error) {
		return hkdf.Key(sha256.New, []byte(secret), []byte("docmatch-burst"), info, n)
	}
	req, err := derive("request-v1", 32)
	if err != nil {
		return Keys{}, err
	}
	man, err := derive("manifest-v1", 32)
	if err != nil {
		return Keys{}, err
	}
	seed, err := derive("tls-ed25519-v1", ed25519.SeedSize)
	if err != nil {
		return Keys{}, err
	}
	return Keys{request: req, manifest: man, tlsKey: ed25519.NewKeyFromSeed(seed)}, nil
}

func mac(key []byte, parts ...string) []byte {
	m := hmac.New(sha256.New, key)
	for i, p := range parts {
		if i > 0 {
			_, _ = m.Write([]byte{'\n'})
		}
		_, _ = m.Write([]byte(p))
	}
	return m.Sum(nil)
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// SignRequest adds the instance, date and signature headers to req. The
// signature covers the method, the path, the date, the instance and the body.
func (k Keys) SignRequest(req *http.Request, body []byte, instanceID string, now time.Time) {
	date := strconv.FormatInt(now.Unix(), 10)
	sig := mac(k.request, req.Method, req.URL.EscapedPath(), date, instanceID, bodyHash(body))
	req.Header.Set(HeaderInstance, instanceID)
	req.Header.Set(HeaderDate, date)
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
}

// VerifyRequest checks the signature headers against the request and its
// body and returns the instance that signed it.
func (k Keys) VerifyRequest(req *http.Request, body []byte, now time.Time) (string, error) {
	instance := req.Header.Get(HeaderInstance)
	date := req.Header.Get(HeaderDate)
	got, err := hex.DecodeString(req.Header.Get(HeaderSignature))
	if err != nil || len(got) == 0 || !validInstance(instance) {
		return "", ErrBadSignature
	}
	sec, err := strconv.ParseInt(date, 10, 64)
	if err != nil {
		return "", ErrBadSignature
	}
	if d := now.Sub(time.Unix(sec, 0)); d > MaxClockSkew || d < -MaxClockSkew {
		return "", ErrExpired
	}
	want := mac(k.request, req.Method, req.URL.EscapedPath(), date, instance, bodyHash(body))
	if !hmac.Equal(got, want) {
		return "", ErrBadSignature
	}
	return instance, nil
}

// validInstance accepts EC2 ids and the names local workers use.
func validInstance(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// Manifest is one job handed to one worker: what to run, where the inputs
// are and the token that authenticates every call about it. It is valid
// until ExpiresAt (the worker must start before) and only for InstanceID.
type Manifest struct {
	Version          int       `json:"v"`
	JobID            string    `json:"job_id"`
	RunID            string    `json:"run_id"`
	InstanceID       string    `json:"instance_id"`
	IssuedAt         time.Time `json:"issued_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	LeaseSeconds     int       `json:"lease_seconds"`
	HeartbeatSeconds int       `json:"heartbeat_seconds"`
	// Token is the bearer token of this job; the API keeps only its hash.
	Token string `json:"token"`
	// Request is the pb.RunRequest (protobuf, base64 in JSON) with the
	// deadline stamped at claim time.
	Request []byte `json:"request"`
	// Sources says where to download each source of Request, by position.
	Sources []Source `json:"sources"`
}

// Source is where one input of the run is downloaded from.
type Source struct {
	URI string `json:"uri"`
	URL string `json:"url"`
	// Bearer is true when the URL is the internal endpoint (wants the job
	// token); presigned store URLs carry their own signature.
	Bearer bool   `json:"bearer,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// SignedManifest is the claim response: the exact bytes that were signed.
type SignedManifest struct {
	Manifest  json.RawMessage `json:"manifest"`
	Signature string          `json:"signature"`
}

// SignManifest serialises and signs m.
func (k Keys) SignManifest(m Manifest) (SignedManifest, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return SignedManifest{}, err
	}
	return SignedManifest{Manifest: raw, Signature: hex.EncodeToString(mac(k.manifest, string(raw)))}, nil
}

// OpenManifest verifies the signature, the version, the validity window and
// the recipient, and returns the manifest.
func (k Keys) OpenManifest(sm SignedManifest, instanceID string, now time.Time) (Manifest, error) {
	got, err := hex.DecodeString(sm.Signature)
	if err != nil || !hmac.Equal(got, mac(k.manifest, string(sm.Manifest))) {
		return Manifest{}, ErrBadSignature
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(sm.Manifest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("burst: manifesto ilegível: %w", err)
	}
	if m.Version != ProtocolVersion {
		return Manifest{}, fmt.Errorf("burst: versão %d do manifesto não suportada (esperada %d)", m.Version, ProtocolVersion)
	}
	if now.After(m.ExpiresAt) || now.Before(m.IssuedAt.Add(-MaxClockSkew)) {
		return Manifest{}, ErrExpired
	}
	if m.InstanceID != instanceID {
		return Manifest{}, ErrWrongWorker
	}
	if m.JobID == "" || m.Token == "" || len(m.Request) == 0 {
		return Manifest{}, errors.New("burst: manifesto incompleto")
	}
	return m, nil
}

// NewToken returns a random job token and the hash the API stores.
func NewToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken is the stored form of a job token.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// BearerToken reads "Authorization: Bearer <token>".
func BearerToken(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(v, "Bearer ")
	return token, ok && token != ""
}

// SameToken compares a presented token with a stored hash in constant time.
func SameToken(token string, hash []byte) bool {
	return len(hash) == sha256.Size && subtle.ConstantTimeCompare(HashToken(token), hash) == 1
}

// PublicKey is the pinned key of the internal endpoint.
func (k Keys) PublicKey() ed25519.PublicKey {
	pub, _ := k.tlsKey.Public().(ed25519.PublicKey)
	return pub
}

// ServerTLS is the TLS configuration of the internal endpoint: a self-signed
// certificate on the key derived from the pool secret. Only a holder of the
// secret can present it, and the workers pin it (ClientTLS).
func (k Keys) ServerTLS(now time.Time) (*tls.Config, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "docmatch-burst"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, k.PublicKey(), k.tlsKey)
	if err != nil {
		return nil, fmt.Errorf("burst: certificado do endpoint interno: %w", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k.tlsKey}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}, nil
}

// ClientTLS trusts exactly the derived key: the certificate chain is not
// checked against a CA (there is none inside the VPC), the leaf key is
// compared with the key the secret derives.
func (k Keys) ClientTLS() *tls.Config {
	want := k.PublicKey()
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The chain is verified by VerifyConnection (pinned key), not by a CA.
		InsecureSkipVerify: true, //nolint:gosec // pinned public key checked in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("burst: endpoint interno sem certificado")
			}
			got, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok || !got.Equal(want) {
				return errors.New("burst: chave do endpoint interno não confere (DM_BURST_POOL_SECRET diferente?)")
			}
			return nil
		},
	}
}
