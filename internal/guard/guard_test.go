package guard

import (
	"context"
	"errors"
	"testing"
)

func TestTreeRSS(t *testing.T) {
	// root 100 and its group; 101 is its child; 200 left the group (a sandbox
	// launcher) but is still a descendant; 300 is unrelated.
	table := `
  100   1 100 1000
  101 100 100  200
  102   1 100   30
  200 101 200 4000
  201 200 200    5
  300   1 300 9999
 garbage line
`
	if got := treeRSS(table, 100); got != 1000+200+30+4000+5 {
		t.Fatalf("treeRSS = %d", got)
	}
}

func TestSlotGate(t *testing.T) {
	dir := t.TempDir()
	g, err := NewSlotGate(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	held, err := g.Acquire(context.Background(), "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Acquire(context.Background(), "second", 0); !errors.Is(err, ErrGateBusy) {
		t.Fatalf("held slot: %v", err)
	}
	if err := held.Release(); err != nil || held.Release() != nil {
		t.Fatal("release must be idempotent:", err)
	}
	again, err := g.Acquire(context.Background(), "second", 0)
	if err != nil {
		t.Fatal(err)
	}
	again.Release()
}
