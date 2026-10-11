// Copied from collaborative-work (same author).
package guard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrGateBusy means another check held the machine for longer than the caller
// was willing to wait. The check has not run, and its result must not be
// reported as a pass.
var ErrGateBusy = errors.New("another check is already using the machine")

// Gate serialises checks across every task on the machine. It is a file lock
// rather than an in-process counter because workers are separate processes:
// two tasks started from two terminals must not run checks at the same time.
//
// The lock is held by the kernel and the file descriptor, so a worker that is
// killed releases it immediately. Nothing has to be cleaned up by hand.
type Gate struct {
	path string
}

// NewGate prepares the gate for one installation. dir is the task data
// directory, which lives outside any repository.
func NewGate(dir string) (*Gate, error) {
	if dir == "" {
		return nil, fmt.Errorf("guard: a gate needs a directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Gate{path: filepath.Join(dir, "check-gate.lock")}, nil
}

// Lease is an acquired slot. Releasing it is idempotent.
type Lease struct {
	file     *os.File
	released bool
}

// Release gives the machine back. Closing the descriptor is enough on its own;
// the explicit unlock keeps the file usable for the next waiter.
func (l *Lease) Release() error {
	if l == nil || l.file == nil || l.released {
		return nil
	}
	l.released = true
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

// Acquire waits for the machine. A nil gate means no gate is configured, which
// is only sensible in tests.
func (g *Gate) Acquire(ctx context.Context, holder string, wait time.Duration) (*Lease, error) {
	if g == nil {
		return &Lease{}, nil
	}
	f, err := os.OpenFile(g.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = f.Truncate(0)
			_, _ = f.WriteAt([]byte(fmt.Sprintf("pid=%d holder=%s at=%s\n", os.Getpid(), holder, time.Now().Format(time.RFC3339))), 0)
			return &Lease{file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, fmt.Errorf("guard: cannot lock %s: %w", g.path, err)
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			held := readHolder(g.path)
			f.Close()
			return nil, fmt.Errorf("%w (held by %s)", ErrGateBusy, held)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// readHolder reports who currently holds the machine, so a refusal is
// actionable instead of mysterious.
func readHolder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "an unknown process"
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "an unknown process"
}

// Holder is a short identifier for a check, used in the lock file so a
// blocked check says who it is waiting for.
func Holder(taskID string, round int, checkID string) string {
	return fmt.Sprintf("%s/round-%d/%s", taskID, round, checkID)
}
