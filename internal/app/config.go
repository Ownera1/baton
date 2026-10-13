package app

import (
	"baton/internal/guard"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// AgentConfig holds one agent's defaults; -model and -effort override them.
type AgentConfig struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Path is an absolute executable path or a name looked up in PATH.
	Path string `json:"path,omitempty"`
	// Hidden keeps the agent out of baton_agents and refuses it over MCP;
	// the CLI still accepts it.
	Hidden bool `json:"hidden,omitempty"`
}

// Config is the user-wide config.json. Every field is optional.
type Config struct {
	Agent   string `json:"agent,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	// Theme is the panel's light or dark palette; empty or auto follows the terminal.
	Theme string `json:"theme,omitempty"`
	// MaxParallel caps turns running at once across BATON_HOME; 0 is no limit.
	MaxParallel int `json:"max_parallel,omitempty"`
	// MemoryMB and CPUs bound each agent turn and each check; 0 is the default
	// (guard.DefaultMemoryMB, guard.DefaultCPUs).
	MemoryMB int                    `json:"memory_mb,omitempty"`
	CPUs     int                    `json:"cpus,omitempty"`
	Agents   map[string]AgentConfig `json:"agents,omitempty"`
}

// ConfigPath is BATON_CONFIG_DIR, XDG_CONFIG_HOME/baton or ~/.config/baton,
// plus config.json.
func ConfigPath() (string, error) {
	dir := os.Getenv("BATON_CONFIG_DIR")
	if dir == "" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			dir = filepath.Join(xdg, "baton")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, ".config", "baton")
		}
	}
	dir, err := filepath.Abs(dir)
	return filepath.Join(dir, "config.json"), err
}

// LoadConfig reads the config and returns the bytes it came from, nil for a
// missing file (an empty config). A broken file is an error rather than
// ignored, so a configured model never silently falls back to the agent's own.
func LoadConfig() (c Config, path string, raw []byte, err error) {
	if path, err = ConfigPath(); err != nil {
		return c, path, nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, path, nil, nil
	}
	if b == nil {
		b = []byte{}
	}
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&c); err == nil && dec.Decode(new(any)) != io.EOF {
			err = errors.New("trailing JSON")
		}
	}
	if err == nil {
		err = c.Validate()
	}
	if err != nil {
		return c, path, b, fmt.Errorf("%s: %w", path, err)
	}
	return c, path, b, nil
}

// Marshal is the file SaveConfig writes: indented, empty fields and agents omitted.
func (c Config) Marshal() ([]byte, error) {
	agents := map[string]AgentConfig{}
	for name, a := range c.Agents {
		if a != (AgentConfig{}) {
			agents[name] = a
		}
	}
	c.Agents = agents
	b, err := json.MarshalIndent(c, "", "  ")
	return append(b, '\n'), err
}

// ErrConfigChanged means the file changed after it was loaded.
var ErrConfigChanged = errors.New("file changed since it was loaded")

// SaveConfig validates c and replaces the file, omitting empty fields.
func SaveConfig(c Config, loaded []byte) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	b, err := c.Marshal()
	if err != nil {
		return nil, err
	}
	return b, replace(path, loaded, b)
}

// replace writes b to path atomically with mode 0600. It refuses when the
// file no longer holds loaded (nil for a file that did not exist), so an
// outside edit is never overwritten.
func replace(path string, loaded, b []byte) error {
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		current, err = nil, nil
	}
	if err != nil {
		return err
	}
	if (current == nil) != (loaded == nil) || !bytes.Equal(current, loaded) {
		return ErrConfigChanged
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op after the rename
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path) // CreateTemp files are already 0600
	}
	return err
}

func (c Config) Validate() error {
	if c.Agent != "" && !slices.Contains(Agents, c.Agent) {
		return fmt.Errorf("agent %q is not one of %s", c.Agent, strings.Join(Agents, ", "))
	}
	if _, err := duration(c.Timeout, DefaultTimeout); err != nil {
		return err
	}
	if !slices.Contains([]string{"", "auto", "light", "dark"}, c.Theme) {
		return fmt.Errorf("theme %q is not auto, light or dark", c.Theme)
	}
	if c.MaxParallel < 0 {
		return fmt.Errorf("max_parallel must be a whole number, 0 for no limit")
	}
	if c.MemoryMB < 0 || c.MemoryMB > 0 && c.MemoryMB < 512 {
		return fmt.Errorf("memory_mb must be at least 512, or 0 for the default %d", guard.DefaultMemoryMB)
	}
	if c.CPUs < 0 {
		return fmt.Errorf("cpus must be a whole number, 0 for the default %d", guard.DefaultCPUs)
	}
	for name, a := range c.Agents {
		if !slices.Contains(Agents, name) {
			return fmt.Errorf("agents.%s: unknown agent; use %s", name, strings.Join(Agents, ", "))
		}
		// The worker runs elsewhere, so a relative path would resolve differently.
		if strings.ContainsRune(a.Path, filepath.Separator) && !filepath.IsAbs(a.Path) {
			return fmt.Errorf("agents.%s.path must be absolute or a command name", name)
		}
	}
	return nil
}

// DefaultAgent is BATON_AGENT, the configured agent, or codex.
func (c Config) DefaultAgent() string {
	return cmp.Or(strings.TrimSpace(os.Getenv("BATON_AGENT")), c.Agent, "codex")
}

// TurnTimeout is the configured timeout, or DefaultTimeout.
func (c Config) TurnTimeout() time.Duration {
	d, _ := duration(c.Timeout, DefaultTimeout) // checked by Validate
	return d
}
