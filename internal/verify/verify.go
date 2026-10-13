// Copied from collaborative-work (same author).
// Package verify runs the acceptance checks itself. The verifier is the only
// source of truth for "the checks pass": whatever the executor claims in its
// report is an executor statement, kept for the record and never used to
// complete a task.
package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"baton/internal/guard"
	"baton/internal/workspace"
)

// Check status values.
const (
	StatusNone        = "none"
	StatusPass        = "pass"
	StatusFail        = "fail"
	StatusUnavailable = "unavailable"
)

// Outcome is one executed check.
type Outcome struct {
	ID         string    `json:"id"`
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	DurationMS int64     `json:"duration_ms"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Reason     string    `json:"reason,omitempty"`
	LogPath    string    `json:"log_path,omitempty"`
	OutputTail string    `json:"output_tail,omitempty"`
	TimedOut   bool      `json:"timed_out,omitempty"`
	StartError string    `json:"start_error,omitempty"`
	// Resource facts, recorded so the report can say what a check cost.
	PeakRSSMB   float64 `json:"peak_rss_mb,omitempty"`
	CPUQuota    int     `json:"cpu_quota,omitempty"`
	Nice        int     `json:"nice,omitempty"`
	GateWaitMS  int64   `json:"gate_wait_ms,omitempty"`
	Stopped     bool    `json:"stopped,omitempty"`
	ForceKilled bool    `json:"force_killed,omitempty"`
}

// Result is the verification record bound to one snapshot.
type Result struct {
	TaskID     string    `json:"task_id"`
	Round      int       `json:"round"`
	SnapshotID string    `json:"snapshot_id"`
	Status     string    `json:"status"`
	Checks     []Outcome `json:"checks"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	DurationMS int64     `json:"duration_ms"`
	// Notes explains the status in one line for the report.
	Notes string `json:"notes"`
}

// Input is one check to run, in the worktree.
type Input struct {
	ID      string
	Command string
}

// Options are the machine limits a verification round runs under.
type Options struct {
	Limits guard.Limits
	// Gate serialises checks across every task on this machine. A nil gate runs
	// them without coordination, which only makes sense in tests.
	Gate *guard.Gate
	// Context cancels the round; a cancelled check stops its process group.
	Context context.Context
	// Note receives resource-enforcement notices for the event stream.
	Note func(string)
}

// Run executes every check in the worktree, one at a time and inside the
// machine's budget. A required check that cannot run to completion (missing
// tool, timeout, memory ceiling, no free slot) is recorded as unavailable,
// which is explicitly not a pass.
func Run(taskID string, round int, snapshotID, worktree, logDir string, checks []Input, defaultTimeout time.Duration, opts Options) (Result, error) {
	res := Result{TaskID: taskID, Round: round, SnapshotID: snapshotID, StartedAt: time.Now().UTC(), Checks: []Outcome{}}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return res, err
	}
	if len(checks) == 0 {
		res.Status = StatusNone
		res.Notes = "no check commands supplied"
		res.EndedAt = time.Now().UTC()
		res.DurationMS = res.EndedAt.Sub(res.StartedAt).Milliseconds()
		return res, nil
	}
	for _, c := range checks {
		out := runOne(taskID, round, worktree, logDir, c, defaultTimeout, opts)
		res.Checks = append(res.Checks, out)
	}
	res.EndedAt = time.Now().UTC()
	res.DurationMS = res.EndedAt.Sub(res.StartedAt).Milliseconds()
	res.Status, res.Notes = summarize(res.Checks)
	return res, nil
}

func runOne(taskID string, round int, worktree, logDir string, c Input, defaultTimeout time.Duration, opts Options) (o Outcome) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	cwd := worktree
	o = Outcome{ID: c.ID, Command: c.Command, Cwd: cwd, StartedAt: time.Now().UTC(), Status: StatusUnavailable, ExitCode: -1}
	defer func() {
		if o.LogPath != "" {
			return
		}
		path := filepath.Join(logDir, fmt.Sprintf("check-%s.log", sanitize(c.ID)))
		body := fmt.Sprintf("$ %s\ncwd: %s\nexit: %d\nstatus: %s\nreason: %s\n", c.Command, cwd, o.ExitCode, o.Status, o.Reason)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			o.Status = StatusUnavailable
			o.Reason += "; cannot save log: " + err.Error()
		} else {
			o.LogPath = path
		}
		o.DurationMS = o.EndedAt.Sub(o.StartedAt).Milliseconds()
	}()
	timeout := defaultTimeout
	if strings.TrimSpace(c.Command) == "" {
		o.Reason = "check has an empty command"
		o.EndedAt = time.Now().UTC()
		return o
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		o.Reason = fmt.Sprintf("worktree %s does not exist", cwd)
		o.EndedAt = time.Now().UTC()
		return o
	}
	// The machine is taken for one check at a time, across every task. Waiting
	// is correct; failing because another task is running is not.
	gateStart := time.Now()
	lease, err := opts.Gate.Acquire(ctx, guard.Holder(taskID, round, c.ID), opts.Limits.GateWait)
	if err != nil {
		o.Status = StatusUnavailable
		if errors.Is(err, guard.ErrGateBusy) {
			o.Reason = "the machine was still busy with another check after waiting: " + err.Error()
		} else {
			o.Reason = fmt.Sprintf("could not wait for the machine: %v", err)
		}
		o.EndedAt = time.Now().UTC()
		return o
	}
	waited := time.Since(gateStart)
	defer lease.Release()

	cr := workspace.RunCommand(cwd, c.Command, workspace.RunOptions{
		Timeout:   timeout,
		MaxOutput: 64 * 1024,
		Limits:    opts.Limits,
		Context:   ctx,
		Note:      opts.Note,
	})
	o.GateWaitMS = waited.Milliseconds()
	o.PeakRSSMB = float64(cr.PeakRSSKB) / 1024
	o.CPUQuota = opts.Limits.CPUs
	o.Nice = opts.Limits.Nice
	o.Stopped = cr.Stopped
	o.ForceKilled = cr.Killed
	o.ExitCode = cr.ExitCode
	o.DurationMS = cr.DurationMS
	o.EndedAt = time.Now().UTC()
	o.TimedOut = cr.TimedOut
	o.StartError = cr.StartErr
	o.OutputTail = tail(cr.Output, 4000)

	logPath := filepath.Join(logDir, fmt.Sprintf("check-%s.log", sanitize(c.ID)))
	header := fmt.Sprintf("$ %s\ncwd: %s\nstarted: %s\nexit: %d timed_out: %v\n\n",
		c.Command, cwd, o.StartedAt.Format(time.RFC3339), cr.ExitCode, cr.TimedOut)
	if err := os.WriteFile(logPath, []byte(header+cr.Output), 0o644); err != nil {
		o.Reason = "cannot save check log: " + err.Error()
		return o
	}
	o.LogPath = logPath

	switch {
	case cr.StartErr != "":
		o.Status = StatusUnavailable
		o.Reason = "command could not start: " + cr.StartErr
	case cr.MemoryExceeded:
		// The check was stopped by the machine's memory ceiling. It did not
		// fail; it never finished, and unknown is not a pass.
		o.Status = StatusUnavailable
		o.Reason = fmt.Sprintf("stopped at the %d MB memory ceiling after using %d MB; the result is unknown, not a pass",
			opts.Limits.MemoryMB, int64(o.PeakRSSMB))
	case cr.Canceled:
		o.Status = StatusUnavailable
		o.Reason = "stopped because the task was cancelled or interrupted"
	case cr.TimedOut:
		o.Status = StatusUnavailable
		o.Reason = fmt.Sprintf("command exceeded its %s timeout", timeout)
	case cr.ExitCode == 0:
		o.Status = StatusPass
	case cr.ExitCode == 126 || cr.ExitCode == 127:
		// The shell could not run the program at all. A check that could not
		// run is unknown, not failed, and unknown never counts as a pass.
		o.Status = StatusUnavailable
		o.Reason = fmt.Sprintf("command not runnable (exit %d): %s", cr.ExitCode, firstLine(cr.Output))
	default:
		o.Status = StatusFail
		o.Reason = fmt.Sprintf("exit code %d", cr.ExitCode)
	}
	return o
}

// summarize decides the round outcome: every check is required.
func summarize(checks []Outcome) (string, string) {
	var failed, unavailable, passed int
	for _, c := range checks {
		switch c.Status {
		case StatusPass:
			passed++
		case StatusFail:
			failed++
		case StatusUnavailable:
			unavailable++
		}
	}
	note := fmt.Sprintf("%d required checks passed, %d failed, %d could not be run", passed, failed, unavailable)
	switch {
	case unavailable > 0:
		return StatusUnavailable, note + "; an unrunnable required check is not a pass"
	case failed > 0:
		return StatusFail, note
	default:
		return StatusPass, note
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…[earlier output omitted]…\n" + s[len(s)-n:]
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "check"
	}
	return b.String()
}
