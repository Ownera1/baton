package app

import (
	"baton/internal/workspace"
	"errors"
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
	Repo        bool // from the repository's .baton/agents, written by whoever wrote the repository
	Prompt      string
	Raw         []byte // the file as read, for SaveRole
	Err         error  // the file cannot be used; the role is listed with this error
}

var roleName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

var errNoDescription = errors.New("description is required; the main agent picks roles by it")

// CheckRoleName refuses a name that cannot be a role's file name.
func CheckRoleName(name string) error {
	switch {
	case !roleName.MatchString(name):
		return errors.New("role names use lowercase letters, digits, - and _")
	case slices.Contains(Agents, name):
		return fmt.Errorf("%s is an agent name, rename the role", name)
	}
	return nil
}

// RoleDirs are the global directory next to config.json, then the
// repository's .baton/agents, which replaces global roles of the same name.
func RoleDirs(dir string) []string {
	var dirs []string
	if d := globalRoleDir(); d != "" {
		dirs = append(dirs, d)
	}
	if root, err := workspace.Git(dir, "rev-parse", "--show-toplevel"); err == nil {
		dirs = append(dirs, filepath.Join(root, ".baton", "agents"))
	}
	return dirs
}

func globalRoleDir() string {
	path, err := ConfigPath()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "agents")
}

// Roles reads every role visible from dir, sorted by name.
func Roles(dir string) []Role {
	byName := map[string]Role{}
	global := globalRoleDir()
	for _, d := range RoleDirs(dir) {
		paths, _ := filepath.Glob(filepath.Join(d, "*.md"))
		for _, path := range paths {
			name := strings.TrimSuffix(filepath.Base(path), ".md")
			r := Role{Name: name, Path: path}
			if err := CheckRoleName(name); err != nil {
				r.Err = fmt.Errorf("%s: %w", path, err)
			} else {
				b, err := os.ReadFile(path)
				if err == nil {
					r, err = parseRole(name, path, b)
				}
				r.Raw, r.Err = b, err
			}
			r.Repo = d != global
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

// unsandboxed refuses a repository role that would run pi in write mode,
// which has no sandbox: a cloned repository must not pick that for itself.
func (r *Role) unsandboxed(agent, mode string) error {
	if r.Repo && agent == "pi" && mode == "write" {
		return fmt.Errorf("role %s comes from the repository (%s) and cannot run pi in write mode, which has no sandbox; copy it to %s if you trust it", r.Name, r.Path, globalRoleDir())
	}
	return nil
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
		}
		if err := r.Set(key, value); err != nil {
			return r, fmt.Errorf("%s: %w", path, err)
		}
	}
	if r.Description == "" {
		return r, fmt.Errorf("%s: %w", path, errNoDescription)
	}
	r.Prompt = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return r, nil
}

// Set assigns one front matter value; empty is the default.
func (r *Role) Set(key, value string) error {
	switch key {
	case "name":
		if value != r.Name {
			return fmt.Errorf("name %q differs from the file name %q", value, r.Name)
		}
	case "description":
		if value == "" {
			return errNoDescription
		}
		r.Description = value
	case "agent":
		if value != "" && !slices.Contains(Agents, value) {
			return fmt.Errorf("agent %q is not one of %s", value, strings.Join(Agents, ", "))
		}
		r.Agent = value
	case "model":
		r.Model = value
	case "effort":
		r.Effort = value
	case "mode":
		if value != "" && value != "read" && value != "write" {
			return errors.New("mode must be read or write")
		}
		r.Mode = value
	case "hidden":
		if value != "" && value != "true" && value != "false" {
			return errors.New("hidden must be true or false")
		}
		r.Hidden = value == "true"
	default:
		return fmt.Errorf("unknown key %q; use name, description, agent, model, effort, mode or hidden", key)
	}
	return nil
}

// SaveRole sets front matter keys in the role file at path, keeping comments,
// other keys and the body; an empty value removes the key. loaded is the file
// as read, nil for a new role; like SaveConfig it refuses an outside edit.
func SaveRole(path string, loaded []byte, set [][2]string) ([]byte, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	if err := CheckRoleName(name); err != nil {
		return nil, err
	}
	b := RoleFile(loaded, set)
	if _, err := parseRole(name, path, b); err != nil {
		return nil, err
	}
	return b, replace(path, loaded, b)
}

// RoleFile is loaded with set applied, as SaveRole writes it; a file without
// front matter is returned unchanged.
func RoleFile(loaded []byte, set [][2]string) []byte {
	lines := strings.Split(strings.ReplaceAll(string(loaded), "\r\n", "\n"), "\n")
	if loaded == nil {
		lines = []string{"---", "---", ""}
	}
	end := slices.Index(lines[1:], "---") + 1
	if lines[0] != "---" || end == 0 {
		return loaded
	}
	for _, kv := range set {
		key, value := kv[0], kv[1]
		if value != "" && (value[0] == '"' || value[0] == '\'') {
			value = `"` + value + `"` // parseRole strips one pair of quotes
		}
		i := 1 + slices.IndexFunc(lines[1:end], func(line string) bool {
			k, _, ok := strings.Cut(line, ":")
			return ok && strings.TrimSpace(k) == key
		})
		switch {
		case i > 0 && value == "":
			lines = slices.Delete(lines, i, i+1)
			end--
		case i > 0:
			lines[i] = key + ": " + value
		case value != "":
			lines = slices.Insert(lines, end, key+": "+value)
			end++
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
