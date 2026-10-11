package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseFixtures(t *testing.T) {
	cases := []struct {
		name, file, id string
		cost           bool
	}{
		{"codex", "codex.exec.jsonl", "01a12a19-a475-7021-822e-76071a979957", false},
		{"claude", "claude.json", "f648bc13-fbec-477a-bdc2-4ac3be5f39cb", true},
		{"grok", "grok.json", "01a12a19-ad0a-7790-9d83-dcb96f90dc22", true},
		{"agy", "agy.json", "d02ba1e0-dae3-4537-9c55-91b9889535a1", false},
		{"pi", "pi.jsonl", "01a12a1a-e032-731f-8373-cdda41aa692d", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", c.file))
			if err != nil {
				t.Fatal(err)
			}
			r := Parse(c.name, b, "", 0)
			if r.Error != "" || r.SessionID != c.id || r.FinalText != "OK" || len(r.Usage) == 0 || (r.CostUSD != nil) != c.cost {
				t.Fatalf("parsed result: %+v", r)
			}
			if c.cost && *r.CostUSD <= 0 {
				t.Fatal("missing positive cost")
			}
			if r := Parse(c.name, b, "", 3); !strings.Contains(r.Error, "code 3") {
				t.Fatalf("nonzero exit: %+v", r)
			}
			for _, bad := range [][]byte{nil, []byte("not json"), []byte("{}"), append(append([]byte{}, b...), []byte("garbage")...)} {
				r := Parse(c.name, bad, "", 0)
				if r.Error == "" || r.FinalText != "" {
					t.Fatalf("malformed output must fail with empty reply: %+v", r)
				}
			}
		})
	}
}

func TestReportedErrors(t *testing.T) {
	cases := []struct{ name, output string }{
		{"codex", `{"type":"turn.failed","error":{"message":"unauthorized"}}`},
		{"claude", `{"is_error":true,"subtype":"error_max_turns","result":"stopped"}`},
		{"grok", `{"sessionId":"s","text":"partial","stopReason":"error"}`},
		{"agy", `{"conversation_id":"s","status":"FAILED","response":"denied"}`},
		{"pi", "{\"type\":\"session\",\"id\":\"s\"}\n{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[],\"stopReason\":\"error\",\"errorMessage\":\"provider error\"}}\n{\"type\":\"agent_end\",\"willRetry\":false}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r := Parse(c.name, []byte(c.output), "", 0); r.Error == "" {
				t.Fatalf("missing reported error: %+v", r)
			}
		})
	}
	if r := Parse("opencode", nil, "", 0); !strings.Contains(r.Error, "unsupported") {
		t.Fatal(r)
	}
}

func TestArgvModesAndResume(t *testing.T) {
	o := Options{Mode: "read", Dir: "/repo path", Prompt: "task", Model: "model", Effort: "high", SessionID: "session", LastFile: "/turn.last"}
	cases := []struct {
		name                string
		build               func(Options) []string
		read, write, resume []string
	}{
		{"codex", codexArgv, []string{"sandbox_mode=\"read-only\""}, []string{"sandbox_mode=\"workspace-write\""}, []string{"resume"}},
		{"claude", claudeArgv, []string{"--permission-mode", "plan"}, []string{"--permission-mode", "acceptEdits"}, []string{"--resume", "session"}},
		{"grok", grokArgv, []string{"--permission-mode", "plan"}, []string{"--permission-mode", "auto", "--sandbox", "workspace"}, []string{"-r", "session"}},
		{"agy", agyArgv, []string{"--mode", "plan"}, []string{"--mode", "accept-edits"}, []string{"--conversation", "session"}},
		{"pi", piArgv, []string{"--tools", "read,grep,find,ls"}, nil, []string{"--session", "session"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, mode := range []string{"read", "write"} {
				o.Mode = mode
				a := c.build(o)
				want := c.read
				if mode == "write" {
					want = c.write
				}
				for _, seq := range [][]string{want, c.resume} {
					found := len(seq) == 0
					for i := 0; i+len(seq) <= len(a); i++ {
						if reflect.DeepEqual(a[i:i+len(seq)], seq) {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing %v in %v", seq, a)
					}
				}
				for _, arg := range a {
					if strings.Contains(arg, "bypass") || strings.Contains(arg, "skip-permissions") || strings.Contains(arg, "always-approve") || strings.Contains(arg, "danger-full-access") {
						t.Fatal(a)
					}
				}
				if c.name == "pi" && mode == "write" && strings.Contains(strings.Join(a, " "), "--tools") {
					t.Fatal(a)
				}
			}
		})
	}
	if r := Parse("claude", []byte(`{"session_id":"s","result":"done","subtype":"success","permission_denials":[{"tool_name":"Bash"}]}`), "", 0); r.Error != "" || len(r.Warnings) != 1 {
		t.Fatalf("permission denials must warn, not fail: %+v", r)
	}
	o.Mode = "read"
	o.SessionID = ""
	if got := codexArgv(o); !reflect.DeepEqual(got, []string{"exec", "-s", "read-only", "-C", "/repo path", "--json", "-o", "/turn.last", "-m", "model", "-c", "model_reasoning_effort=\"high\"", "--", "task"}) {
		t.Fatal(got)
	}
}
