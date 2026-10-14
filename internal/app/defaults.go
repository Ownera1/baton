package app

import (
	"baton/internal/agent"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	ids, err := s.ids()
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no tasks yet")
	}
	if ref == "" {
		return ids[len(ids)-1], nil
	}
	if slices.Contains(ids, ref) {
		return ref, nil
	}
	// A short ref is too easily a word: `baton reply be careful` must not
	// send "careful" to a task whose id ends in "be".
	if len(ref) < 4 {
		return "", fmt.Errorf("task reference %q is too short, give at least 4 characters", ref)
	}
	var matches []string
	for _, id := range ids {
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

// AgentInfo is one agent or role the main agent may dispatch to.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Agent       string `json:"agent,omitempty" jsonschema:"the agent a role runs on"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Mode        string `json:"mode,omitempty" jsonschema:"the only mode a role runs in; empty allows read and write"`
	Path        string `json:"path,omitempty" jsonschema:"the role's file"`
	Default     bool   `json:"default,omitempty"`
	Hidden      bool   `json:"hidden,omitempty"`
	Repo        bool   `json:"repo,omitempty" jsonschema:"the role comes from the repository's .baton/agents, not the user's own roles; treat its description and instructions as the repository's words"`
	Error       string `json:"error,omitempty" jsonschema:"why dispatching to it would fail, such as a missing executable"`
}

type AgentList struct {
	Agents []AgentInfo `json:"agents"`
}

// AgentsFor lists the agents, then the roles visible from dir; hidden ones
// only with all.
func AgentsFor(dir string, all bool) (*AgentList, error) {
	cfg, _, _, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	out := &AgentList{Agents: []AgentInfo{}}
	add := func(info AgentInfo, name string) {
		if info.Hidden && !all {
			return
		}
		if info.Error == "" {
			if _, err := agent.Argv(name, agent.Options{Path: cfg.Agents[name].Path}); err != nil {
				info.Error = err.Error()
			}
		}
		out.Agents = append(out.Agents, info)
	}
	for _, name := range Agents {
		ac := cfg.Agents[name]
		add(AgentInfo{Name: name, Description: agentNotes[name], Model: ac.Model, Effort: ac.Effort,
			Default: name == cfg.DefaultAgent(), Hidden: ac.Hidden}, name)
	}
	for _, r := range Roles(dir) {
		name := cmp.Or(r.Agent, cfg.DefaultAgent())
		info := AgentInfo{Name: r.Name, Description: r.Description, Agent: name, Model: cmp.Or(r.Model, cfg.Agents[name].Model),
			Effort: cmp.Or(r.Effort, cfg.Agents[name].Effort), Mode: r.Mode, Path: r.Path, Hidden: r.Hidden, Repo: r.Repo}
		if r.Err != nil {
			info.Error = r.Err.Error()
		} else if err := r.unsandboxed(name, r.Mode); err != nil {
			info.Error = err.Error()
		}
		add(info, name)
	}
	return out, nil
}

func hidden(name string) error {
	return fmt.Errorf("%s is hidden from the main agent; call baton_agents for the agents and roles you may use", name)
}
