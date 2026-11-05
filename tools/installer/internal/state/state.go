// Package state keeps what an installation chose and how far it got (state.json in the
// installation folder), so running the installer again resumes instead of starting over, and
// `docmatch up/down/status/demo` know which project, ports and folder to use.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
)

// Modes.
const (
	ModeTry = "experimentar"
	ModeDev = "desenvolver"
)

// FileName is the state file inside the installation folder.
const FileName = "state.json"

// State is the content of state.json. The OpenRouter key never goes here (only in .env, 0600).
type State struct {
	Version   int       `json:"version"`
	Mode      string    `json:"mode"`
	Project   string    `json:"project"`
	Ports     ports.Set `json:"ports"`
	LLMMode   string    `json:"llm_mode"`
	LocalLLM  string    `json:"local_llm_url,omitempty"`
	Registry  string    `json:"registry"`
	Tag       string    `json:"tag"`
	SourceDir string    `json:"source_dir,omitempty"`
	// Images is how the docmatch images arrived: "pulled" or "built".
	Images string `json:"images,omitempty"`
	// Revision is the source revision the built images came from.
	Revision  string    `json:"revision,omitempty"`
	Completed []string  `json:"completed,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Path is the state file of dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads dir/state.json; ok is false when there is none.
func Load(dir string) (*State, bool, error) {
	data, err := os.ReadFile(Path(dir)) //nolint:gosec // the installation's own file
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false, err
	}
	if s.Ports == nil {
		s.Ports = ports.Defaults()
	}
	return &s, true, nil
}

// Save writes dir/state.json atomically.
func (s *State) Save(dir string) error {
	s.Version = 1
	s.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := Path(dir) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(dir))
}

// Done reports whether step finished in an earlier run.
func (s *State) Done(step string) bool {
	for _, c := range s.Completed {
		if c == step {
			return true
		}
	}
	return false
}

// Mark records step as finished.
func (s *State) Mark(step string) {
	if !s.Done(step) {
		s.Completed = append(s.Completed, step)
	}
}

// LastDone is the last finished step, or "".
func (s *State) LastDone() string {
	if len(s.Completed) == 0 {
		return ""
	}
	return s.Completed[len(s.Completed)-1]
}
