package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseFixtures(t *testing.T) {
	cases := []struct {
		name, id, text string
		cost           bool
	}{
		{"codex", "01a12aa4-3781-7181-a0c4-33d76e940f0a", "baton-fixture", false},
		{"claude", "028766dc-fc4b-48bc-91a1-e2577117a785", "baton-fixture", true},
		// The real grok sample includes an explanation after the file content.
		{"grok", "01a12aa7-8ccd-7b63-aa3d-c1af79300e27", "baton-fixture\n\n`ls -la` is not available in this session, so I listed the directory with the file tool instead. It contains one entry:\n\n- `a.txt`", true},
		{"agy", "4ab36d9f-121b-476e-8736-01035b3a657d", "baton-fixture", false},
		{"pi", "01a12aa4-36e9-7576-a665-f8ca272a545e", "baton-fixture", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "stream", c.name+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			r := Parse(c.name, b, "", 0)
			if r.Error != "" || r.SessionID != c.id || r.FinalText != c.text || len(r.Usage) == 0 || (r.CostUSD != nil) != c.cost {
				t.Fatalf("parsed result: %+v", r)
			}
			if c.cost && *r.CostUSD <= 0 {
				t.Fatal("missing positive cost")
			}
			found := false
			for _, e := range Events(c.name, b) {
				if e.Kind == "tool" && strings.Contains(e.Text, "a.txt") {
					found = true
				}
				if strings.ContainsAny(e.Text, "\n\r") {
					t.Fatalf("event must be one line: %+v", e)
				}
			}
			if !found {
				t.Fatalf("missing a.txt tool event: %+v", Events(c.name, b))
			}
			// The reader may catch the writer in the middle of a JSON record.
			Events(c.name, b[:len(b)/2])
			if r := Parse(c.name, b, "", 3); !strings.Contains(r.Error, "code 3") {
				t.Fatalf("nonzero exit: %+v", r)
			}
			// A stray warning line and a cut-off last record do not lose the turn.
			noisy := append(append([]byte("npm WARN deprecated\n"), b...), []byte("\n{\"type\":")...)
			if r := Parse(c.name, noisy, "", 0); r.Error != "" || r.SessionID != c.id || r.FinalText != c.text {
				t.Fatalf("noisy output: %+v", r)
			}
			for _, bad := range [][]byte{nil, []byte("not json"), []byte("{}")} {
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
		{"claude", `{"type":"result","is_error":true,"subtype":"error_max_turns","result":"stopped"}`},
		{"grok", `{"type":"end","sessionId":"s","stopReason":"error"}`},
		{"agy", `{"event":"result","result":{"conversation_id":"s","status":"FAILED","response":"denied"}}`},
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
		{"grok", grokArgv, []string{"--permission-mode", "plan", "--tools", "read_file,list_dir,grep"}, []string{"--permission-mode", "auto", "--sandbox", "workspace"}, []string{"-r", "session"}},
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
				if (c.name == "pi" || c.name == "grok") && mode == "write" && strings.Contains(strings.Join(a, " "), "--tools") {
					t.Fatal(a)
				}
				formats := map[string]string{"claude": "stream-json", "grok": "streaming-json", "agy": "stream-json"}
				if format := formats[c.name]; format != "" && !strings.Contains(strings.Join(a, " "), "--output-format "+format) {
					t.Fatal(a)
				}
				if c.name == "claude" && !strings.Contains(strings.Join(a, " "), "--verbose") {
					t.Fatal(a)
				}
			}
		})
	}
	if r := Parse("claude", []byte(`{"type":"result","session_id":"s","result":"done","subtype":"success","permission_denials":[{"tool_name":"Bash"}]}`), "", 0); r.Error != "" || len(r.Warnings) != 1 {
		t.Fatalf("permission denials must warn, not fail: %+v", r)
	}
	o.Mode = "read"
	o.SessionID = ""
	if got := codexArgv(o); !reflect.DeepEqual(got, []string{"exec", "-s", "read-only", "-C", "/repo path", "-c", `approval_policy="never"`, "--json", "-o", "/turn.last", "-m", "model", "-c", "model_reasoning_effort=\"high\"", "--", "-"}) {
		t.Fatal(got)
	}
	// A codex write task shares one Go cache outside its worktree; a read task cannot write.
	o.Cache = "/baton/cache"
	if got := strings.Join(codexArgv(o), " "); strings.Contains(got, "GOCACHE") {
		t.Fatal(got)
	}
	o.Mode = "write"
	if got := strings.Join(codexArgv(o), " "); !strings.Contains(got, `-c sandbox_workspace_write.writable_roots=["/baton/cache"] -c shell_environment_policy.set.GOCACHE="/baton/cache/go-build" --`) {
		t.Fatal(got)
	}
	o.Mode, o.Cache = "read", ""
	// codex and claude read the prompt from stdin, so its size is not capped by argv.
	if got := claudeArgv(o); !PromptOnStdin("codex") || !PromptOnStdin("claude") || slices.Contains(got, "task") {
		t.Fatalf("prompt on argv: %v", got)
	}
	if got, err := Argv("codex", Options{Path: "/bin/sh"}); err != nil || got[0] != "/bin/sh" {
		t.Fatalf("configured path: %v %v", got, err)
	}
	if _, err := Argv("codex", Options{Path: "/nonexistent/codex"}); err == nil {
		t.Fatal("missing configured path must fail")
	}
}

func TestCodexTransientErrorIsWarning(t *testing.T) {
	out := "{\"type\":\"thread.started\",\"thread_id\":\"s\"}\n" +
		"{\"type\":\"error\",\"message\":\"Reconnecting... 2/5 (stream disconnected before completion: tls handshake eof)\"}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}\n" +
		"{\"type\":\"turn.completed\",\"usage\":{}}\n"
	if r := Parse("codex", []byte(out), "", 0); r.Error != "" || r.FinalText != "done" || len(r.Warnings) != 1 {
		t.Fatalf("reconnect before turn.completed must warn, not fail: %+v", r)
	}
	if r := Parse("codex", []byte("{\"type\":\"thread.started\",\"thread_id\":\"s\"}\n{\"type\":\"error\",\"message\":\"unauthorized\"}\n"), "", 1); r.Error == "" {
		t.Fatal("an error without turn.completed must fail")
	}
}

// grok's write sandbox only lets go build cache under the temporary directory.
func TestEnv(t *testing.T) {
	if got := Env("grok", Options{Mode: "write"}); len(got) != 1 || got[0] != "GOCACHE="+filepath.Join(os.TempDir(), "baton-go-build") {
		t.Fatalf("grok write: %q", got)
	}
	for _, o := range []struct{ name, mode string }{{"grok", "read"}, {"codex", "write"}, {"claude", "write"}} {
		if got := Env(o.name, Options{Mode: o.mode}); got != nil {
			t.Errorf("%s %s: %q", o.name, o.mode, got)
		}
	}
}

func TestSessionWithoutResult(t *testing.T) {
	// A turn stopped before its result event can still be resumed.
	for name, out := range map[string]string{
		"claude": "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{\"type\":\"assistant\",\"session_id\":\"s\"}\n",
		// agy 1.3.4: the init event's top-level conversation_id.
		"agy": "{\"event\":\"init\",\"conversation_id\":\"s\",\"init\":{\"cwd\":\"/repo\"}}\n{\"event\":\"step_update\",\"step_update\":{\"conversation_id\":\"s\",\"step_index\":0,\"state\":\"DONE\"}}\n",
	} {
		if r := Parse(name, []byte(out), "", -1); r.SessionID != "s" || r.Error == "" {
			t.Fatalf("%s: %+v", name, r)
		}
	}
}
