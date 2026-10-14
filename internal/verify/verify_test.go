package verify

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	dir, logs := t.TempDir(), t.TempDir()
	checks := []Input{
		{ID: "pass", Command: "true"},
		{ID: "fail", Command: "echo broken; exit 3"},
		{ID: "missing", Command: "/nonexistent-baton-command"},
		{ID: "empty", Command: " "},
		{ID: "slow", Command: "sleep 30"},
		{ID: "long", Command: "seq 100000"},
	}
	start := time.Now()
	res, err := Run("task", 1, "snap", dir, logs, checks, 500*time.Millisecond, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("timeout did not stop the slow check")
	}
	want := []string{StatusPass, StatusFail, StatusUnavailable, StatusUnavailable, StatusUnavailable, StatusPass}
	for i, c := range res.Checks {
		if c.Status != want[i] || c.LogPath == "" {
			t.Errorf("%s: %+v", c.ID, c)
		}
	}
	if res.Status != StatusUnavailable {
		t.Fatalf("an unrunnable check is not a pass: %s", res.Status)
	}
	if c := res.Checks[1]; c.ExitCode != 3 || c.OutputTail != "broken\n" {
		t.Errorf("fail: %+v", c)
	}
	if c := res.Checks[4]; !c.TimedOut {
		t.Errorf("slow: %+v", c)
	}
	c := res.Checks[5]
	if log, err := os.ReadFile(c.LogPath); err != nil || !strings.Contains(string(log), "\n1\n2\n") || !strings.Contains(string(log), "\n100000\n") {
		t.Fatalf("log must keep the whole output: %v", err)
	}
	if !strings.HasPrefix(c.OutputTail, "…[earlier output omitted]…\n") || !strings.HasSuffix(c.OutputTail, "\n100000\n") {
		t.Fatalf("tail: %q", c.OutputTail)
	}
}

func TestSummarize(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{StatusPass, StatusPass}, StatusPass},
		{[]string{StatusPass, StatusFail}, StatusFail},
		{[]string{StatusFail, StatusUnavailable}, StatusUnavailable},
	} {
		var checks []Outcome
		for _, s := range c.in {
			checks = append(checks, Outcome{Status: s})
		}
		if got, _ := summarize(checks); got != c.want {
			t.Errorf("%v: %s, want %s", c.in, got, c.want)
		}
	}
	if sanitize("go test ./...") != "go_test______" {
		t.Error(sanitize("go test ./..."))
	}
}
