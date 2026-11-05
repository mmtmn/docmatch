// Package envfile generates the .env of an installation: the variables deploy/local/compose.yaml
// reads (ports, images, model mode) and the local secrets the API refuses to boot without, made
// the way tools/dev/local.sh makes them (`openssl rand -base64 48` for the session secret,
// `openssl rand -hex 32` for the signing keys). Secrets are generated once and kept on every
// re-run; everything else is rewritten from the current choices.
package envfile

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
)

// Secret variables: generated once, never rewritten.
var Secrets = []string{"DM_PG_PASSWORD", "DM_SESSION_SECRET", "DM_QUOTE_HMAC_KEY", "DM_APIKEY_PEPPER", "DM_WEBHOOK_SIGNING_KEY"}

// LLM modes (DM_LLM_MODE).
const (
	LLMOff        = "off"
	LLMLocal      = "local"
	LLMOpenRouter = "openrouter"
)

// DockerHost is how containers reach the host machine (compose maps it with host-gateway).
const DockerHost = "host.docker.internal"

// Settings are the non-secret values of the .env.
type Settings struct {
	Ports      ports.Set
	Registry   string // DM_IMAGE_REGISTRY, e.g. ghcr.io/owner
	Tag        string // DM_IMAGE_TAG
	SourceDir  string // DM_SOURCE_DIR: repository root for local builds ("" = none)
	Revision   string // DM_SOURCE_REVISION
	LLMMode    string // off | local | openrouter
	LocalLLM   string // DM_LOCAL_LLM_BASE_URL as the user typed it (localhost is rewritten)
	OpenRouter string // OPENROUTER_API_KEY
}

// Values renders the settings as variables.
func (s Settings) Values() map[string]string {
	mode := s.LLMMode
	if mode == "" {
		mode = LLMOff
	}
	v := map[string]string{
		"DM_WEB_PORT":           strconv.Itoa(s.Ports[ports.Web]),
		"DM_API_PORT":           strconv.Itoa(s.Ports[ports.API]),
		"DM_PG_PORT":            strconv.Itoa(s.Ports[ports.Postgres]),
		"DM_MAILPIT_PORT":       strconv.Itoa(s.Ports[ports.Mailpit]),
		"DM_IMAGE_REGISTRY":     s.Registry,
		"DM_IMAGE_TAG":          s.Tag,
		"DM_SOURCE_DIR":         s.SourceDir,
		"DM_SOURCE_REVISION":    s.Revision,
		"DM_LLM_MODE":           mode,
		"DM_LOCAL_LLM_BASE_URL": "",
		"OPENROUTER_API_KEY":    "",
	}
	switch mode {
	case LLMLocal:
		v["DM_LOCAL_LLM_BASE_URL"] = ContainerURL(s.LocalLLM)
	case LLMOpenRouter:
		v["OPENROUTER_API_KEY"] = s.OpenRouter
	}
	return v
}

// ContainerURL rewrites a loopback host (localhost, 127.0.0.1, ::1) to host.docker.internal, so
// a model server running on this machine is reachable from inside the containers.
func ContainerURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	host := u.Hostname()
	if host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		if p := u.Port(); p != "" {
			u.Host = net.JoinHostPort(DockerHost, p)
		} else {
			u.Host = DockerHost
		}
	}
	return u.String()
}

// ValidateLLMURL accepts an http(s) URL with a host.
func ValidateLLMURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("use um endereço http(s), ex.: http://localhost:11434/v1")
	}
	return nil
}

// Generate makes a fresh value for a secret variable.
func Generate(name string, rnd io.Reader) (string, error) {
	n := 32
	switch name {
	case "DM_SESSION_SECRET":
		n = 48
	case "DM_PG_PASSWORD":
		n = 24
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rnd, buf); err != nil {
		return "", err
	}
	if name == "DM_SESSION_SECRET" {
		return base64.StdEncoding.EncodeToString(buf), nil
	}
	return hex.EncodeToString(buf), nil
}

// Parse reads KEY=value lines (comments and blanks ignored, surrounding quotes removed).
func Parse(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		out[strings.TrimSpace(key)] = value
	}
	return out
}

// header heads every generated file.
const header = `# docmatch local (gerado pelo instalador; segredos locais, nunca saem desta máquina).
# Os segredos ficam como estão a cada execução; o resto é reescrito a partir das escolhas.
`

// Render writes the variables sorted, secrets last.
func Render(values map[string]string) []byte {
	secret := map[string]bool{}
	for _, s := range Secrets {
		secret[s] = true
	}
	var plain, secrets []string
	for k := range values {
		if secret[k] {
			secrets = append(secrets, k)
		} else {
			plain = append(plain, k)
		}
	}
	sort.Strings(plain)
	sort.Strings(secrets)
	var b strings.Builder
	b.WriteString(header)
	for _, k := range plain {
		fmt.Fprintf(&b, "%s=%s\n", k, values[k])
	}
	b.WriteString("\n# Segredos locais\n")
	for _, k := range secrets {
		fmt.Fprintf(&b, "%s=%s\n", k, values[k])
	}
	return []byte(b.String())
}

// Merge keeps the secrets already in existing (generating the missing ones with rnd) and takes
// every other value from settings. It reports which secrets were generated.
func Merge(existing map[string]string, settings Settings, rnd io.Reader) (map[string]string, []string, error) {
	out := settings.Values()
	var generated []string
	for _, name := range Secrets {
		if v := strings.TrimSpace(existing[name]); v != "" {
			out[name] = v
			continue
		}
		v, err := Generate(name, rnd)
		if err != nil {
			return nil, nil, err
		}
		out[name] = v
		generated = append(generated, name)
	}
	return out, generated, nil
}

// Write merges and writes path with mode 0600 (it holds secrets), atomically.
func Write(path string, settings Settings) ([]string, error) {
	existing := map[string]string{}
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // path is the installation's own .env
		existing = Parse(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	values, generated, err := Merge(existing, settings, rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, Render(values), 0o600); err != nil {
		return nil, err
	}
	return generated, os.Rename(tmp, path)
}

// Read loads path.
func Read(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the installation's own .env
	if err != nil {
		return nil, err
	}
	return Parse(data), nil
}
