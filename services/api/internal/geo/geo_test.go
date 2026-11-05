package geo

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

const secret = "segredo-da-origem-cloudfront"

func request(headers map[string]string, remote string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if remote != "" {
		r.RemoteAddr = remote
	}
	return r
}

func TestCountrySpoofing(t *testing.T) {
	t.Parallel()
	e := New(secret)
	cases := []struct {
		name    string
		headers map[string]string
		want    string
		trusted bool
	}{
		{"sem segredo, com país", map[string]string{HeaderViewerCountry: "BR"}, Unknown, false},
		{"segredo errado", map[string]string{HeaderOriginSecret: "outro", HeaderViewerCountry: "BR"}, Unknown, false},
		{"segredo com prefixo", map[string]string{HeaderOriginSecret: secret + "x", HeaderViewerCountry: "BR"}, Unknown, false},
		{"segredo vazio", map[string]string{HeaderOriginSecret: "", HeaderViewerCountry: "BR"}, Unknown, false},
		{"segredo certo", map[string]string{HeaderOriginSecret: secret, HeaderViewerCountry: "BR"}, "BR", true},
		{"segredo certo, minúsculas", map[string]string{HeaderOriginSecret: secret, HeaderViewerCountry: " us "}, "US", true},
		{"segredo certo, sem país", map[string]string{HeaderOriginSecret: secret}, Unknown, true},
		{"segredo certo, país malformado", map[string]string{HeaderOriginSecret: secret, HeaderViewerCountry: "BRA"}, Unknown, true},
		{"segredo certo, dígitos", map[string]string{HeaderOriginSecret: secret, HeaderViewerCountry: "B1"}, Unknown, true},
		{"segredo certo, injeção", map[string]string{HeaderOriginSecret: secret, HeaderViewerCountry: "BR\r\nX: y"}, Unknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := request(tc.headers, "")
			if got := e.Trusted(r); got != tc.trusted {
				t.Fatalf("Trusted = %v, esperado %v", got, tc.trusted)
			}
			if got := e.Country(r); got != tc.want {
				t.Fatalf("Country = %q, esperado %q", got, tc.want)
			}
		})
	}
}

func TestNoSecretNeverTrusts(t *testing.T) {
	t.Parallel()
	e := New("")
	r := request(map[string]string{HeaderOriginSecret: "", HeaderViewerCountry: "BR", HeaderViewerAddress: "203.0.113.9:1"}, "198.51.100.7:555")
	if e.Trusted(r) || e.Country(r) != Unknown {
		t.Fatal("sem segredo configurado nada é confiável")
	}
	if ip := e.ClientIP(r); ip != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("ClientIP = %v, esperado o endereço TCP", ip)
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	e := New(secret)
	cases := []struct {
		name    string
		headers map[string]string
		remote  string
		want    string
	}{
		{"não confiável usa remote", map[string]string{HeaderViewerAddress: "203.0.113.9:1"}, "198.51.100.7:555", "198.51.100.7"},
		{"confiável usa viewer v4", map[string]string{HeaderOriginSecret: secret, HeaderViewerAddress: "203.0.113.9:1"}, "198.51.100.7:555", "203.0.113.9"},
		{"confiável viewer v6 sem colchetes", map[string]string{HeaderOriginSecret: secret, HeaderViewerAddress: "2001:db8::1:4321"}, "198.51.100.7:555", "2001:db8::1"},
		{"confiável viewer v6 com colchetes", map[string]string{HeaderOriginSecret: secret, HeaderViewerAddress: "[2001:db8::2]:4321"}, "198.51.100.7:555", "2001:db8::2"},
		{"confiável viewer sem porta", map[string]string{HeaderOriginSecret: secret, HeaderViewerAddress: "203.0.113.10"}, "198.51.100.7:555", "203.0.113.10"},
		{"confiável viewer mapeado v4", map[string]string{HeaderOriginSecret: secret, HeaderViewerAddress: "[::ffff:203.0.113.11]:9"}, "198.51.100.7:555", "203.0.113.11"},
		{"confiável viewer vazio cai no remote", map[string]string{HeaderOriginSecret: secret}, "198.51.100.7:555", "198.51.100.7"},
		{"confiável viewer lixo cai no remote", map[string]string{HeaderOriginSecret: secret, HeaderViewerAddress: "não-é-ip:1"}, "198.51.100.7:555", "198.51.100.7"},
		{"confiável usa o X-Real-IP do Caddy", map[string]string{HeaderOriginSecret: secret, HeaderRealIP: "203.0.113.20", HeaderViewerAddress: "203.0.113.9:1"}, "127.0.0.1:41000", "203.0.113.20"},
		{"confiável X-Real-IP v6", map[string]string{HeaderOriginSecret: secret, HeaderRealIP: "2001:db8::20"}, "127.0.0.1:41000", "2001:db8::20"},
		{"confiável X-Real-IP lixo cai no viewer", map[string]string{HeaderOriginSecret: secret, HeaderRealIP: "1.2.3", HeaderViewerAddress: "203.0.113.9:1"}, "127.0.0.1:41000", "203.0.113.9"},
		{"X-Real-IP sem segredo é ignorado", map[string]string{HeaderRealIP: "203.0.113.20"}, "127.0.0.1:41000", "127.0.0.1"},
		{"remote sem porta", nil, "198.51.100.8", "198.51.100.8"},
		{"remote v6", nil, "[2001:db8::9]:1", "2001:db8::9"},
		{"remote inválido", nil, "socket-unix", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := e.ClientIP(request(tc.headers, tc.remote))
			if tc.want == "" {
				if got.IsValid() {
					t.Fatalf("esperava endereço inválido, veio %v", got)
				}
				return
			}
			if got != netip.MustParseAddr(tc.want) {
				t.Fatalf("ClientIP = %v, esperado %s", got, tc.want)
			}
		})
	}
}

func TestCustomHeaderName(t *testing.T) {
	t.Parallel()
	e := New(secret, WithHeaderName("X-Edge-Token"), WithHeaderName(""))
	r := request(map[string]string{"X-Edge-Token": secret, HeaderViewerCountry: "BR"}, "")
	if e.Country(r) != "BR" {
		t.Fatal("nome de header customizado ignorado")
	}
	r = request(map[string]string{HeaderOriginSecret: secret, HeaderViewerCountry: "BR"}, "")
	if e.Country(r) != Unknown {
		t.Fatal("header padrão não deveria valer com nome customizado")
	}
}
