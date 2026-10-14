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

// ErrGateBusy means the slot was held for longer than the caller was willing
// to wait.
var ErrGateBusy = errors.New("slot is held by another worker")

// Gate is one slot of max_parallel. It is a file lock rather than an
// in-process counter because workers are separate processes.
//
// The lock is held by the kernel and the file descriptor, so a worker that is
// killed releases it immediately. Nothing has to be cleaned up by hand.
type Gate struct {
	path string
}

// NewSlotGate is slot i of max_parallel.
func NewSlotGate(dir string, i int) (*Gate, error) {
	slots := filepath.Join(dir, "slots")
	if err := os.MkdirAll(slots, 0o755); err != nil {
		return nil, err
	}
	return &Gate{path: filepath.Join(slots, fmt.Sprintf("%d.lock", i))}, nil
}

// Lease is an acquired slot. Releasing it is idempotent.
type Lease struct {
	file     *os.File
	released bool
}

// Release gives the slot back. Closing the descriptor is enough on its own;
// the explicit unlock keeps the file usable for the next waiter.
func (l *Lease) Release() error {
	if l == nil || l.file == nil || l.released {
		return nil
	}
	l.released = true
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

// Acquire waits up to wait for the slot.
func (g *Gate) Acquire(ctx context.Context, holder string, wait time.Duration) (*Lease, error) {
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

// readHolder reports who currently holds the slot, so a refusal is
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
