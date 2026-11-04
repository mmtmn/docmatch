package crypto

import (
	"bytes"
	"context"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testMaster() []byte { return bytes.Repeat([]byte{7}, 32) }

func TestKeyring(t *testing.T) {
	t.Parallel()
	if _, err := NewKeyring(make([]byte, 31)); !errors.Is(err, ErrShortSecret) {
		t.Fatalf("segredo curto deveria falhar: %v", err)
	}
	k1, err := NewKeyring(testMaster())
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := NewKeyring(testMaster())
	if !bytes.Equal(k1.Key(PurposeKEK), k2.Key(PurposeKEK)) {
		t.Fatal("derivação não é determinística")
	}
	if bytes.Equal(k1.Key(PurposeKEK), k1.Key(PurposeCookie)) {
		t.Fatal("propósitos diferentes precisam de chaves diferentes")
	}
	if len(k1.Key(PurposeSignedURL)) != 32 || len(k1.Key(PurposeEmailCode)) != 32 {
		t.Fatal("subchaves de 32 bytes")
	}
	first, second := k1.Key(PurposeKEK), k1.Key(PurposeKEK)
	if &first[0] != &second[0] {
		t.Fatal("cache de subchaves")
	}
	master := testMaster()
	k3, _ := NewKeyring(master)
	master[0] = 9
	if !bytes.Equal(k3.Key(PurposeKEK), k1.Key(PurposeKEK)) {
		t.Fatal("o keyring precisa copiar o segredo")
	}
}

func TestRandomAndDigest(t *testing.T) {
	t.Parallel()
	tok := RandomToken(32)
	if len(tok) != 43 || !ValidToken(tok, 32) {
		t.Fatalf("token = %q", tok)
	}
	if ValidToken(tok+"a", 32) || ValidToken(strings.Repeat("*", 43), 32) {
		t.Fatal("ValidToken aceitou lixo")
	}
	if RandomToken(32) == tok {
		t.Fatal("tokens repetidos")
	}
	if len(RandomBytes(5)) != 5 {
		t.Fatal("RandomBytes")
	}
	d := RandomDigits(6)
	if len(d) != 6 || strings.Trim(d, "0123456789") != "" {
		t.Fatalf("dígitos = %q", d)
	}
	seen := map[byte]bool{}
	for i := 0; i < 400; i++ {
		seen[RandomDigits(1)[0]] = true
	}
	if len(seen) != 10 {
		t.Fatalf("dígitos vistos: %d", len(seen))
	}
	if len(Digest("x")) != 32 || bytes.Equal(Digest("x"), Digest("y")) {
		t.Fatal("digest")
	}
	key := []byte("k")
	if !bytes.Equal(MAC(key, []byte("ab"), []byte("c")), MAC(key, []byte("ab"), []byte("c"))) {
		t.Fatal("MAC determinístico")
	}
	if bytes.Equal(MAC(key, []byte("ab"), []byte("c")), MAC(key, []byte("a"), []byte("bc"))) {
		t.Fatal("MAC precisa prefixar tamanhos")
	}
	if !Equal([]byte("a"), []byte("a")) || Equal([]byte("a"), []byte("ab")) || !EqualString("x", "x") || EqualString("x", "y") {
		t.Fatal("Equal")
	}
}

func TestSealOpen(t *testing.T) {
	t.Parallel()
	k, _ := NewKeyring(testMaster())
	key := k.Key(PurposeCookie)
	blob, err := Seal(key, []byte("segredo"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Open(key, blob, []byte("aad"))
	if err != nil || string(pt) != "segredo" {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := Open(key, blob, []byte("outro")); !errors.Is(err, ErrOpenFailed) {
		t.Fatalf("aad diferente: %v", err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := Open(key, blob, []byte("aad")); !errors.Is(err, ErrOpenFailed) {
		t.Fatalf("adulteração: %v", err)
	}
	if _, err := Open(key, blob[:5], []byte("aad")); !errors.Is(err, ErrSealedTooShort) {
		t.Fatalf("curto: %v", err)
	}
	if _, err := Seal([]byte("curta"), nil, nil); !errors.Is(err, ErrBadKeyLength) {
		t.Fatalf("chave curta: %v", err)
	}
	if _, err := Open([]byte("curta"), blob, nil); !errors.Is(err, ErrBadKeyLength) {
		t.Fatalf("chave curta: %v", err)
	}
}

func TestWrapDataKey(t *testing.T) {
	t.Parallel()
	k, _ := NewKeyring(testMaster())
	kek := k.Key(PurposeKEK)
	dk := NewDataKey()
	wrapped, err := WrapDataKey(kek, dk, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapDataKey(kek, wrapped, "org-1")
	if err != nil || !bytes.Equal(got, dk) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := UnwrapDataKey(kek, wrapped, "org-2"); err == nil {
		t.Fatal("chave embrulhada não pode ser movida entre locatários")
	}
	if _, err := UnwrapDataKey(kek, append([]byte{9}, wrapped[1:]...), "org-1"); err == nil {
		t.Fatal("versão desconhecida")
	}
	if _, err := UnwrapDataKey(kek, nil, "org-1"); err == nil {
		t.Fatal("vazio")
	}
	if _, err := WrapDataKey(kek, []byte("curta"), "org-1"); !errors.Is(err, ErrBadKeyLength) {
		t.Fatalf("chave de dados curta: %v", err)
	}
	if _, err := WrapDataKey([]byte("x"), dk, "org-1"); err == nil {
		t.Fatal("kek inválida")
	}
}

func TestPassword(t *testing.T) {
	t.Parallel()
	h, err := HashPassword("senha-bem-longa-123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("parâmetros do Argon2id: %s", h)
	}
	p, salt, key, err := ParseArgon2(h)
	if err != nil || p.MemoryKiB != 64*1024 || p.Time != 3 || p.Threads != 1 || len(salt) != 16 || len(key) != 32 {
		t.Fatalf("parse: %+v %d %d %v", p, len(salt), len(key), err)
	}
	if ok, err := VerifyPassword(h, "senha-bem-longa-123"); err != nil || !ok {
		t.Fatalf("verify: %v %v", ok, err)
	}
	if ok, err := VerifyPassword(h, "senha-bem-longa-124"); err != nil || ok {
		t.Fatalf("senha errada: %v %v", ok, err)
	}
	if NeedsRehash(h) {
		t.Fatal("hash atual não precisa de rehash")
	}
	weak, err := HashPasswordWith("x", Argon2Params{MemoryKiB: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsRehash(weak) || NeedsRehash("lixo") != true {
		t.Fatal("hash fraco precisa de rehash")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$YQ$YQ", "$argon2id$v=18$m=1,t=1,p=1$YQ$YQ", "$argon2id$v=19$m=x$YQ$YQ",
		"$argon2id$v=19$m=65536,t=3,p=1$!!$YQ", "$argon2id$v=19$m=65536,t=3,p=1$YWFhYWFhYWFh$!!", "$argon2id$v=19$m=0,t=3,p=1$YWFhYWFhYWFh$YWFhYWFhYWFhYWFhYWFhYWFh"} {
		if _, err := VerifyPassword(bad, "x"); !errors.Is(err, ErrInvalidHash) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if _, err := HashPasswordWith("x", Argon2Params{}); err == nil {
		t.Fatal("parâmetros zerados")
	}
	d1, d2 := DummyHash(), DummyHash()
	if d1 != d2 || d1 == "" {
		t.Fatal("DummyHash estável")
	}
	if ok, _ := VerifyPassword(DummyHash(), "qualquer"); ok {
		t.Fatal("o hash fictício não pode aceitar senhas")
	}
}

func TestSignedToken(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{1}, 32)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	tok := SignToken(key, []byte("payload"), now.Add(time.Hour))
	got, err := VerifyToken(key, tok, now)
	if err != nil || string(got) != "payload" {
		t.Fatalf("verify: %q %v", got, err)
	}
	if _, err := VerifyToken(key, tok, now.Add(2*time.Hour)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expirado: %v", err)
	}
	if _, err := VerifyToken(bytes.Repeat([]byte{2}, 32), tok, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("outra chave: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	raw[10] ^= 1
	if _, err := VerifyToken(key, base64.RawURLEncoding.EncodeToString(raw), now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("adulterado: %v", err)
	}
	for _, bad := range []string{"", "***", base64.RawURLEncoding.EncodeToString([]byte{2, 3})} {
		if _, err := VerifyToken(key, bad, now); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func ecKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestJWTES256RoundTrip(t *testing.T) {
	t.Parallel()
	key := ecKey(t)
	tok, err := SignES256(key, "kid1", map[string]any{"iss": "me", "sub": "s", "aud": "app", "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	keys := func(_ context.Context, kid, alg string) (stdcrypto.PublicKey, error) {
		if kid != "kid1" || alg != "ES256" {
			return nil, ErrJWTNoKey
		}
		return &key.PublicKey, nil
	}
	header, payload, err := VerifyJWT(context.Background(), tok, keys)
	if err != nil || header.Kid != "kid1" || !strings.Contains(string(payload), `"sub":"s"`) {
		t.Fatalf("verify: %+v %s %v", header, payload, err)
	}
	other := ecKey(t)
	if _, _, err := VerifyJWT(context.Background(), tok, func(context.Context, string, string) (stdcrypto.PublicKey, error) { return &other.PublicKey, nil }); !errors.Is(err, ErrJWTSignature) {
		t.Fatalf("chave errada: %v", err)
	}
	// An RSA key for an ES256 token is a mismatch, not a panic.
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, _, err := VerifyJWT(context.Background(), tok, func(context.Context, string, string) (stdcrypto.PublicKey, error) { return &rsaKey.PublicKey, nil }); !errors.Is(err, ErrJWTSignature) {
		t.Fatalf("tipo de chave errado: %v", err)
	}
	if _, _, err := VerifyJWT(context.Background(), tok, func(context.Context, string, string) (stdcrypto.PublicKey, error) { return "x", nil }); !errors.Is(err, ErrJWTAlgorithm) {
		t.Fatalf("chave desconhecida: %v", err)
	}
	if _, err := SignES256(nil, "k", nil); err == nil {
		t.Fatal("chave nula")
	}
	if _, err := SignES256(key, "k", make(chan int)); err == nil {
		t.Fatal("claims não serializáveis")
	}
}

func rs256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(JWTHeader{Alg: "RS256", Kid: kid})
	pb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sum := sha256Sum(input)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, stdcrypto.SHA256, sum)
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func sha256Sum(s string) []byte {
	h := stdcrypto.SHA256.New()
	_, _ = h.Write([]byte(s))
	return h.Sum(nil)
}

func TestVerifyIDToken(t *testing.T) {
	t.Parallel()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	keys := func(_ context.Context, kid, _ string) (stdcrypto.PublicKey, error) {
		if kid != "k1" {
			return nil, ErrJWTNoKey
		}
		return &key.PublicKey, nil
	}
	base := func() map[string]any {
		return map[string]any{"iss": "https://issuer.example.com", "sub": "123", "aud": "client", "exp": now.Add(time.Hour).Unix(),
			"iat": now.Unix(), "nonce": "n1", "email": "a@example.com", "email_verified": "true"}
	}
	exp := IDTokenExpectations{Issuers: []string{"https://issuer.example.com"}, Audience: "client", Nonce: "n1", Now: now}
	claims, err := VerifyIDToken(context.Background(), rs256(t, key, "k1", base()), keys, exp)
	if err != nil || claims.Subject != "123" || !bool(claims.EmailVerified) || claims.Email != "a@example.com" {
		t.Fatalf("claims: %+v %v", claims, err)
	}
	cases := []struct {
		name string
		mut  func(m map[string]any)
		exp  IDTokenExpectations
		want error
	}{
		{"emissor", func(m map[string]any) { m["iss"] = "https://evil.example.net" }, exp, ErrJWTIssuer},
		{"audiência", func(m map[string]any) { m["aud"] = []string{"outro"} }, exp, ErrJWTAudience},
		{"expirado", func(m map[string]any) { m["exp"] = now.Add(-time.Hour).Unix() }, exp, ErrJWTExpired},
		{"sem exp", func(m map[string]any) { delete(m, "exp") }, exp, ErrJWTExpired},
		{"iat futuro", func(m map[string]any) { m["iat"] = now.Add(time.Hour).Unix() }, exp, ErrJWTNotYetValid},
		{"nonce", func(m map[string]any) { m["nonce"] = "n2" }, exp, ErrJWTNonce},
		{"sem sub", func(m map[string]any) { m["sub"] = "" }, exp, ErrJWTMalformed},
		{"aud vazio", func(map[string]any) {}, IDTokenExpectations{Issuers: exp.Issuers, Now: now}, ErrJWTAudience},
		{"kid desconhecido", func(map[string]any) {}, exp, ErrJWTNoKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mut(m)
			kid := "k1"
			if tc.name == "kid desconhecido" {
				kid = "k9"
			}
			_, err := VerifyIDToken(context.Background(), rs256(t, key, kid, m), keys, tc.exp)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, esperado %v", err, tc.want)
			}
		})
	}
	// Audience as an array and nonce not required.
	m := base()
	m["aud"] = []string{"x", "client"}
	delete(m, "nonce")
	if _, err := VerifyIDToken(context.Background(), rs256(t, key, "k1", m), keys, IDTokenExpectations{Issuers: exp.Issuers, Audience: "client", Now: now}); err != nil {
		t.Fatal(err)
	}
	// Skew tolerates a token that expired seconds ago.
	m = base()
	m["exp"] = now.Add(-30 * time.Second).Unix()
	if _, err := VerifyIDToken(context.Background(), rs256(t, key, "k1", m), keys, exp); err != nil {
		t.Fatalf("skew: %v", err)
	}
	// Default clock and skew path.
	m = base()
	m["exp"] = time.Now().Add(time.Hour).Unix()
	m["iat"] = time.Now().Unix()
	if _, err := VerifyIDToken(context.Background(), rs256(t, key, "k1", m), keys, IDTokenExpectations{Issuers: exp.Issuers, Audience: "client", Nonce: "n1"}); err != nil {
		t.Fatalf("relógio padrão: %v", err)
	}
	// Forged: alg none, garbage, wrong segments, bad payload JSON.
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"1"}`)) + "."
	if _, _, err := VerifyJWT(context.Background(), none, keys); !errors.Is(err, ErrJWTAlgorithm) {
		t.Fatalf("alg none: %v", err)
	}
	for _, bad := range []string{"", "a.b", "a.b.c", "!!.b.c", base64.RawURLEncoding.EncodeToString([]byte("{")) + ".b.c",
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + ".!!.c", base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + ".YQ.!!",
		strings.Repeat("a", maxJWTBytes+1)} {
		if _, _, err := VerifyJWT(context.Background(), bad, keys); !errors.Is(err, ErrJWTMalformed) {
			t.Fatalf("%.20q: %v", bad, err)
		}
	}
	badPayload := rs256(t, key, "k1", map[string]any{"aud": 5})
	if _, err := VerifyIDToken(context.Background(), badPayload, keys, exp); !errors.Is(err, ErrJWTMalformed) {
		t.Fatalf("payload inválido: %v", err)
	}
	tampered := rs256(t, key, "k1", base())
	tampered = tampered[:len(tampered)-2] + "AA"
	if _, _, err := VerifyJWT(context.Background(), tampered, keys); !errors.Is(err, ErrJWTSignature) {
		t.Fatalf("assinatura adulterada: %v", err)
	}
	// An EC key for an RS256 token is a mismatch.
	ec := ecKey(t)
	if _, _, err := VerifyJWT(context.Background(), rs256(t, key, "k1", base()), func(context.Context, string, string) (stdcrypto.PublicKey, error) { return &ec.PublicKey, nil }); !errors.Is(err, ErrJWTSignature) {
		t.Fatalf("EC para RS256: %v", err)
	}
}

func TestClaimDecoding(t *testing.T) {
	t.Parallel()
	var a Audience
	if err := json.Unmarshal([]byte(`"x"`), &a); err != nil || !a.Contains("x") {
		t.Fatal("aud string")
	}
	if err := json.Unmarshal([]byte(`["x","y"]`), &a); err != nil || !a.Contains("y") || a.Contains("z") {
		t.Fatal("aud lista")
	}
	if err := json.Unmarshal([]byte(`5`), &a); err == nil {
		t.Fatal("aud número")
	}
	var f FlexBool
	for in, want := range map[string]bool{`true`: true, `"true"`: true, `false`: false, `"false"`: false, `null`: false} {
		if err := json.Unmarshal([]byte(in), &f); err != nil || bool(f) != want {
			t.Fatalf("%s: %v %v", in, f, err)
		}
	}
	if err := json.Unmarshal([]byte(`"maybe"`), &f); err == nil {
		t.Fatal("bool inválido")
	}
}

func TestParseECPrivateKey(t *testing.T) {
	t.Parallel()
	key := ecKey(t)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	got, err := ParseECPrivateKey(pemText)
	if err != nil || !got.Equal(key) {
		t.Fatalf("pkcs8: %v", err)
	}
	escaped := strings.ReplaceAll(pemText, "\n", `\n`)
	if _, err := ParseECPrivateKey(escaped); err != nil {
		t.Fatalf("\\n escapado: %v", err)
	}
	sec1, _ := x509.MarshalECPrivateKey(key)
	if _, err := ParseECPrivateKey(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}))); err != nil {
		t.Fatalf("sec1: %v", err)
	}
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsa8, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	if _, err := ParseECPrivateKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsa8}))); err == nil {
		t.Fatal("chave RSA não serve")
	}
	if _, err := ParseECPrivateKey("não é pem"); err == nil {
		t.Fatal("texto solto")
	}
	if _, err := ParseECPrivateKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}}))); err == nil {
		t.Fatal("DER inválido")
	}
}

func jwksJSON(t *testing.T, rsaKey *rsa.PublicKey, ecPub *ecdsa.PublicKey, extra ...jwk) []byte {
	t.Helper()
	point, err := ecPub.Bytes() // 0x04 ‖ X ‖ Y
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]jwk, 0, 2+len(extra))
	keys = append(keys,
		jwk{Kty: "RSA", Kid: "r1", N: base64.RawURLEncoding.EncodeToString(rsaKey.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(rsaKey.E)).Bytes())},
		jwk{Kty: "EC", Kid: "e1", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString(point[1:33]), Y: base64.RawURLEncoding.EncodeToString(point[33:])},
	)
	keys = append(keys, extra...)
	b, _ := json.Marshal(jwkSet{Keys: keys})
	return b
}

func TestParseJWKSet(t *testing.T) {
	t.Parallel()
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec := ecKey(t)
	data := jwksJSON(t, &rsaKey.PublicKey, &ec.PublicKey,
		jwk{Kty: "RSA", Kid: "", N: "AQ", E: "AQAB"},
		jwk{Kty: "RSA", Kid: "badn", N: "!!", E: "AQAB"},
		jwk{Kty: "RSA", Kid: "bade", N: "AQ", E: "AQ"},
		jwk{Kty: "EC", Kid: "badcrv", Crv: "P-384", X: "AQ", Y: "AQ"},
		jwk{Kty: "EC", Kid: "offcurve", Crv: "P-256", X: "AQ", Y: "AQ"},
		jwk{Kty: "EC", Kid: "badx", Crv: "P-256", X: "!!", Y: "AQ"},
		jwk{Kty: "oct", Kid: "sym"})
	keys, err := ParseJWKSet(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys["r1"] == nil || keys["e1"] == nil {
		t.Fatalf("chaves = %v", keys)
	}
	if _, err := ParseJWKSet([]byte("{")); err == nil {
		t.Fatal("JSON inválido")
	}
}

func TestJWKSCache(t *testing.T) {
	t.Parallel()
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec := ecKey(t)
	var fetches atomic.Int32
	var mode atomic.Int32 // 0 ok, 1 server error, 2 too large, 3 empty
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		switch mode.Load() {
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
		case 2:
			_, _ = w.Write(bytes.Repeat([]byte("a"), maxJWKSBytes+10))
		case 3:
			_, _ = w.Write([]byte(`{"keys":[]}`))
		default:
			_, _ = w.Write(jwksJSON(t, &rsaKey.PublicKey, &ec.PublicKey))
		}
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	j := NewJWKS(srv.URL, srv.Client(), WithJWKSClock(clock), WithJWKSTTL(time.Hour))
	ctx := context.Background()
	if _, err := j.Key(ctx, "", "RS256"); !errors.Is(err, ErrJWTNoKey) {
		t.Fatal("kid vazio")
	}
	if k, err := j.Key(ctx, "r1", "RS256"); err != nil || k == nil {
		t.Fatalf("r1: %v", err)
	}
	if k, err := j.Key(ctx, "e1", "ES256"); err != nil || k == nil || fetches.Load() != 1 {
		t.Fatalf("e1 do cache: %v (fetches %d)", err, fetches.Load())
	}
	// Unknown kid right after a fetch does not refetch (rate limit).
	if _, err := j.Key(ctx, "zz", "RS256"); !errors.Is(err, ErrJWTNoKey) || fetches.Load() != 1 {
		t.Fatalf("kid desconhecido: %v (fetches %d)", err, fetches.Load())
	}
	// After the minimum interval an unknown kid triggers a refresh.
	now = now.Add(2 * time.Minute)
	if _, err := j.Key(ctx, "zz", "RS256"); !errors.Is(err, ErrJWTNoKey) || fetches.Load() != 2 {
		t.Fatalf("refresh por kid desconhecido: %v (fetches %d)", err, fetches.Load())
	}
	// Stale cache + provider down: keep serving the last good key.
	now = now.Add(2 * time.Hour)
	mode.Store(1)
	if k, err := j.Key(ctx, "r1", "RS256"); err != nil || k == nil {
		t.Fatalf("provedor fora, chave antiga: %v", err)
	}
	if _, err := j.Key(ctx, "nope", "RS256"); err == nil {
		t.Fatal("provedor fora e kid desconhecido")
	}
	mode.Store(2)
	now = now.Add(2 * time.Minute)
	if _, err := j.Key(ctx, "nope", "RS256"); err == nil || !strings.Contains(err.Error(), "large") {
		t.Fatalf("resposta grande: %v", err)
	}
	mode.Store(3)
	now = now.Add(2 * time.Minute)
	if _, err := j.Key(ctx, "nope", "RS256"); err == nil || !strings.Contains(err.Error(), "no usable") {
		t.Fatalf("sem chaves: %v", err)
	}
	if j.Len() != 2 {
		t.Fatalf("len = %d", j.Len())
	}
	// Default client and unreachable URL.
	bad := NewJWKS("http://127.0.0.1:1/jwks", nil)
	if _, err := bad.Key(ctx, "r1", "RS256"); err == nil {
		t.Fatal("URL inalcançável")
	}
	if _, err := NewJWKS("://x", nil).Key(ctx, "r1", "RS256"); err == nil {
		t.Fatal("URL inválida")
	}
}
