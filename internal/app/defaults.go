package app

import (
	"baton/internal/agent"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultTimeout bounds one turn including checks. Real tasks ran 15–25 min,
// so 30 min was too tight.
const DefaultTimeout = 2 * time.Hour

// Agents lists the supported sub-agents, for CLI argument detection.
var Agents = []string{"codex", "claude", "grok", "agy", "pi"}

var makeTest = regexp.MustCompile(`(?m)^test\s*:`)

// DetectChecks returns the project's own test commands. A .baton/checks file
// (one command per line, # comments) overrides detection; an empty file means
// no checks. npm is not detected: worktrees have no node_modules, so `npm test`
// would fail for a reason unrelated to the change.
func DetectChecks(root string) []string {
	if b, err := os.ReadFile(filepath.Join(root, ".baton", "checks")); err == nil {
		out := []string{}
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				out = append(out, line)
			}
		}
		return out
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	switch {
	case exists("go.mod"):
		return []string{"go vet ./...", "go test ./..."}
	case exists("Cargo.toml"):
		return []string{"cargo test"}
	}
	if b, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil && makeTest.Match(b) {
		return []string{"make test"}
	}
	return []string{}
}

// Resolve accepts a full task id, a unique prefix or suffix (the TUI shows the
// last 8 characters), or "" for the newest task.
func (s *Service) Resolve(ref string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Home, "tasks"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no tasks yet")
	}
	if ref == "" {
		sort.Strings(ids) // ids start with a UTC timestamp
		return ids[len(ids)-1], nil
	}
	var matches []string
	for _, id := range ids {
		if id == ref {
			return id, nil
		}
		if strings.HasPrefix(id, ref) || strings.HasSuffix(id, ref) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no task matches %q", ref)
	case 1:
		return matches[0], nil
	}
	sort.Strings(matches)
	if len(matches) > 5 {
		matches = append(matches[:5], "…")
	}
	return "", fmt.Errorf("%q matches several tasks: %s", ref, strings.Join(matches, ", "))
}

// agentNotes tell the main agent what each sub-agent is and how it is run.
var agentNotes = map[string]string{
	"codex":  "OpenAI Codex CLI. read: read-only sandbox; write: workspace-write sandbox, no approvals.",
	"claude": "Claude Code. read: plan mode; write: acceptEdits.",
	"grok":   "Grok CLI. read: only read_file, list_dir and grep; write: auto with a workspace sandbox.",
	"agy":    "Antigravity CLI. read: plan mode; write: accept-edits.",
	"pi":     "pi. read: read, grep, find and ls; write: default tools with no sandbox, only the git worktree isolates changes.",
}

// AgentInfo is one sub-agent the main agent may dispatch to.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Default     bool   `json:"default,omitempty"`
	Error       string `json:"error,omitempty" jsonschema:"why dispatching to it would fail, such as a missing executable"`
}

type AgentList struct {
	Agents []AgentInfo `json:"agents"`
}

// Exposed lists the agents not hidden in the config, for baton_agents.
func Exposed() (*AgentList, error) {
	cfg, _, _, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	out := &AgentList{Agents: []AgentInfo{}}
	for _, name := range Agents {
		ac := cfg.Agents[name]
		if ac.Hidden {
			continue
		}
		info := AgentInfo{Name: name, Description: agentNotes[name], Model: ac.Model, Effort: ac.Effort, Default: name == cfg.DefaultAgent()}
		if _, err := agent.Argv(name, agent.Options{Path: ac.Path}); err != nil {
			info.Error = err.Error()
		}
		out.Agents = append(out.Agents, info)
	}
	return out, nil
}

func hidden(name string) error {
	return fmt.Errorf("agent %s is hidden from the main agent (agents.%s.hidden in the Baton config); call baton_agents for the agents you may use", name, name)
}
