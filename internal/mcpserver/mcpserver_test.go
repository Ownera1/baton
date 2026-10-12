package mcpserver

import (
	"baton/internal/app"
	"context"
	"encoding/json"
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
