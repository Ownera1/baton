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
	st, ct := mcp.NewInMemoryTransports()
	session, err := server("1", &app.Service{Home: home}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	conn, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
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
