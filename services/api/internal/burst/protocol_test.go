package burst

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testSecret  = "segredo-do-pool-de-teste-com-32-caracteres!"
	otherSecret = "outro-segredo-do-pool-com-mais-de-32-caracteres"
)

func mustKeys(t *testing.T, secret string) Keys {
	t.Helper()
	k, err := DeriveKeys(secret)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestDeriveKeysNeedsALongSecret(t *testing.T) {
	t.Parallel()
	if _, err := DeriveKeys("curto"); !errors.Is(err, ErrBadSecret) {
		t.Fatalf("err = %v", err)
	}
	a, b := mustKeys(t, testSecret), mustKeys(t, testSecret)
	if !a.PublicKey().Equal(b.PublicKey()) {
		t.Fatal("a mesma chave deveria derivar a mesma chave TLS")
	}
	if a.PublicKey().Equal(mustKeys(t, otherSecret).PublicKey()) {
		t.Fatal("segredos diferentes não podem derivar a mesma chave")
	}
}

func signed(t *testing.T, k Keys, method, path string, body []byte, instance string, at time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	k.SignRequest(req, body, instance, at)
	return req
}

func TestRequestSignature(t *testing.T) {
	t.Parallel()
	k := mustKeys(t, testSecret)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"instance_type":"c7g.large"}`)
	req := signed(t, k, http.MethodPost, PathClaim, body, "i-0abc", now)
	if got, err := k.VerifyRequest(req, body, now.Add(time.Minute)); err != nil || got != "i-0abc" {
		t.Fatalf("verify = %q %v", got, err)
	}
	cases := map[string]func() (*http.Request, []byte, time.Time){
		"corpo trocado": func() (*http.Request, []byte, time.Time) { return req, []byte(`{}`), now },
		"caminho trocado": func() (*http.Request, []byte, time.Time) {
			r := req.Clone(req.Context())
			r.URL.Path = PathHealth
			return r, body, now
		},
		"instância trocada": func() (*http.Request, []byte, time.Time) {
			r := req.Clone(req.Context())
			r.Header.Set(HeaderInstance, "i-0def")
			return r, body, now
		},
		"instância inválida": func() (*http.Request, []byte, time.Time) {
			r := req.Clone(req.Context())
			r.Header.Set(HeaderInstance, "i 0/def")
			return r, body, now
		},
		"assinatura não hex": func() (*http.Request, []byte, time.Time) {
			r := req.Clone(req.Context())
			r.Header.Set(HeaderSignature, "zz")
			return r, body, now
		},
		"data ilegível": func() (*http.Request, []byte, time.Time) {
			r := req.Clone(req.Context())
			r.Header.Set(HeaderDate, "ontem")
			return r, body, now
		},
		"outra chave": func() (*http.Request, []byte, time.Time) {
			return signed(t, mustKeys(t, otherSecret), http.MethodPost, PathClaim, body, "i-0abc", now), body, now
		},
	}
	for name, c := range cases {
		r, b, at := c()
		if _, err := k.VerifyRequest(r, b, at); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for _, skew := range []time.Duration{MaxClockSkew + time.Second, -MaxClockSkew - time.Second} {
		if _, err := k.VerifyRequest(req, body, now.Add(skew)); !errors.Is(err, ErrExpired) {
			t.Errorf("skew %v: err = %v", skew, err)
		}
	}
}

func testManifest(now time.Time) Manifest {
	return Manifest{
		Version: ProtocolVersion, JobID: "019a0000-0000-7000-8000-000000000001", RunID: "run_x", InstanceID: "i-0abc",
		IssuedAt: now, ExpiresAt: now.Add(DefaultManifestTTL), LeaseSeconds: 90, HeartbeatSeconds: 15,
		Token: "tok", Request: []byte{1, 2, 3}, Sources: []Source{{URI: "s3://b/k", URL: "https://s3/k?sig", Size: 3, SHA256: "ab"}},
	}
}

func TestManifestSignature(t *testing.T) {
	t.Parallel()
	k := mustKeys(t, testSecret)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	sm, err := k.SignManifest(testManifest(now))
	if err != nil {
		t.Fatal(err)
	}
	m, err := k.OpenManifest(sm, "i-0abc", now.Add(time.Minute))
	if err != nil || m.Token != "tok" || len(m.Sources) != 1 || m.Sources[0].URL != "https://s3/k?sig" {
		t.Fatalf("open = %+v %v", m, err)
	}
	// A manifest is valid only for its worker, inside its window, with its signature.
	if _, err := k.OpenManifest(sm, "i-0def", now); !errors.Is(err, ErrWrongWorker) {
		t.Fatalf("outro trabalhador: %v", err)
	}
	if _, err := k.OpenManifest(sm, "i-0abc", now.Add(DefaultManifestTTL+time.Second)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expirado: %v", err)
	}
	if _, err := k.OpenManifest(sm, "i-0abc", now.Add(-MaxClockSkew-time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("do futuro: %v", err)
	}
	if _, err := mustKeys(t, otherSecret).OpenManifest(sm, "i-0abc", now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("outra chave: %v", err)
	}
	tampered := sm
	tampered.Manifest = json.RawMessage(strings.Replace(string(sm.Manifest), `"tok"`, `"tik"`, 1))
	if _, err := k.OpenManifest(tampered, "i-0abc", now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("adulterado: %v", err)
	}
	// Even correctly signed, an unknown field, another version or a missing token is refused.
	for name, raw := range map[string]string{
		"campo desconhecido": `{"v":1,"job_id":"j","token":"t","request":"AQ==","instance_id":"i-0abc","issued_at":"2026-11-05T12:00:00Z","expires_at":"2026-11-05T13:00:00Z","x":1}`,
		"versão":             `{"v":9,"job_id":"j","token":"t","request":"AQ==","instance_id":"i-0abc","issued_at":"2026-11-05T12:00:00Z","expires_at":"2026-11-05T13:00:00Z"}`,
		"sem token":          `{"v":1,"job_id":"j","request":"AQ==","instance_id":"i-0abc","issued_at":"2026-11-05T12:00:00Z","expires_at":"2026-11-05T13:00:00Z"}`,
	} {
		forged := SignedManifest{Manifest: json.RawMessage(raw), Signature: hexMAC(k, raw)}
		if _, err := k.OpenManifest(forged, "i-0abc", now); err == nil {
			t.Errorf("%s: deveria recusar", name)
		}
	}
	if _, err := k.OpenManifest(SignedManifest{Manifest: sm.Manifest, Signature: "zz"}, "i-0abc", now); !errors.Is(err, ErrBadSignature) {
		t.Fatal("assinatura não hex")
	}
}

func hexMAC(k Keys, raw string) string { return hex.EncodeToString(mac(k.manifest, raw)) }

func TestTokens(t *testing.T) {
	t.Parallel()
	tok, hash, err := NewToken()
	if err != nil || len(tok) < 40 || len(hash) != 32 {
		t.Fatalf("token = %q %x %v", tok, hash, err)
	}
	if !SameToken(tok, hash) || SameToken(tok+"x", hash) || SameToken(tok, hash[:10]) {
		t.Fatal("comparação do token")
	}
	other, _, _ := NewToken()
	if other == tok {
		t.Fatal("tokens repetidos")
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := BearerToken(r); ok {
		t.Fatal("sem cabeçalho")
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	if got, ok := BearerToken(r); !ok || got != tok {
		t.Fatalf("bearer = %q %v", got, ok)
	}
	r.Header.Set("Authorization", "Basic abc")
	if _, ok := BearerToken(r); ok {
		t.Fatal("basic não é bearer")
	}
	if JobPath("j1", "events") != "/internal/burst/v1/jobs/j1/events" {
		t.Fatal(JobPath("j1", "events"))
	}
}

func TestPinnedTLS(t *testing.T) {
	t.Parallel()
	k := mustKeys(t, testSecret)
	cfg, err := k.ServerTLS(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	get := func(c *tls.Config) error {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: c}}
		resp, err := client.Get(srv.URL)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	if err := get(k.ClientTLS()); err != nil {
		t.Fatalf("chave fixada deveria conectar: %v", err)
	}
	if err := get(mustKeys(t, otherSecret).ClientTLS()); err == nil || !strings.Contains(err.Error(), "não confere") {
		t.Fatalf("outra chave deveria falhar: %v", err)
	}
}
