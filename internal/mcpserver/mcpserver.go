// Copied from collaborative-work (same author).
package mcpserver

import (
	"baton/internal/app"
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func Serve() error {
	svc, err := app.New()
	if err != nil {
		return err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "baton", Version: "0.1.0"}, nil)
	register(srv, svc)
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

type taskID struct {
	ID string `json:"id"`
}

func register(srv *mcp.Server, svc *app.Service) {
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_run", Description: "Dispatch one task to an agent in a detached worker. Pass the conversation title in title. Mode defaults to write; no planning or review."},
		func(ctx context.Context, req *mcp.CallToolRequest, in app.RunRequest) (*mcp.CallToolResult, *app.Submitted, error) {
			out, err := svc.Run(in)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_status", OutputSchema: map[string]any{"type": "object"}, Description: "Read task status; a missing running worker is reported as stale."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskID) (*mcp.CallToolResult, *app.Task, error) {
			out, err := svc.Status(in.ID)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_wait", OutputSchema: map[string]any{"type": "object"}, Description: "Poll until terminal status or timeout. Default 120s, maximum 600s; timeout returns the current status."},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			ID             string `json:"id"`
			TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
		}) (*mcp.CallToolResult, *app.Task, error) {
			n := in.TimeoutSeconds
			if n == 0 {
				n = 120
			}
			if n < 0 || n > 600 {
				return nil, nil, fmt.Errorf("timeout_seconds must be 1..600")
			}
			out, err := svc.Wait(ctx, in.ID, time.Duration(n)*time.Second)
			if err == context.DeadlineExceeded && ctx.Err() == nil {
				err = nil
			}
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_result", OutputSchema: map[string]any{"type": "object"}, Description: "Read final reply, session, usage, cost, patch, branch and independently executed checks."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskID) (*mcp.CallToolResult, *app.Result, error) {
			out, err := svc.Result(in.ID)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_reply", Description: "Continue the same agent session and worktree in a detached worker."},
		func(ctx context.Context, req *mcp.CallToolRequest, in app.ReplyRequest) (*mcp.CallToolResult, *app.Submitted, error) {
			out, err := svc.Reply(in)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_cancel", OutputSchema: map[string]any{"type": "object"}, Description: "Request cancellation; the worker stops the agent or check process group and records canceled."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskID) (*mcp.CallToolResult, *app.Task, error) {
			out, err := svc.Cancel(in.ID)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_list", OutputSchema: map[string]any{"type": "object"}, Description: "List persisted tasks, newest first."},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, *app.ListResult, error) {
			out, err := svc.List()
			return nil, out, err
		})
}
