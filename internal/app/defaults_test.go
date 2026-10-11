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
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BATON_CONFIG_DIR", dir)
	t.Setenv("BATON_AGENT", "")
	if c, _, exists, err := LoadConfig(); err != nil || exists || c.DefaultAgent() != "codex" || c.TurnTimeout() != DefaultTimeout {
		t.Fatalf("missing file: %+v %v %v", c, exists, err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"agent":"claude","timeout":"30m","agents":{"claude":{"model":"opus","effort":"high","path":"/opt/claude"},"pi":{"path":"pi-dev"}}}`)
	c, _, exists, err := LoadConfig()
	if err != nil || !exists || c.DefaultAgent() != "claude" || c.TurnTimeout() != 30*time.Minute || c.Agents["claude"] != (AgentConfig{"opus", "high", "/opt/claude"}) {
		t.Fatalf("valid file: %+v %v", c, err)
	}
	t.Setenv("BATON_AGENT", "grok")
	if c.DefaultAgent() != "grok" {
		t.Error("BATON_AGENT must override the config")
	}
	for _, body := range []string{
		`{"agent":"gpt"}`,
		`{"timeout":"soon"}`,
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
