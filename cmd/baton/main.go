package main

import (
	"baton/internal/app"
	"baton/internal/mcpserver"
	"baton/internal/tui"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// version is set by the release build: -ldflags "-X main.version=...".
var version = "dev"

type checks []string

func (c *checks) String() string         { return fmt.Sprint([]string(*c)) }
func (c *checks) Set(value string) error { *c = append(*c, value); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "baton:", err)
		os.Exit(1)
	}
}

func emit(value any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(value)
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: baton run|status|wait|result|reply|cancel|list|tui|mcp|version")
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if args[0] == "mcp" {
		if len(args) != 1 {
			return fmt.Errorf("mcp takes no arguments")
		}
		return mcpserver.Serve()
	}
	svc, err := app.New()
	if err != nil {
		return err
	}
	if args[0] == "_worker" {
		if len(args) != 2 {
			return fmt.Errorf("worker requires task id")
		}
		return svc.Worker(args[1])
	}
	if args[0] == "tui" {
		fs := flag.NewFlagSet("tui", flag.ContinueOnError)
		theme := fs.String("theme", "auto", "auto|light|dark")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("tui takes no positional arguments")
		}
		return tui.Run(svc, *theme)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	var value any
	switch args[0] {
	case "run":
		var req app.RunRequest
		var commands checks
		fs.StringVar(&req.Agent, "agent", "", "codex|claude|grok|agy|pi")
		fs.StringVar(&req.Repo, "repo", "", "git repository")
		fs.StringVar(&req.Task, "task", "", "text or @file")
		fs.StringVar(&req.Title, "title", "", "conversation title (inferred from task when omitted)")
		fs.StringVar(&req.Mode, "mode", "write", "read|write")
		fs.StringVar(&req.Model, "model", "", "model")
		fs.StringVar(&req.Effort, "effort", "", "reasoning effort")
		fs.StringVar(&req.Timeout, "timeout", "30m", "turn timeout, including checks")
		fs.Var(&commands, "check", "check command (repeatable)")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected arguments: %v", fs.Args())
		}
		req.Checks = commands
		value, err = svc.Run(req)
	case "status", "wait", "result", "reply", "cancel":
		if len(args) < 2 || args[1] == "" || args[1][0] == '-' {
			return fmt.Errorf("%s requires task id before flags", args[0])
		}
		id := args[1]
		asJSON := fs.Bool("json", false, "JSON output")
		var msg string
		var timeout time.Duration
		if args[0] == "wait" {
			fs.DurationVar(&timeout, "timeout", 10*time.Minute, "wait timeout")
		}
		if args[0] == "reply" {
			fs.StringVar(&msg, "msg", "", "text or @file")
			fs.DurationVar(&timeout, "timeout", 30*time.Minute, "turn timeout")
		}
		if err = fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected arguments: %v", fs.Args())
		}
		switch args[0] {
		case "status":
			value, err = svc.Status(id)
		case "wait":
			value, err = svc.Wait(context.Background(), id, timeout)
		case "result":
			value, err = svc.Result(id)
		case "reply":
			value, err = svc.Reply(app.ReplyRequest{ID: id, Message: msg, Timeout: timeout.String()})
		case "cancel":
			value, err = svc.Cancel(id)
		}
		if args[0] == "wait" && err != nil && value != nil {
			_ = emit(value)
			return err
		}
		if err == nil && !*asJSON && (args[0] == "status" || args[0] == "wait") {
			t := value.(*app.Task)
			fmt.Printf("%s %s (%s, %s), turns=%d\n", t.ID, t.Status, t.Agent, t.Mode, len(t.Turns))
			return nil
		}
	case "list":
		asJSON := fs.Bool("json", false, "JSON output")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("list takes no positional arguments")
		}
		var list *app.ListResult
		list, err = svc.List()
		value = list
		if err == nil && !*asJSON {
			for _, t := range list.Tasks {
				fmt.Printf("%s\t%s\t%s\t%s\n", t.ID, t.Status, t.Agent, t.Repo)
			}
			return nil
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		return err
	}
	return emit(value)
}
