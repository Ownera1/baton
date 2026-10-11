package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

func TestParseFlagsAnywhere(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	w := fs.Bool("w", false, "")
	repo := fs.String("repo", "", "")
	pos, err := parse(fs, []string{"claude", "解释", "-w", "guard", "-repo", "/r", "--", "-x", "y"})
	if err != nil || !*w || *repo != "/r" || !reflect.DeepEqual(pos, []string{"claude", "解释", "guard", "-x", "y"}) {
		t.Fatalf("pos=%q w=%v repo=%q err=%v", pos, *w, *repo, err)
	}
}

func TestSplitAgent(t *testing.T) {
	for _, c := range []struct {
		in    []string
		agent string
		rest  []string
	}{
		{[]string{"claude", "解释 guard"}, "claude", []string{"解释 guard"}},
		{[]string{"codex"}, "", []string{"codex"}}, // a lone word is the task
		{[]string{"修复", "codex"}, "", []string{"修复", "codex"}},
	} {
		agent, rest := splitAgent(c.in)
		if agent != c.agent || !reflect.DeepEqual(rest, c.rest) {
			t.Errorf("splitAgent(%q) = %q, %q", c.in, agent, rest)
		}
	}
}

func TestText(t *testing.T) {
	if got, _ := text([]string{"给", "API", "加分页"}, nil, false); got != "给 API 加分页" {
		t.Errorf("joined words: %q", got)
	}
	if got, _ := text([]string{"-"}, strings.NewReader("来自管道"), false); got != "来自管道" {
		t.Errorf("dash reads stdin: %q", got)
	}
	if got, _ := text(nil, strings.NewReader("piped"), true); got != "piped" {
		t.Errorf("piped stdin: %q", got)
	}
	if _, err := text(nil, nil, false); err == nil {
		t.Error("missing text must fail")
	}
}
