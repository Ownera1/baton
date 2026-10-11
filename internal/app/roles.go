package app

import (
	"baton/internal/workspace"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Role is a named sub-agent from a Markdown file: flat front matter for the
// agent and its defaults, and a body of instructions put before the first turn.
type Role struct {
	Name        string
	Path        string
	Description string
	Agent       string // empty means the default agent
	Model       string
	Effort      string
	Mode        string // empty allows both; read or write refuses the other
	Hidden      bool
	Prompt      string
	Err         error // the file cannot be used; the role is listed with this error
}

var roleName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// RoleDirs are the global directory next to config.json, then the
// repository's .baton/agents, which replaces global roles of the same name.
func RoleDirs(dir string) []string {
	var dirs []string
	if path, err := ConfigPath(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(path), "agents"))
	}
	if root, err := workspace.Git(dir, "rev-parse", "--show-toplevel"); err == nil {
		dirs = append(dirs, filepath.Join(root, ".baton", "agents"))
	}
	return dirs
}

// Roles reads every role visible from dir, sorted by name.
func Roles(dir string) []Role {
	byName := map[string]Role{}
	for _, d := range RoleDirs(dir) {
		paths, _ := filepath.Glob(filepath.Join(d, "*.md"))
		for _, path := range paths {
			name := strings.TrimSuffix(filepath.Base(path), ".md")
			r := Role{Name: name, Path: path}
			switch {
			case !roleName.MatchString(name):
				r.Err = fmt.Errorf("%s: role names use lowercase letters, digits, - and _", path)
			case slices.Contains(Agents, name):
				r.Err = fmt.Errorf("%s: %s is an agent name, rename the role", path, name)
			default:
				b, err := os.ReadFile(path)
				if err == nil {
					r, err = parseRole(name, path, b)
				}
				r.Err = err
			}
			byName[name] = r
		}
	}
	out := make([]Role, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FindRole is the usable role called name, visible from dir.
func FindRole(dir, name string) (*Role, error) {
	for _, r := range Roles(dir) {
		if r.Name == name {
			return &r, r.Err
		}
	}
	return nil, fmt.Errorf("unknown agent or role %q: agents are %s; roles live in %s", name, strings.Join(Agents, ", "), strings.Join(RoleDirs(dir), " and "))
}

// parseRole reads "---", one "key: value" per line, "---", then the body.
// ponytail: flat front matter only; use a YAML library if lists or multi-line values are needed.
func parseRole(name, path string, b []byte) (Role, error) {
	r := Role{Name: name, Path: path}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	end := slices.Index(lines[1:], "---") + 1
	if lines[0] != "---" || end == 0 {
		return r, fmt.Errorf("%s: start with front matter between --- lines", path)
	}
	for _, line := range lines[1:end] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if n := len(value); n >= 2 && (value[0] == '"' || value[0] == '\'') && value[n-1] == value[0] {
			value = value[1 : n-1]
		}
		switch {
		case !ok:
			return r, fmt.Errorf("%s: %q is not key: value", path, line)
		case value == "|" || value == ">":
			return r, fmt.Errorf("%s: %s: multi-line values are not supported", path, key)
		case value == "" && key != "name" && key != "description":
			continue // an empty value is the default
		}
		switch key {
		case "name":
			if value != name {
				return r, fmt.Errorf("%s: name %q differs from the file name %q", path, value, name)
			}
		case "description":
			r.Description = value
		case "agent":
			if !slices.Contains(Agents, value) {
				return r, fmt.Errorf("%s: agent %q is not one of %s", path, value, strings.Join(Agents, ", "))
			}
			r.Agent = value
		case "model":
			r.Model = value
		case "effort":
			r.Effort = value
		case "mode":
			if value != "read" && value != "write" {
				return r, fmt.Errorf("%s: mode must be read or write", path)
			}
			r.Mode = value
		case "hidden":
			if value != "true" && value != "false" {
				return r, fmt.Errorf("%s: hidden must be true or false", path)
			}
			r.Hidden = value == "true"
		default:
			return r, fmt.Errorf("%s: unknown key %q; use name, description, agent, model, effort, mode or hidden", path, key)
		}
	}
	if r.Description == "" {
		return r, fmt.Errorf("%s: description is required; the main agent picks roles by it", path)
	}
	r.Prompt = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return r, nil
}
