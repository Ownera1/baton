package app

import (
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

// DefaultAgent is BATON_AGENT, or codex.
func DefaultAgent() string {
	if a := strings.TrimSpace(os.Getenv("BATON_AGENT")); a != "" {
		return a
	}
	return "codex"
}

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
