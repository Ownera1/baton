package mcpserver

import (
	"baton/internal/app"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunTitleSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv := server("1", &app.Service{Home: t.TempDir()})
	st, ct := mcp.NewInMemoryTransports()
	session, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	conn, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The instructions must name tools and parameters that exist.
	for _, word := range []string{"baton_agents", "baton_run", "baton_wait", "baton_result", "baton_reply", "ids", "base", "group", "baton/<id>"} {
		if !strings.Contains(conn.InitializeResult().Instructions, word) {
			t.Errorf("instructions do not mention %s", word)
		}
	}
	list, err := conn.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, name := range regexp.MustCompile(`baton_\w+`).FindAllString(instructions, -1) {
		if !names[name] {
			t.Errorf("instructions name a missing tool %s", name)
		}
	}
	for _, tool := range list.Tools {
		if tool.Name != "baton_run" {
			continue
		}
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]json.RawMessage
			Required   []string
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		var title struct{ Type string }
		if err := json.Unmarshal(schema.Properties["title"], &title); err != nil {
			t.Fatal(err)
		}
		if title.Type != "string" || !strings.Contains(tool.Description, "conversation title") {
			t.Fatalf("title not exposed: %s", b)
		}
		for _, field := range schema.Required {
			if field == "title" {
				t.Fatal("title should be optional")
			}
		}
		return
	}
	t.Fatal("baton_run missing")
}

// Over MCP an empty id must not fall back to the newest task, which may be
// another session's.
func TestTaskToolsRequireID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	dir := filepath.Join(home, "tasks", "20261013-000000-aaaa1111bbbb2222")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{"task":{"id":"20261013-000000-aaaa1111bbbb2222","status":"succeeded"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conn := connect(ctx, t, home, nil, "")
	for tool, args := range map[string]map[string]any{
		"baton_status": {"id": ""}, "baton_result": {}, "baton_wait": {"timeout_seconds": 1},
		"baton_cancel": {"id": " "}, "baton_reply": {"msg": "go on"}, "baton_export": {},
	} {
		if out, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args}); err == nil && !out.IsError {
			t.Errorf("%s without id: %+v", tool, out.StructuredContent)
		}
	}
	if out, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: "baton_status", Arguments: map[string]any{"id": "bbbb2222"}}); err != nil || out.IsError {
		t.Fatalf("status by id: %+v %v", out, err)
	}
}

// connect starts a client on home; version empty is the latest protocol.
func connect(ctx context.Context, t *testing.T, home string, opts *mcp.ClientOptions, version string) *mcp.ClientSession {
	st, ct := mcp.NewInMemoryTransports()
	session, err := server("1", &app.Service{Home: home}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	conn, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, opts).Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// Tools the main agent polls return briefs; baton_result keeps one copy of
// the checks.
func TestBriefs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	id := "20261013-000000-aaaa1111bbbb2222"
	dir := filepath.Join(home, "tasks", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	check := `{"round":1,"status":"fail","notes":"0 required checks passed, 1 failed","checks":[{"id":"1","output_tail":"FAIL"}]}`
	files := map[string]string{
		"request.json": `{"task":{"id":"` + id + `"},"number":1}`,
		"task.json": `{"id":"` + id + `","agent":"codex","mode":"write","branch":"baton/` + id + `","status":"failed","instructions":"role body","verify":` + check +
			`,"turns":[{"number":1,"prompt":"fix it","final_text":"done","error":"boom","verify":` + check + `}]}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conn := connect(ctx, t, home, nil, "")
	call := func(tool string, args map[string]any) string {
		t.Helper()
		out, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || out.IsError {
			t.Fatalf("%s: %+v %v", tool, out, err)
		}
		b, _ := json.Marshal(out.StructuredContent)
		return string(b)
	}
	brief := `"agent":"codex","branch":"baton/` + id + `","check_notes":"0 required checks passed, 1 failed","check_status":"fail","error":"boom","id":"` + id + `","mode":"write","status":"failed","title":"fix it","turns":1`
	for tool, args := range map[string]map[string]any{
		"baton_status": {"id": id}, "baton_wait": {"id": id}, "baton_wait ids": {"ids": []string{id}}, "baton_list": {},
	} {
		if got := call(strings.Fields(tool)[0], args); !strings.Contains(got, brief) || strings.Contains(got, "FAIL") || strings.Contains(got, "prompt") {
			t.Errorf("%s: %s", tool, got)
		}
	}
	if got := call("baton_result", map[string]any{"id": id}); strings.Count(got, "FAIL") != 1 || strings.Contains(got, "role body") || !strings.Contains(got, `"final_text":"done"`) {
		t.Errorf("baton_result: %s", got)
	}
}

// baton_clean removes nothing until the user accepts the list the client
// shows; a client that cannot ask gets an error.
func TestCleanNeedsApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	id := "20261013-000000-aaaa1111bbbb2222"
	dir := filepath.Join(home, "tasks", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	task := `{"id":"` + id + `","title":"读一读","mode":"read","repo":"/nowhere","status":"succeeded","turns":[{"number":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{"task":`+task+`,"number":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"ids": []string{id}}
	// Codex asks within the call (2025-06-18); Claude Code answers by
	// calling again (2026-07-28).
	for _, version := range []string{"2025-06-18", ""} {
		if out, err := connect(ctx, t, home, nil, version).CallTool(ctx, &mcp.CallToolParams{Name: "baton_clean", Arguments: args}); err == nil && !out.IsError {
			t.Fatalf("%s: cleaned without asking: %+v", version, out.StructuredContent)
		}
		var asked string
		answer := func(content map[string]any) *mcp.ClientSession {
			return connect(ctx, t, home, &mcp.ClientOptions{ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = req.Params.Message
				return &mcp.ElicitResult{Action: "accept", Content: content}, nil
			}}, version)
		}
		for _, content := range []map[string]any{nil, {"confirm": false}, {"confirm": true}} {
			out, err := answer(content).CallTool(ctx, &mcp.CallToolParams{Name: "baton_clean", Arguments: args})
			if err != nil || out.IsError || !strings.Contains(asked, "读一读") {
				t.Fatalf("%s %v: %+v %v %q", version, content, out, err, asked)
			}
			_, statErr := os.Stat(dir)
			if b, _ := json.Marshal(out.StructuredContent); (content["confirm"] == true) != os.IsNotExist(statErr) || (content["confirm"] == true) == strings.Contains(string(b), `"declined":true`) {
				t.Fatalf("%s %v: %s, task dir: %v", version, content, b, statErr)
			}
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{"task":`+task+`,"number":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
