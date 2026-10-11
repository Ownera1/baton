package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestEventsBestEffort(t *testing.T) {
	cases := []struct {
		name, output string
		want         []Event
	}{
		{"codex", `{"type":"item.started","item":{"type":"command_execution","command":"cat a.txt"}}
{"type":"item.completed","item":{"type":"command_execution","command":"cat a.txt","exit_code":7}}
{"type":"item.completed","item":{"type":"file_change","changes":[{"kind":"update","path":"a.txt"}]}}
{"type":"item.completed","item":{"type":"agent_message","text":"done\nnow"}}`, []Event{{Kind: "tool", Text: "$ cat a.txt", Category: "read"}, {Kind: "tool", Text: "$ cat a.txt", Category: "read", Right: "exit 7"}, {Kind: "tool", Text: "update a.txt", Category: "edit"}, {Kind: "message", Text: "done now", Category: "message"}}},
		{"claude", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"TODO","path":"."}},{"type":"text","text":"done"}]}}`, []Event{{Kind: "tool", Text: "Grep . TODO", Category: "search"}, {Kind: "message", Text: "done", Category: "message"}}},
		{"grok", `{"type":"text","data":"checking "}
{"type":"text","data":"file"}
{"type":"tool_call","toolCallId":"t","toolName":"read_file","rawInput":{"target_file":"a.txt"}}
{"type":"tool_call_update","toolCallId":"t","status":"failed","rawOutput":"denied"}
{"type":"text","data":"baton-"}
{"type":"text","data":"fixture"}`, []Event{{Kind: "message", Text: "checking file", Category: "message"}, {Kind: "tool", Text: "read_file a.txt", Category: "read"}, {Kind: "error", Text: `read_file a.txt failed: "denied"`, Category: "error"}, {Kind: "message", Text: "baton-fixture", Category: "message"}}},
		{"agy", `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"view_file","tool_info":{"parameters":{"AbsolutePath":"a.txt"}}}}
{"event":"step_update","step_update":{"step_type":"tool","state":"ERROR","tool_name":"view_file","tool_info":{"parameters":{"AbsolutePath":"a.txt"},"error":"denied"}}}
{"event":"step_update","step_update":{"step_type":"agent_response","state":"ACTIVE","text_delta":"baton-"}}
{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE","text_delta":"fixture\n"}}`, []Event{{Kind: "error", Text: `view_file a.txt failed: "denied"`, Category: "error"}, {Kind: "message", Text: "baton-fixture", Category: "message"}}},
		{"pi", `{"type":"tool_execution_start","toolName":"read","args":{"path":"a.txt"}}
{"type":"message_end","message":{"role":"user","content":"ignore"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`, []Event{{Kind: "tool", Text: "read a.txt", Category: "read"}, {Kind: "message", Text: "done", Category: "message"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Invalid lines, valid JSON with wrong shapes, and a half-written tail
			// must neither panic nor hide the complete events before/after them.
			input := "garbage\nnull\n[]\n{\"message\":false}\n" + c.output + "\n{\"type\":\"unfinished"
			if got := Events(c.name, []byte(input)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("events: %#v; want %#v", got, c.want)
			}
		})
	}
	if got := Events("unknown", nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestStreamCompletion(t *testing.T) {
	cases := []struct {
		name, output, text, error string
	}{
		{"claude", `{"type":"result","session_id":"s","result":"old"}
{"type":"result","session_id":"s","result":"baton-fixture"}
{"type":"system"}`, "baton-fixture", ""},
		{"agy", `{"event":"result","result":{"conversation_id":"s","status":"SUCCESS","response":"old"}}
{"event":"result","result":{"conversation_id":"s","status":"SUCCESS","response":"baton-fixture"}}`, "baton-fixture", ""},
		{"grok", `{"type":"text","data":"baton-"}
{"type":"text","data":"fixture"}
{"type":"end","sessionId":"s","stopReason":"stop"}`, "baton-fixture", ""},
		{"grok", `{"type":"text","data":"discard this"}
{"type":"tool_call"}
{"type":"text","data":"also discard this"}
{"type":"tool_call_update"}
{"type":"text","data":"baton-"}
{"type":"text","data":"fixture"}
{"type":"end","sessionId":"s","stopReason":"end_turn"}`, "baton-fixture", ""},
		{"grok", `{"type":"text","data":"partial"}
{"type":"end","sessionId":"s","stopReason":"cancelled"}`, "partial", "cancelled"},
	}
	for _, c := range cases {
		r := Parse(c.name, []byte(c.output), "", 0)
		if r.SessionID != "s" || r.FinalText != c.text || (c.error == "" && r.Error != "") || !strings.Contains(r.Error, c.error) {
			t.Fatalf("%s: %+v", c.name, r)
		}
	}
	for _, name := range []string{"claude", "grok", "agy"} {
		if r := Parse(name, []byte(`{"type":"text","data":"partial"}`), "", 0); r.Error == "" || r.FinalText != "" {
			t.Fatalf("missing completion %s: %+v", name, r)
		}
	}
}

func TestEventClassificationAndMetadata(t *testing.T) {
	for text, want := range map[string]string{
		"Read a.go": "read", "read_file a.go": "read", "view_file a.go": "read", "$ /bin/zsh -lc 'cat a.go'": "read",
		"Grep TODO": "search", "grep_search TODO": "search", "$ rg TODO": "search",
		"Edit a.go": "edit", "write_file a.go": "edit", "apply_patch x": "edit", "edit_file a.go": "edit",
		"Bash go test": "command", "run_command go test": "command", "bash go test": "command",
	} {
		if got := toolCategory(text); got != want {
			t.Errorf("%s: got %s want %s", text, got, want)
		}
	}
	for _, tc := range []struct{ name, stream, category, right string }{
		{"codex", `{"type":"item.started","item":{"id":"cmd1","type":"command_execution","command":"go test"}}
{"type":"item.completed","item":{"id":"cmd1","type":"command_execution","command":"go test","exit_code":1,"duration_ms":234}}`, "command", "exit 1 · 0.2s"},
		{"claude", `{"type":"assistant","message":{"content":[{"id":"t1","type":"tool_use","name":"Bash","input":{"command":"go test"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"tool_use_result":{"exit_code":0,"duration_ms":500}}`, "command", "exit 0 · 0.5s"},
		{"grok", `{"type":"tool_call","toolCallId":"t1","toolName":"read_file","rawInput":{"path":"a.go"}}
{"type":"tool_call_update","toolCallId":"t1","status":"done","rawOutput":{"duration_ms":123}}`, "read", "0.1s"},
		{"agy", `{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"view_file","duration_seconds":0.25,"tool_info":{"parameters":{"AbsolutePath":"a.go"}}}}`, "read", "0.2s"},
		{"pi", `{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{"command":"go test"}}
{"type":"tool_execution_end","toolCallId":"t1","toolName":"bash","result":{"exitCode":0,"durationMs":100}}`, "command", "exit 0 · 0.1s"},
	} {
		events := Events(tc.name, []byte(tc.stream))
		if len(events) != 1 || events[0].Category != tc.category || events[0].Right != tc.right {
			t.Errorf("%s: %+v", tc.name, events)
		}
	}
}
