package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultIMDS is the instance metadata service of EC2.
const DefaultIMDS = "http://169.254.169.254"

// imdsTokenTTL is the lifetime asked for an IMDSv2 session token.
const imdsTokenTTL = 6 * time.Hour

// IMDS reads instance metadata with IMDSv2 (the launch template requires
// tokens). The burst worker uses it for its identity and to hear about a
// Spot interruption or an Auto Scaling termination.
type IMDS struct {
	Base   string
	Client *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewIMDS returns a client for base (DefaultIMDS on EC2). The client never
// goes through a proxy: the service is link-local.
func NewIMDS(base string) *IMDS {
	return &IMDS{Base: strings.TrimRight(base, "/"), Client: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}}
}

func (m *IMDS) session(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && time.Now().Before(m.expires) {
		return m.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, m.Base+"/latest/api/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", fmt.Sprint(int(imdsTokenTTL/time.Second)))
	resp, err := m.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("imds: token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || len(body) == 0 {
		return "", fmt.Errorf("imds: token: status %d", resp.StatusCode)
	}
	m.token, m.expires = string(body), time.Now().Add(imdsTokenTTL-time.Minute)
	return m.token, nil
}

// errNotFound is a metadata path that does not exist (no notice pending).
var errNotFound = errors.New("imds: caminho inexistente")

// Get reads one metadata path (below /latest/meta-data/).
func (m *IMDS) Get(ctx context.Context, path string) (string, error) {
	token, err := m.session(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.Base+"/latest/meta-data/"+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)
	resp, err := m.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("imds: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		return strings.TrimSpace(string(body)), nil
	case http.StatusNotFound:
		return "", errNotFound
	case http.StatusUnauthorized:
		m.mu.Lock()
		m.token = ""
		m.mu.Unlock()
	}
	return "", fmt.Errorf("imds: %s: status %d", path, resp.StatusCode)
}

// Identity is who this instance is.
type Identity struct {
	InstanceID   string
	InstanceType string
	// Lifecycle is "spot" or "on-demand".
	Lifecycle string
}

// Identity reads the instance id, type and lifecycle.
func (m *IMDS) Identity(ctx context.Context) (Identity, error) {
	id, err := m.Get(ctx, "instance-id")
	if err != nil {
		return Identity{}, err
	}
	typ, _ := m.Get(ctx, "instance-type")
	life, _ := m.Get(ctx, "instance-life-cycle")
	return Identity{InstanceID: id, InstanceType: typ, Lifecycle: life}, nil
}

// Notice reports a pending termination: a Spot interruption (two minutes'
// notice) or the Auto Scaling group taking the instance out. The empty
// string means none.
func (m *IMDS) Notice(ctx context.Context) (string, error) {
	action, err := m.Get(ctx, "spot/instance-action")
	switch {
	case err == nil && action != "":
		return "interrupção Spot: " + action, nil
	case err != nil && !errors.Is(err, errNotFound):
		return "", err
	}
	// The target state is InService from launch on; Terminated means the
	// group is taking this instance out (scale-in, health check, refresh).
	state, err := m.Get(ctx, "autoscaling/target-lifecycle-state")
	if err == nil && state == "Terminated" {
		return "grupo de Auto Scaling: " + state, nil
	}
	if err != nil && !errors.Is(err, errNotFound) {
		return "", err
	}
	return "", nil
}
