package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDetectChecks(t *testing.T) {
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		files map[string]string
		want  []string
	}{
		{map[string]string{"go.mod": "module x\n"}, []string{"go vet ./...", "go test ./..."}},
		{map[string]string{"Cargo.toml": ""}, []string{"cargo test"}},
		{map[string]string{"Makefile": "build:\n\tcc x.c\n\ntest: build\n\t./x\n"}, []string{"make test"}},
		{map[string]string{"Makefile": "build:\n\tcc x.c\n", "package.json": `{"scripts":{"test":"jest"}}`}, []string{}},
		{map[string]string{"go.mod": "", ".baton/checks": "# fast only\nmake lint\n\ngo test ./internal/...\n"}, []string{"make lint", "go test ./internal/..."}},
		{map[string]string{"go.mod": "", ".baton/checks": "# none\n"}, []string{}},
	}
	for _, c := range cases {
		dir := t.TempDir()
		for name, body := range c.files {
			write(dir, name, body)
		}
		if got := DetectChecks(dir); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %q, want %q", c.files, got, c.want)
		}
	}
}

func TestResolve(t *testing.T) {
	s := &Service{Home: t.TempDir()}
	if _, err := s.Resolve(""); err == nil {
		t.Fatal("no tasks must be an error")
	}
	ids := []string{"20261011-090000-aaaa1111bbbb2222", "20261011-100000-cccc3333dddd4444", "20261011-100000-cccc3333eeee5555"}
	for _, id := range ids {
		if err := os.MkdirAll(filepath.Join(s.Home, "tasks", id), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(s.Home, "tasks", id, "request.json"), request{Task: Task{ID: id}}); err != nil {
			t.Fatal(err)
		}
	}
	// A directory without request.json (being created, or a failed dispatch) is not a task.
	if err := os.MkdirAll(filepath.Join(s.Home, "tasks", "20261012-000000-ffff6666ffff7777"), 0o700); err != nil {
		t.Fatal(err)
	}
	if list, err := s.List(""); err != nil || len(list.Tasks) != len(ids) {
		t.Fatalf("list with a half-created task: %+v %v", list, err)
	}
	for ref, want := range map[string]string{"": ids[2], ids[0]: ids[0], "bbbb2222": ids[0], "20261011-09": ids[0], "eeee5555": ids[2]} {
		if got, err := s.Resolve(ref); err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := s.Resolve("20261011-100000"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("ambiguous prefix: %v", err)
	}
	if _, err := s.Resolve("ffff"); err == nil {
		t.Error("unknown ref must fail")
	}
	// `baton reply 22 is the answer` must not send "is the answer" to ...bbbb2222.
	if _, err := s.Resolve("22"); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("short ref: %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BATON_CONFIG_DIR", dir)
	t.Setenv("BATON_AGENT", "")
	if c, _, raw, err := LoadConfig(); err != nil || raw != nil || c.DefaultAgent() != "codex" || c.TurnTimeout() != DefaultTimeout {
		t.Fatalf("missing file: %+v %q %v", c, raw, err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"agent":"claude","timeout":"30m","theme":"dark","agents":{"claude":{"model":"opus","effort":"high","path":"/opt/claude"},"pi":{"path":"pi-dev"}}}`)
	c, _, raw, err := LoadConfig()
	if err != nil || raw == nil || c.DefaultAgent() != "claude" || c.TurnTimeout() != 30*time.Minute || c.Agents["claude"] != (AgentConfig{Model: "opus", Effort: "high", Path: "/opt/claude"}) {
		t.Fatalf("valid file: %+v %v", c, err)
	}
	t.Setenv("BATON_AGENT", "grok")
	if c.DefaultAgent() != "grok" {
		t.Error("BATON_AGENT must override the config")
	}
	for _, body := range []string{
		`{"agent":"gpt"}`,
		`{"timeout":"soon"}`,
		`{"theme":"blue"}`,
		`{"agents":{"cursor":{}}}`,
		`{"agents":{"codex":{"modle":"x"}}}`,
		`{"agents":{"codex":{"path":"bin/codex"}}}`,
		`{} {}`,
		`not json`,
	} {
		write(body)
		if _, _, _, err := LoadConfig(); err == nil {
			t.Errorf("%s: want error", body)
		}
	}
}

func TestSaveConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baton")
	t.Setenv("BATON_CONFIG_DIR", dir)
	c := Config{Timeout: "1h", Agents: map[string]AgentConfig{"codex": {Model: "m"}, "pi": {}}}
	if _, err := SaveConfig(c, []byte("{}")); err != ErrConfigChanged {
		t.Fatalf("missing file loaded as existing: %v", err)
	}
	raw, err := SaveConfig(c, nil)
	if err != nil || string(raw) != "{\n  \"timeout\": \"1h\",\n  \"agents\": {\n    \"codex\": {\n      \"model\": \"m\"\n    }\n  }\n}\n" {
		t.Fatalf("%q %v", raw, err)
	}
	if st, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", st, err)
	}
	got, _, loaded, err := LoadConfig()
	if err != nil || got.Agents["codex"].Model != "m" || string(loaded) != string(raw) {
		t.Fatalf("reload: %+v %v", got, err)
	}
	if _, err := SaveConfig(Config{Agent: "gpt"}, loaded); err == nil {
		t.Fatal("invalid config saved")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"agent":"pi"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveConfig(Config{}, loaded); err != ErrConfigChanged {
		t.Fatalf("outside edit overwritten: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
}

func TestWorkerCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/opt/homebrew/bin/baton _worker 20261014-071917-9009cb91d2b1c2cc": true,
		"baton _worker 20261014-071917-9009cb91d2b1c2cc":                   true,
		"/tmp/my tools/baton _worker 20261014-071917-9009cb91d2b1c2cc":     true,
		"python train.py _worker 3":                                        false,
		"vim notes_worker _worker.md":                                      false,
		"/usr/bin/other _worker 20261014-071917-9009cb91d2b1c2cc":          false,
	} {
		if workerCommand.MatchString(cmd) != want {
			t.Errorf("%q: want %v", cmd, want)
		}
	}
}

func TestListCache(t *testing.T) {
	s := &Service{Home: t.TempDir()}
	id := "20261011-090000-aaaa1111bbbb2222"
	dir := filepath.Join(s.Home, "tasks", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "request.json"), request{Task: Task{ID: id, Title: "one"}, Number: 1}); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"one", "one", "two", "two"} {
		if i == 2 {
			if err := writeJSON(filepath.Join(dir, "task.json"), Task{ID: id, Title: "two", Status: "succeeded", Turns: []Turn{{Number: 1}}}); err != nil {
				t.Fatal(err)
			}
		}
		// The queued turn status adds must not pile up in the cached task.
		if list, err := s.List(""); err != nil || list.Tasks[0].Title != title || len(list.Tasks[0].Turns) != 1 {
			t.Fatalf("%d: %+v %v", i, list.Tasks[0], err)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(""); err != nil || len(s.loaded) != 0 {
		t.Fatalf("removed task stays cached: %v %v", s.loaded, err)
	}
}
