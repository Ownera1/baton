// Copied from collaborative-work (same author).
package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"baton/internal/guard"
)

// RunOptions are the resource limits applied to one check command.
type RunOptions struct {
	Timeout   time.Duration
	MaxOutput int
	// Limits carries the CPU, memory and scheduling budget. When it is zero the
	// defaults still apply: a check never gets the whole machine.
	Limits guard.Limits
	// Context cancels the command. Cancellation uses the same group-wide
	// termination path as a timeout, so children die with it.
	Context context.Context
	// Note receives one-line notices about limit enforcement, for the log.
	Note    func(string)
	OnStart func(int) error
	Stdout  io.Writer
	Stderr  io.Writer
}

// CommandResult is the outcome of one check command.
type CommandResult struct {
	Command    string        `json:"command"`
	Cwd        string        `json:"cwd"`
	ExitCode   int           `json:"exit_code"`
	Duration   time.Duration `json:"-"`
	DurationMS int64         `json:"duration_ms"`
	Output     string        `json:"output"`
	TimedOut   bool          `json:"timed_out"`
	// Stopped is set when the tool ended the command itself, for any reason:
	// timeout, memory ceiling or cancellation. A stopped command did not
	// produce a result, which is different from a command that failed.
	Stopped bool `json:"stopped,omitempty"`
	// Killed is set when a process had to be force-killed, meaning it did not
	// even respond to a polite request to stop.
	Killed bool `json:"killed,omitempty"`
	// MemoryExceeded is set when the process group crossed its memory ceiling.
	// The check did not finish, so it is neither a pass nor a failure.
	MemoryExceeded bool  `json:"memory_exceeded,omitempty"`
	PeakRSSKB      int64 `json:"peak_rss_kb,omitempty"`
	// Canceled is set when the surrounding context ended the check.
	Canceled bool   `json:"canceled,omitempty"`
	StartErr string `json:"start_error,omitempty"`
}

// RunCommand runs a command under this machine's resource budget: its own
// process group, a low scheduling priority, a bounded slice of CPU, a memory
// ceiling and a timeout. Everything the command starts is inside that budget,
// and everything it starts is gone when it ends.
func RunCommand(dir, command string, opts RunOptions) CommandResult {
	return RunArgv(dir, []string{"/bin/sh", "-c", command}, nil, os.Environ(), opts)
}

// RunArgv runs a fixed argv and a caller-selected environment without a shell.
func RunArgv(dir string, argv []string, stdin io.Reader, env []string, opts RunOptions) CommandResult {
	command := fmt.Sprint(argv)
	res := CommandResult{Command: command, Cwd: dir, ExitCode: -1}
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		res.TimedOut = ctx.Err() == context.DeadlineExceeded
		res.Canceled = !res.TimedOut
		res.Stopped = true
		return res
	}
	limits := opts.Limits
	applyLimitDefaults(&limits)

	if len(argv) == 0 {
		res.StartErr = "empty command"
		return res
	}
	if _, err := guard.ProcessGroupRSS(os.Getpid()); err != nil {
		res.StartErr = "resource monitoring unavailable: " + err.Error()
		res.Stopped = true
		res.ExitCode = -1
		return res
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Dir = dir
	buf := safeBuffer{limit: 4 << 20}
	if opts.MaxOutput > 0 {
		buf.limit = opts.MaxOutput
	}
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	}
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	// Preserve the caller's environment, then add noninteractive settings and
	// resource hints. Limit variables go last so they win over inherited values.
	cmd.Env = append(append([]string{}, env...), "CI=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")
	for _, setting := range limits.CheckEnv() {
		if strings.HasPrefix(setting, "GOFLAGS=") {
			for _, existing := range env {
				if strings.HasPrefix(existing, "GOFLAGS=") {
					setting = existing + " " + strings.TrimPrefix(setting, "GOFLAGS=")
				}
			}
		}
		cmd.Env = append(cmd.Env, setting)
	}
	// Its own process group, so a timeout, a cancel or a memory stop reaches the
	// whole tree and nothing else.
	setProcessGroup(cmd)
	if limits.Nice > 0 {
		applyNice(cmd, limits.Nice)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.StartErr = err.Error()
		res.Duration = time.Since(start)
		res.DurationMS = res.Duration.Milliseconds()
		return res
	}

	if limits.Nice > 0 {
		applyNiceRunning(cmd.Process.Pid, limits.Nice)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if opts.OnStart != nil {
		if err := opts.OnStart(cmd.Process.Pid); err != nil {
			res.StartErr = "process registration failed: " + err.Error()
			res.Stopped = true
			res.Killed = terminateGroup(cmd, done)
			res.ExitCode = -1
			return res
		}
	}

	stopCh := make(chan stopReason, 1)
	watch := newResourceWatch(cmd.Process.Pid, limits, stopCh, func(msg string) {
		if opts.Note != nil {
			opts.Note(msg)
		}
	})

	var timeout <-chan time.Time
	if opts.Timeout > 0 {
		t := time.NewTimer(opts.Timeout)
		defer t.Stop()
		timeout = t.C
	}

	select {
	case err := <-done:
		// The normal path: the command finished on its own.
		res.ExitCode = exitCodeOf(err)
		_ = signalProcessGroup(cmd, killSignal)
	case reason := <-stopCh:
		res.Stopped = true
		res.Killed = terminateGroup(cmd, done)
		res.MemoryExceeded = reason == stopMemory
		if reason == stopMonitor {
			res.StartErr = "resource monitoring became unavailable"
		}
		res.ExitCode = -1
	case <-ctx.Done():
		res.Stopped = true
		res.Killed = terminateGroup(cmd, done)
		res.TimedOut = ctx.Err() == context.DeadlineExceeded
		res.Canceled = !res.TimedOut
		res.ExitCode = -1
	case <-timeout:
		res.Stopped = true
		res.Killed = terminateGroup(cmd, done)
		res.TimedOut = true
		res.ExitCode = -1
	}

	res.Duration = time.Since(start)
	watch.halt()
	// A final resource sample can arrive at the same time as process exit.
	// Its refusal must still prevent a successful exit being labeled a pass.
	if !res.Stopped {
		select {
		case reason := <-stopCh:
			res.Stopped = true
			res.MemoryExceeded = reason == stopMemory
			res.ExitCode = -1
			if reason == stopMonitor {
				res.StartErr = "resource monitoring became unavailable"
			}
		default:
		}
	}
	res.PeakRSSKB = watch.peak()
	out := buf.String()
	if res.MemoryExceeded {
		out += fmt.Sprintf("\n[stopped: the check reached %d MB, over its %d MB ceiling]\n",
			res.PeakRSSKB/1024, limits.MemoryMB)
	}
	res.Output = truncateMiddle(out, opts.MaxOutput)
	return res
}

// terminateGroup ends a check and everything it started, politely first.
func terminateGroup(cmd *exec.Cmd, done <-chan error) bool {
	return stopProcessGroup(cmd, done)
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errorsAs(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

type stopReason int

const (
	stopMemory stopReason = iota
	stopMonitor
)

// newResourceWatch samples the memory of the process group and reports when the
// ceiling is crossed. It owns no completion channel, so it can never steal the
// result the caller is waiting for.
func newResourceWatch(pgid int, limits guard.Limits, stopCh chan<- stopReason, note func(string)) *resourceWatch {
	w := &resourceWatch{
		pgid:     pgid,
		limitKB:  int64(limits.MemoryMB) * 1024,
		interval: limits.SampleEvery,
		stopCh:   stopCh,
		note:     note,
		quit:     make(chan struct{}),
	}
	if w.interval <= 0 {
		w.interval = guard.DefaultSampleEvery
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

type resourceWatch struct {
	pgid     int
	limitKB  int64
	interval time.Duration
	stopCh   chan<- stopReason
	note     func(string)
	quit     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup

	mu     sync.Mutex
	peakKB int64
	fired  bool
}

func (w *resourceWatch) loop() {
	defer w.wg.Done()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.quit:
			w.sample()
			return
		case <-t.C:
			if w.sample() {
				return
			}
		}
	}
}

// sample records the group's memory and reports whether the ceiling was hit.
func (w *resourceWatch) sample() bool {
	kb, err := guard.ProcessTreeRSS(w.pgid)
	if err != nil {
		select {
		case w.stopCh <- stopMonitor:
		default:
		}
		return true
	}
	w.mu.Lock()
	if kb > w.peakKB {
		w.peakKB = kb
	}
	already := w.fired
	w.mu.Unlock()
	if kb <= w.limitKB || already {
		return already
	}
	w.mu.Lock()
	w.fired = true
	w.mu.Unlock()
	if w.note != nil {
		w.note(fmt.Sprintf("check exceeded its %d MB memory ceiling (using %d MB); stopping it", w.limitKB/1024, kb/1024))
	}
	select {
	case w.stopCh <- stopMemory:
	default:
	}
	return true
}

func (w *resourceWatch) halt() {
	w.once.Do(func() { close(w.quit) })
	w.wg.Wait()
}

func (w *resourceWatch) peak() int64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.peakKB
}

func applyLimitDefaults(l *guard.Limits) {
	if l.SampleEvery <= 0 {
		l.SampleEvery = guard.DefaultSampleEvery
	}
	if l.MemoryMB <= 0 {
		l.MemoryMB = guard.DefaultMemoryMB
	}
	if l.CPUs <= 0 {
		l.CPUs = guard.DefaultCPUs
	}
}

func truncateMiddle(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	head := s[:max/2]
	tail := s[len(s)-max/2:]
	return head + "\n…[truncated]…\n" + tail
}

// safeBuffer collects output from the two streams exec.Cmd writes on separate
// goroutines.
type safeBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.limit
	if limit <= 0 {
		limit = 4 << 20
	}
	if len(b.buf)+len(p) <= limit {
		b.buf = append(b.buf, p...)
	} else {
		b.truncated = true
		head := limit / 2
		tail := limit - head
		combined := append(b.buf, p...)
		b.buf = append(append([]byte{}, combined[:head]...), combined[len(combined)-tail:]...)
	}
	return len(p), nil
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		half := len(b.buf) / 2
		return string(b.buf[:half]) + "\n[Baton output truncated]\n" + string(b.buf[half:])
	}
	return string(b.buf)
}
