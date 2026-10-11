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
{"type":"item.completed","item":{"type":"agent_message","text":"done\nnow"}}`, []Event{{"tool", "$ cat a.txt"}, {"tool", "$ cat a.txt (exit 7)"}, {"tool", "update a.txt"}, {"message", "done now"}}},
		{"claude", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"TODO","path":"."}},{"type":"text","text":"done"}]}}`, []Event{{"tool", "Grep . TODO"}, {"message", "done"}}},
		{"grok", `{"type":"text","data":"checking "}
{"type":"text","data":"file"}
{"type":"tool_call","toolCallId":"t","toolName":"read_file","rawInput":{"target_file":"a.txt"}}
{"type":"tool_call_update","toolCallId":"t","status":"failed","rawOutput":"denied"}
{"type":"text","data":"baton-"}
{"type":"text","data":"fixture"}`, []Event{{"message", "checking file"}, {"tool", "read_file a.txt"}, {"error", `read_file a.txt failed: "denied"`}, {"message", "baton-fixture"}}},
		{"agy", `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"view_file","tool_info":{"parameters":{"AbsolutePath":"a.txt"}}}}
{"event":"step_update","step_update":{"step_type":"tool","state":"ERROR","tool_name":"view_file","tool_info":{"parameters":{"AbsolutePath":"a.txt"},"error":"denied"}}}
{"event":"step_update","step_update":{"step_type":"agent_response","state":"ACTIVE","text_delta":"baton-"}}
{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE","text_delta":"fixture\n"}}`, []Event{{"error", `view_file a.txt failed: "denied"`}, {"message", "baton-fixture"}}},
		{"pi", `{"type":"tool_execution_start","toolName":"read","args":{"path":"a.txt"}}
{"type":"message_end","message":{"role":"user","content":"ignore"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`, []Event{{"tool", "read a.txt"}, {"message", "done"}}},
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
