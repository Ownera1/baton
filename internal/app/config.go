package app

import (
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
}

// Config is the user-wide config.json. Every field is optional.
type Config struct {
	Agent   string                 `json:"agent,omitempty"`
	Timeout string                 `json:"timeout,omitempty"`
	Agents  map[string]AgentConfig `json:"agents,omitempty"`
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

// LoadConfig reads the config; a missing file is an empty config. A broken one
// is an error rather than ignored, so a configured model never silently falls
// back to the agent's own.
func LoadConfig() (c Config, path string, exists bool, err error) {
	if path, err = ConfigPath(); err != nil {
		return c, path, false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, path, false, nil
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
		return c, path, true, fmt.Errorf("%s: %w", path, err)
	}
	return c, path, true, nil
}

func (c Config) Validate() error {
	if c.Agent != "" && !slices.Contains(Agents, c.Agent) {
		return fmt.Errorf("agent %q is not one of %s", c.Agent, strings.Join(Agents, ", "))
	}
	if _, err := duration(c.Timeout, DefaultTimeout); err != nil {
		return err
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
