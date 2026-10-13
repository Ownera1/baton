package app

import (
	"baton/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRole(t *testing.T) {
	r, err := parseRole("reviewer", "r.md", []byte("---\r\nname: reviewer\r\n# comment\r\ndescription: \"Reviews: diffs only\"\r\nagent: claude\r\nmodel: opus\r\neffort:\r\nmode: read\r\nhidden: false\r\n---\r\n\r\nCheck tests.\r\n---\r\nKeep it short.\r\n"))
	if err != nil || r.Description != "Reviews: diffs only" || r.Agent != "claude" || r.Model != "opus" || r.Effort != "" || r.Mode != "read" || r.Hidden || r.Prompt != "Check tests.\n---\nKeep it short." {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err := parseRole("x", "x.md", []byte("---\ndescription: d\nhidden: true\n---")); err != nil || !r.Hidden || r.Prompt != "" {
		t.Fatalf("no body: %+v %v", r, err)
	}
	for body, want := range map[string]string{
		"description: d\n":                        "front matter",
		"---\ndescription: d\n":                   "front matter",
		"---\nagent: claude\n---\n":               "description is required",
		"---\ndescription: d\ntools: Read\n---\n": `unknown key "tools"`,
		"---\nname: other\ndescription: d\n---\n": "differs from the file name",
		"---\ndescription: d\nagent: gpt\n---\n":  "not one of",
		"---\ndescription: d\nmode: plan\n---\n":  "read or write",
		"---\ndescription: d\nhidden: yes\n---\n": "true or false",
		"---\ndescription: |\n  long\n---\n":      "multi-line",
		"---\ndescription d\n---\n":               "not key: value",
	} {
		if _, err := parseRole("x", "x.md", []byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

func TestRolesOverrideAndErrors(t *testing.T) {
	global := t.TempDir()
	t.Setenv("BATON_CONFIG_DIR", global)
	repo := t.TempDir()
	if out, err := workspace.Git(repo, "init", "-q"); err != nil {
		t.Fatal(out, err)
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(global, "agents"), "tester.md", "---\ndescription: global\n---\n")
	write(filepath.Join(global, "agents"), "codex.md", "---\ndescription: clash\n---\n")
	write(filepath.Join(global, "agents"), "Bad Name.md", "---\ndescription: d\n---\n")
	write(filepath.Join(repo, ".baton", "agents"), "tester.md", "---\ndescription: repo\n---\nrepo rules\n")
	write(filepath.Join(repo, ".baton", "agents"), "broken.md", "no front matter")
	got := map[string]Role{}
	for _, r := range Roles(filepath.Join(repo, ".baton")) {
		got[r.Name] = r
	}
	if len(got) != 4 || got["tester"].Description != "repo" || got["tester"].Prompt != "repo rules" || got["codex"].Err == nil || got["Bad Name"].Err == nil || got["broken"].Err == nil {
		t.Fatalf("%+v", got)
	}
	if r, err := FindRole(repo, "tester"); err != nil || r.Description != "repo" {
		t.Fatalf("find: %+v %v", r, err)
	}
	if _, err := FindRole(repo, "broken"); err == nil {
		t.Fatal("broken role usable")
	}
	if _, err := FindRole(repo, "missing"); err == nil || !strings.Contains(err.Error(), "unknown agent or role") {
		t.Fatalf("missing role: %v", err)
	}
	// Outside a repository only global roles apply.
	if _, err := FindRole(global, "broken"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("repo role outside repo: %v", err)
	}
}

func TestSaveRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents", "drawer.md")
	b, err := SaveRole(path, nil, [][2]string{{"description", "Draws"}, {"agent", "codex"}, {"model", ""}})
	if err != nil || string(b) != "---\ndescription: Draws\nagent: codex\n---\n" {
		t.Fatalf("new: %q %v", b, err)
	}
	loaded := []byte("---\n# keep me\ndescription: Draws\nagent: codex\nmode: read\n---\n\nRules.\n")
	if err := os.WriteFile(path, loaded, 0600); err != nil {
		t.Fatal(err)
	}
	b, err = SaveRole(path, loaded, [][2]string{{"agent", "claude"}, {"mode", ""}, {"model", `"quoted"`}})
	if err != nil || string(b) != "---\n# keep me\ndescription: Draws\nagent: claude\nmodel: \"\"quoted\"\"\n---\n\nRules.\n" {
		t.Fatalf("edit: %q %v", b, err)
	}
	if r, err := parseRole("drawer", path, b); err != nil || r.Model != `"quoted"` || r.Agent != "claude" || r.Mode != "" || r.Prompt != "Rules." {
		t.Fatalf("reparse: %+v %v", r, err)
	}
	if _, err := SaveRole(path, loaded, [][2]string{{"agent", "gpt"}}); err == nil || !strings.Contains(err.Error(), "not one of") {
		t.Fatalf("invalid saved: %v", err)
	}
	if _, err := SaveRole(path, loaded, [][2]string{{"mode", "write"}}); err != ErrConfigChanged {
		t.Fatalf("outside edit overwritten: %v", err)
	}
	if _, err := SaveRole(filepath.Join(filepath.Dir(path), "codex.md"), nil, [][2]string{{"description", "d"}}); err == nil {
		t.Fatal("agent name accepted")
	}
}
