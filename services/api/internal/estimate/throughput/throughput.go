// Package throughput parses config/throughput.yaml: the per-kind seconds per
// unit the estimator multiplies the measured input by, the fixed overhead of
// a run, the history window those defaults give way to, the queue model and
// the thresholds of a large order and of the end-of-run e-mail. Every number
// the timing estimate applies comes from here or from the run history.
package throughput

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"
)

// Input kinds (keys of `kinds`).
const (
	KindTable       = "table"
	KindDocument    = "document"
	KindEmail       = "email"
	KindArchive     = "archive"
	KindAudio       = "audio"
	KindVideo       = "video"
	KindLLM         = "llm"
	KindNewTools    = "new_tools"
	KindWebRequests = "web_requests"
)

// Units of the kinds.
const (
	UnitMB       = "mb"
	UnitMinutes  = "minutes"
	UnitKTokens  = "ktokens"
	UnitTools    = "tools"
	UnitRequests = "requests"
)

// kindUnits is the unit each kind is measured in; the file must agree.
var kindUnits = map[string]string{
	KindTable: UnitMB, KindDocument: UnitMB, KindEmail: UnitMB, KindArchive: UnitMB,
	KindAudio: UnitMinutes, KindVideo: UnitMinutes, KindLLM: UnitKTokens, KindNewTools: UnitTools, KindWebRequests: UnitRequests,
}

// Kinds lists every kind, sorted.
func Kinds() []string {
	out := make([]string, 0, len(kindUnits))
	for k := range kindUnits {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Config is the parsed file.
type Config struct {
	OverheadSeconds    float64         `yaml:"overhead_seconds"`
	LargeAfterSeconds  int64           `yaml:"large_after_seconds"`
	NotifyAfterSeconds int64           `yaml:"notify_after_seconds"`
	History            History         `yaml:"history"`
	Queue              Queue           `yaml:"queue"`
	Kinds              map[string]Kind `yaml:"kinds"`
}

// History bounds the finished runs the per-kind rates are learned from.
type History struct {
	WindowDays int `yaml:"window_days"`
	MaxRuns    int `yaml:"max_runs"`
	MinRuns    int `yaml:"min_runs"`
}

// Queue models the wait before a run starts.
type Queue struct {
	ParallelRuns      int     `yaml:"parallel_runs"`
	DefaultRunSeconds float64 `yaml:"default_run_seconds"`
}

// Kind is one row of `kinds`.
type Kind struct {
	Unit           string  `yaml:"unit"`
	SecondsPerUnit float64 `yaml:"seconds_per_unit"`
	// MBPerMinute converts the size of a media file whose duration is
	// unknown into minutes (audio and video only).
	MBPerMinute float64 `yaml:"mb_per_minute"`
}

// Load reads and validates the file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from DM_THROUGHPUT_FILE
	if err != nil {
		return nil, fmt.Errorf("throughput: ler %s: %w", path, err)
	}
	return Parse(data)
}

// Parse parses and validates the YAML.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("throughput: yaml inválido: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Validate checks every kind is present with its unit and the numbers make sense.
func (c *Config) Validate() error {
	var problems []error
	if c.OverheadSeconds < 0 || !finite(c.OverheadSeconds) {
		problems = append(problems, errors.New("overhead_seconds precisa ser >= 0"))
	}
	if c.LargeAfterSeconds <= 0 {
		problems = append(problems, errors.New("large_after_seconds precisa ser positivo"))
	}
	if c.NotifyAfterSeconds <= 0 {
		problems = append(problems, errors.New("notify_after_seconds precisa ser positivo"))
	}
	if c.History.WindowDays <= 0 || c.History.MaxRuns <= 0 || c.History.MinRuns <= 0 {
		problems = append(problems, errors.New("history.window_days, max_runs e min_runs precisam ser positivos"))
	}
	if c.Queue.ParallelRuns <= 0 {
		problems = append(problems, errors.New("queue.parallel_runs precisa ser positivo"))
	}
	if c.Queue.DefaultRunSeconds < 0 || !finite(c.Queue.DefaultRunSeconds) {
		problems = append(problems, errors.New("queue.default_run_seconds precisa ser >= 0"))
	}
	for _, name := range Kinds() {
		k, ok := c.Kinds[name]
		if !ok {
			problems = append(problems, fmt.Errorf("kinds.%s ausente", name))
			continue
		}
		if k.Unit != kindUnits[name] {
			problems = append(problems, fmt.Errorf("kinds.%s.unit precisa ser %s", name, kindUnits[name]))
		}
		if k.SecondsPerUnit < 0 || !finite(k.SecondsPerUnit) {
			problems = append(problems, fmt.Errorf("kinds.%s.seconds_per_unit precisa ser >= 0", name))
		}
		if k.Unit == UnitMinutes && (k.MBPerMinute <= 0 || !finite(k.MBPerMinute)) {
			problems = append(problems, fmt.Errorf("kinds.%s.mb_per_minute precisa ser positivo", name))
		}
	}
	for name := range c.Kinds {
		if _, ok := kindUnits[name]; !ok {
			problems = append(problems, fmt.Errorf("kinds.%s desconhecido", name))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("throughput: %w", errors.Join(problems...))
	}
	return nil
}

// Default returns the configured seconds per unit of a kind (0 for unknown kinds).
func (c *Config) Default(kind string) float64 { return c.Kinds[kind].SecondsPerUnit }

// Window is how far back the history goes.
func (c *Config) Window() time.Duration { return time.Duration(c.History.WindowDays) * 24 * time.Hour }

// Large reports whether an expected duration makes a large order.
func (c *Config) Large(expectedSeconds int64) bool { return expectedSeconds > c.LargeAfterSeconds }
