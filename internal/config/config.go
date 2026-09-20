// Package config persists codespace-status settings and the synced codespace
// list in codespace-status.generated.json next to the executable.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// FileName is the generated config file, excluded from version control.
const FileName = "codespace-status.generated.json"

// Poll interval bounds accepted by the CLI, web UI and extension.
const (
	MinPollMS = 500
	MaxPollMS = 60000
)

// Codespace is the synced view of one codespace known to the tool.
type Codespace struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Machine     string `json:"machine,omitempty"`
	State       string `json:"state,omitempty"`
	LastSync    string `json:"last_sync,omitempty"`
}

// Config is the full persisted state of the tool.
type Config struct {
	Selected   string      `json:"selected"`
	PollMS     int         `json:"poll_ms"`
	Theme      string      `json:"theme"`
	WebPort    int         `json:"web_port"`
	SampleAll  bool        `json:"sample_all"`
	Codespaces []Codespace `json:"codespaces"`
}

// Default returns the configuration used when no file exists yet.
func Default() Config {
	return Config{
		Selected:   "",
		PollMS:     2000,
		Theme:      "light",
		WebPort:    7071,
		SampleAll:  true,
		Codespaces: []Codespace{},
	}
}

// Path returns the config path next to the running executable.
func Path() (string, error) {
	if p := os.Getenv("CODESPACE_STATUS_CONFIG"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), FileName), nil
}

// Load reads config from path, writing defaults when the file is absent.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := Default()
		return cfg, Save(path, cfg)
	}
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.Normalize()
	return cfg, nil
}

// Save writes config to path as indented JSON.
func Save(path string, cfg Config) error {
	cfg.Normalize()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Normalize clamps values that the UI or a hand edit could put out of range.
func (c *Config) Normalize() {
	if c.PollMS < MinPollMS {
		c.PollMS = MinPollMS
	}
	if c.PollMS > MaxPollMS {
		c.PollMS = MaxPollMS
	}
	if c.Theme != "dark" {
		c.Theme = "light"
	}
	if c.WebPort <= 0 || c.WebPort > 65535 {
		c.WebPort = Default().WebPort
	}
	if c.Codespaces == nil {
		c.Codespaces = []Codespace{}
	}
	if !c.Has(c.Selected) {
		c.Selected = ""
	}
}

// Has reports whether name is a known codespace.
func (c Config) Has(name string) bool {
	if name == "" {
		return false
	}
	for _, cs := range c.Codespaces {
		if cs.Name == name {
			return true
		}
	}
	return false
}

// Find returns the codespace entry for name.
func (c Config) Find(name string) (Codespace, bool) {
	for _, cs := range c.Codespaces {
		if cs.Name == name {
			return cs, true
		}
	}
	return Codespace{}, false
}

// Merge replaces the codespace list with a freshly synced one, keeping the
// selection whenever that codespace still exists.
func (c *Config) Merge(list []Codespace) {
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range list {
		list[i].LastSync = now
	}
	c.Codespaces = list
	if !c.Has(c.Selected) {
		c.Selected = ""
	}
}
