package app_test

import (
	"baton/internal/app"
	"baton/internal/workspace"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// BenchmarkDispatch is Baton's own cost for one write turn around an agent
// that answers at once: `baton do -w` from the CLI to the printed result.
// start-ms is dispatch to agent start (worker, worktree), turn-ms the turn
// itself (fake agent, commit, patch); the rest of ns/op is the CLI and the
// wait poll.
func BenchmarkDispatch(b *testing.B) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	bin := filepath.Join(dir, "baton")
	build := exec.Command("go", "build", "-o", bin, "./cmd/baton")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		b.Fatalf("build: %v\n%s", err, out)
	}
	home, fakeBin, repo := filepath.Join(dir, "home"), filepath.Join(dir, "fake-bin"), filepath.Join(dir, "repo")
	for _, p := range []string{home, fakeBin, repo} {
		if err := os.Mkdir(p, 0700); err != nil {
			b.Fatal(err)
		}
	}
	b.Setenv("BATON_HOME", home)
	b.Setenv("BATON_AGENT", "")
	b.Setenv("BATON_CONFIG_DIR", dir)
	b.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	b.Setenv("GIT_AUTHOR_NAME", "Baton Test")
	b.Setenv("GIT_AUTHOR_EMAIL", "baton@example.invalid")
	b.Setenv("GIT_COMMITTER_NAME", "Baton Test")
	b.Setenv("GIT_COMMITTER_EMAIL", "baton@example.invalid")
	b.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	b.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	b.Setenv("FIXTURE_PATH", filepath.Join(root, "testdata", "fixtures", "stream", "codex.jsonl"))
	script := "#!/bin/sh\ncat > /dev/null\nprintf 'turn\\n' >> result.txt\ncat \"$FIXTURE_PATH\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte(script), 0700); err != nil {
		b.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"commit", "--allow-empty", "-m", "base"}} {
		if _, err := workspace.Git(repo, args...); err != nil {
			b.Fatal(err)
		}
	}
	// dispatch returns milliseconds from dispatch to agent start, and of the turn.
	dispatch := func() (float64, float64) {
		out, err := exec.Command(bin, "do", "-repo", repo, "-no-check", "-w", "-json", "make a file").Output()
		if err != nil {
			b.Fatalf("baton do: %v\n%s", err, out)
		}
		var r app.Result
		if err := json.Unmarshal(out, &r); err != nil || len(r.Turns) != 1 || r.Turns[0].EndedAt == nil {
			b.Fatalf("result %s: %v", out, err)
		}
		t := r.Turns[0]
		return float64(t.StartedAt.Sub(r.CreatedAt).Milliseconds()), float64(t.EndedAt.Sub(*t.StartedAt).Milliseconds())
	}
	dispatch() // the first task in a new home is about a second slower
	var start, turn float64
	for b.Loop() {
		s, t := dispatch()
		start, turn = start+s, turn+t
	}
	b.ReportMetric(start/float64(b.N), "start-ms/op")
	b.ReportMetric(turn/float64(b.N), "turn-ms/op")
}
