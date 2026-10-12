// Copied from collaborative-work (same author).
package mcpserver

import (
	"baton/internal/app"
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// instructions tell the main agent how to work with sub-agents; clients put
// them in the model's context at connection time.
const instructions = `Baton runs coding tasks on sub-agents in detached workers. You plan, split, choose and judge; a sub-agent only knows what you put in task. Each write task works in its own git worktree on branch baton/<id> and is checked by the project's tests; Baton never merges, pushes or switches the user's branch.

Start with baton_agents: it lists the agents and the user's roles you may use, with what each is for. Prefer a role whose description fits. Give every task a short title.

Patterns:
- One task: baton_run, then baton_wait (at most 600 s per call; call again while it runs) and baton_result for the reply, branch, patch and checks.
- Parallel: dispatch independent tasks that touch different files with the same group, then baton_wait with ids; with any, handle each as it finishes. Tasks may stay queued when the user limits how many run at once.
- Best of N: send the same task to two agents and keep the better patch by its checks and your reading.
- Review loop: when a write task finishes, baton_run a read task (a reviewer role if one exists) with base set to it; send real problems back with baton_reply to the writer, not a new task.
- Relay: base a write task on a finished one to continue its branch, for example tests after an implementation.
- Merging is yours, after review: sub-agents cannot merge, their sandboxes block git writes. Review every finished branch yourself, reading the patch and checks from baton_result and adding a reviewer task when it helps; then merge the approved baton/<id> branches into the user's branch with your own git tools, resolving conflicts yourself or replying to the task that caused them.

Sub-agents cannot dispatch tasks and never talk to each other: results reach the next task only through you, in task or reply, or through base. When checks fail, reply with the failure instead of starting over. Never work around a sandbox refusal by moving the task to an agent with a weaker sandbox (pi write has none); tell the user instead. Finish by reporting what you merged, and which branches you left out and why.`

func Serve(version string) error {
	svc, err := app.New()
	if err != nil {
		return err
	}
	return server(version, svc).Run(context.Background(), &mcp.StdioTransport{})
}

func server(version string, svc *app.Service) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "baton", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	register(srv, svc)
	return srv
}

// waited is one task, as before ids existed, or the tasks asked for by ids.
type waited struct {
	*app.Task
	Tasks []*app.Task `json:"tasks,omitempty"`
}

type taskID struct {
	ID string `json:"id,omitempty" jsonschema:"task id, unique prefix or suffix; empty means the newest task"`
}

func register(srv *mcp.Server, svc *app.Service) {
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_agents", OutputSchema: map[string]any{"type": "object"}, Description: "List the agents and roles you may dispatch to with baton_run's agent. Agents are the coding CLIs, with how read and write tasks run. Roles are the user's sub-agents: a description to choose by, the agent they run on, model, effort, and a mode when they only run read or write; their instructions are added to the first turn. Also the default agent and any error that would make dispatch fail. Hidden agents and roles are omitted and refused by baton_run and baton_reply."},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			Repo string `json:"repo,omitempty" jsonschema:"git repository whose .baton/agents roles to include; defaults to the server's directory"`
		}) (*mcp.CallToolResult, *app.AgentList, error) {
			out, err := app.AgentsFor(cmp.Or(in.Repo, "."), false)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_run", Description: "Dispatch one task to an agent in a detached worker. Only task is required: agent (an agent or role listed by baton_agents), model and effort default to the user's Baton config (agent codex if unset), repo to the server's directory, checks are detected from the project. Pass the conversation title in title. Mode defaults to write; no planning or review."},
		func(ctx context.Context, req *mcp.CallToolRequest, in app.RunRequest) (*mcp.CallToolResult, *app.Submitted, error) {
			in.FromMCP = true
			out, err := svc.Run(in)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_status", OutputSchema: map[string]any{"type": "object"}, Description: "Read task status; a missing running worker is reported as stale."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskID) (*mcp.CallToolResult, *app.Task, error) {
			id, err := svc.Resolve(in.ID)
			if err != nil {
				return nil, nil, err
			}
			out, err := svc.Status(id)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_wait", OutputSchema: map[string]any{"type": "object"}, Description: "Poll until terminal status or timeout. Default 120s, maximum 600s; timeout returns the current status. With ids, wait for several tasks and return them in tasks; any returns as soon as one of them finishes, so you can act on it while the rest run."},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			ID             string   `json:"id,omitempty" jsonschema:"task id, unique prefix or suffix; empty means the newest task"`
			IDs            []string `json:"ids,omitempty" jsonschema:"several tasks to wait for instead of id"`
			Any            bool     `json:"any,omitempty" jsonschema:"with ids, return when the first task finishes"`
			TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
		}) (*mcp.CallToolResult, *waited, error) {
			n := in.TimeoutSeconds
			if n == 0 {
				n = 120
			}
			if n < 0 || n > 600 {
				return nil, nil, fmt.Errorf("timeout_seconds must be 1..600")
			}
			refs := in.IDs
			if len(refs) == 0 {
				refs = []string{in.ID}
			}
			ids := make([]string, len(refs))
			for i, ref := range refs {
				id, err := svc.Resolve(ref)
				if err != nil {
					return nil, nil, err
				}
				ids[i] = id
			}
			tasks, err := svc.WaitMany(ctx, ids, in.Any, time.Duration(n)*time.Second)
			if err == context.DeadlineExceeded && ctx.Err() == nil {
				err = nil
			}
			if err != nil {
				return nil, nil, err
			}
			if len(in.IDs) == 0 {
				return nil, &waited{Task: tasks[0]}, nil
			}
			return nil, &waited{Tasks: tasks}, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_result", OutputSchema: map[string]any{"type": "object"}, Description: "Read final reply, session, usage, cost, patch, branch and independently executed checks."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskID) (*mcp.CallToolResult, *app.Result, error) {
			id, err := svc.Resolve(in.ID)
			if err != nil {
				return nil, nil, err
			}
			out, err := svc.Result(id)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_reply", Description: "Continue the same agent session and worktree in a detached worker."},
		func(ctx context.Context, req *mcp.CallToolRequest, in app.ReplyRequest) (*mcp.CallToolResult, *app.Submitted, error) {
			id, err := svc.Resolve(in.ID)
			if err != nil {
				return nil, nil, err
			}
			in.ID, in.FromMCP = id, true
			out, err := svc.Reply(in)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_cancel", OutputSchema: map[string]any{"type": "object"}, Description: "Request cancellation; the worker stops the agent or check process group and records canceled."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskID) (*mcp.CallToolResult, *app.Task, error) {
			id, err := svc.Resolve(in.ID)
			if err != nil {
				return nil, nil, err
			}
			out, err := svc.Cancel(id)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_list", OutputSchema: map[string]any{"type": "object"}, Description: "List persisted tasks, newest first; group limits it to one group of tasks."},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			Group string `json:"group,omitempty" jsonschema:"only tasks dispatched with this group"`
		}) (*mcp.CallToolResult, *app.ListResult, error) {
			out, err := svc.List(in.Group)
			return nil, out, err
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "baton_export", OutputSchema: map[string]any{"type": "object"}, Description: "Write tasks as Markdown (each turn's instruction, final reply and checks) to .baton/history/ in their repository, for agents and people working there to read. Pick tasks by ids, or every task in a group and/or repo; nothing exports the newest task. Returns the files written."},
		func(ctx context.Context, req *mcp.CallToolRequest, in app.ExportRequest) (*mcp.CallToolResult, *app.ExportResult, error) {
			out, err := svc.Export(in)
			return nil, out, err
		})
}
