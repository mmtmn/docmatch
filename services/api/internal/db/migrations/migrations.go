// Package migrations embeds the SQL migrations so cmd/migrate is a single
// self-contained binary.
//
// The canonical files live in db/migrations at the repo root; go:embed cannot
// reach outside the module, so sql/ holds a synced copy (make generate in
// services/api). TestEmbeddedMatchesCanonical fails when they drift.
//
// File naming follows golang-migrate: NNNN_name.up.sql and NNNN_name.down.sql.
package migrations

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed sql/*.sql
var embedded embed.FS

// FS returns the embedded migrations rooted at the directory holding the files.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "sql")
	if err != nil {
		panic(err)
	}
	return sub
}

// Migration is one versioned pair of up/down scripts.
type Migration struct {
	Version  int
	Name     string
	Up       string
	Down     string
	Checksum string // hex SHA-256 of Up
}

var fileRe = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_-]+)\.(up|down)\.sql$`)

// Load parses every migration in fsys. Every version must have both an up
// and a down script, versions must be unique and are returned ascending.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrations: listar: %w", err)
	}
	byVersion := map[int]*Migration{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			if strings.HasSuffix(e.Name(), ".sql") {
				return nil, fmt.Errorf("migrations: nome inválido %q (esperado NNNN_nome.up.sql / .down.sql)", e.Name())
			}
			continue
		}
		version, err := strconv.Atoi(m[1])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migrations: versão inválida em %q", e.Name())
		}
		data, err := fs.ReadFile(fsys, path.Clean(e.Name()))
		if err != nil {
			return nil, fmt.Errorf("migrations: ler %s: %w", e.Name(), err)
		}
		mig := byVersion[version]
		if mig == nil {
			mig = &Migration{Version: version, Name: m[2]}
			byVersion[version] = mig
		} else if mig.Name != m[2] {
			return nil, fmt.Errorf("migrations: versão %d tem nomes diferentes (%s, %s)", version, mig.Name, m[2])
		}
		switch m[3] {
		case "up":
			if mig.Up != "" {
				return nil, fmt.Errorf("migrations: versão %d tem dois scripts up", version)
			}
			mig.Up = string(data)
		case "down":
			if mig.Down != "" {
				return nil, fmt.Errorf("migrations: versão %d tem dois scripts down", version)
			}
			mig.Down = string(data)
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, mig := range byVersion {
		if strings.TrimSpace(mig.Up) == "" {
			return nil, fmt.Errorf("migrations: versão %d (%s) sem script up", mig.Version, mig.Name)
		}
		if strings.TrimSpace(mig.Down) == "" {
			return nil, fmt.Errorf("migrations: versão %d (%s) sem script down", mig.Version, mig.Name)
		}
		sum := sha256.Sum256([]byte(mig.Up))
		mig.Checksum = hex.EncodeToString(sum[:])
		out = append(out, *mig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version != out[i-1].Version+1 {
			return nil, fmt.Errorf("migrations: lacuna entre as versões %d e %d", out[i-1].Version, out[i].Version)
		}
	}
	return out, nil
}

// MustLoad loads the embedded migrations or panics (validated by tests).
func MustLoad() []Migration {
	ms, err := Load(FS())
	if err != nil {
		panic(err)
	}
	return ms
}
