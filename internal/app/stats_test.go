package app

import (
	"baton/internal/agent"
	"baton/internal/verify"
	"baton/internal/workspace"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStats(t *testing.T) {
	repo, other := t.TempDir(), t.TempDir()
	for _, dir := range []*string{&repo, &other} {
		if out, err := workspace.Git(*dir, "init", "-q"); err != nil {
			t.Fatal(out, err)
		}
		*dir, _ = workspace.Git(*dir, "rev-parse", "--show-toplevel")
	}
	s := &Service{Home: t.TempDir()}
	base := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	turn := func(n int, minutes int, checkMS int64, usage string, cost *float64) Turn {
		start, end := base, base.Add(time.Duration(minutes)*time.Minute)
		return Turn{Number: n, Prompt: "x", StartedAt: &start, EndedAt: &end, Usage: json.RawMessage(usage), CostUSD: cost,
			Verify: &verify.Result{DurationMS: checkMS}}
	}
	cost := func(v float64) *float64 { return &v }
	add := func(n int, task Task) {
		t.Helper()
		task.CreatedAt = base.Add(time.Duration(n) * time.Minute)
		dir := filepath.Join(s.Home, "tasks", task.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, "request.json"), request{Task: task, Number: 1}); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, "task.json"), task); err != nil {
			t.Fatal(err)
		}
	}
	claude := `{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":5}`
	add(1, Task{ID: "a", Agent: "claude", Repo: repo, Status: "succeeded", Group: "g",
		Turns: []Turn{turn(1, 2, 30_000, claude, cost(0.5))}})
	add(2, Task{ID: "b", Agent: "claude", Repo: repo, Status: "failed",
		Turns: []Turn{turn(1, 1, 0, claude, cost(0.25)), turn(2, 1, 0, "", nil)}})
	// codex's second record already counts the first.
	add(3, Task{ID: "c", Agent: "codex", Role: "tester", Repo: repo, Status: "succeeded", Group: "g",
		Turns: []Turn{turn(1, 1, 0, `{"input_tokens":100,"output_tokens":1}`, nil), turn(2, 1, 0, `{"input_tokens":300,"output_tokens":3}`, nil)}})
	add(4, Task{ID: "d", Agent: "claude", Repo: repo, Status: "running", Turns: []Turn{turn(1, 1, 0, claude, cost(9))}})
	add(5, Task{ID: "e", Agent: "claude", Repo: other, Status: "succeeded", Turns: []Turn{turn(1, 1, 0, claude, cost(9))}})

	if c, err := s.Status("c"); err != nil || c.Turns[1].Tokens == nil || *c.Turns[1].Tokens != (agent.Tokens{Input: 200, Output: 2}) {
		t.Fatalf("codex's second turn is its own usage: %+v, %v", c, err)
	}
	got, err := s.Stats(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d stats, want codex role and claude: %+v", len(got), got)
	}
	tester, cl := got[0], got[1] // the most recently used first
	if tester.Role != "tester" || tester.Tokens != (agent.Tokens{Input: 300, Output: 3}) || tester.CostUSD != nil || tester.Turns != 2 {
		t.Errorf("codex role: %+v", tester)
	}
	want := Stat{Agent: "claude", Tasks: 2, Succeeded: 1, FirstTurn: 1, Turns: 3, AgentMS: 4*60_000 - 30_000, CheckMS: 30_000,
		Tokens: agent.Tokens{Input: 200, Cached: 180, Output: 10}}
	if cl.CostUSD == nil || *cl.CostUSD != 0.75 {
		t.Errorf("claude cost %v, want 0.75", cl.CostUSD)
	}
	cl.CostUSD = nil
	if *cl != want {
		t.Errorf("claude %+v, want %+v", *cl, want)
	}
	if all, err := s.Stats("", "g"); err != nil || len(all) != 2 || all[1].Tasks != 1 {
		t.Errorf("group: %+v, %v", all, err)
	}
	if all, err := s.Stats("", ""); err != nil || len(all) != 2 || all[0].Tasks != 3 {
		t.Errorf("every repository: %+v, %v", all, err)
	}
}
