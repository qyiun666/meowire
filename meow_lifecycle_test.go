// meow_lifecycle_test.go — lifecycle tests: Close behavior.
package meowire

import (
	"context"
	"errors"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
)

// closerStub records Close invocations.
type closerStub struct {
	called bool
}

func (c *closerStub) Close() error {
	c.called = true
	return nil
}

// TestCloseBehavior verifies Close marks the agent as closed and calls the host closer.
func TestCloseBehavior(t *testing.T) {
	cs := &closerStub{}
	a, err := New(testOrgans(Organs{Closer: cs}), Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Stimulate should work before close
	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == nerve.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone before close")
	}

	// Close
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Verify closed
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if !closed {
		t.Fatal("agent should be closed after Close()")
	}
	if !cs.called {
		t.Fatal("host closer should have been called")
	}
}

// TestStimulateAfterClose verifies Stimulate on a closed agent yields EventError with ErrCellClosed.
func TestStimulateAfterClose(t *testing.T) {
	a, err := New(testOrgans(Organs{}), Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var events []nerve.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("events count = %d, want 1", len(events))
	}
	if events[0].Kind != nerve.EventError {
		t.Fatalf("events[0] kind = %d, want EventError", events[0].Kind)
	}
	if !errors.Is(events[0].Err, ErrCellClosed) {
		t.Fatalf("events[0].Err = %v, want ErrCellClosed", events[0].Err)
	}
}
