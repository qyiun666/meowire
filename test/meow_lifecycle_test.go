// meow_lifecycle_test.go — lifecycle tests: Close behavior.
package meowire_test

import (
	"context"
	"errors"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// TestCloseBehavior verifies Close calls the host closer and subsequent Stimulate fails.
func TestCloseBehavior(t *testing.T) {
	cs := &closerStub{}
	a, err := meowire.New(testOrgans(meowire.Organs{Closer: cs}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Stimulate should work before close
	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventDone {
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
	if !cs.called {
		t.Fatal("host closer should have been called")
	}

	// Stimulate after close should yield ErrCellClosed
	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("post-close events = %d, want 1", len(events))
	}
	if events[0].Kind != meowire.EventError {
		t.Fatalf("post-close event kind = %d, want EventError", events[0].Kind)
	}
	if !errors.Is(events[0].Err, meowire.ErrCellClosed) {
		t.Fatalf("post-close event err = %v, want ErrCellClosed", events[0].Err)
	}
}

// TestStimulateAfterClose verifies Stimulate on a closed agent yields EventError with ErrCellClosed.
func TestStimulateAfterClose(t *testing.T) {
	a, err := meowire.New(testOrgans(meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("events count = %d, want 1", len(events))
	}
	if events[0].Kind != meowire.EventError {
		t.Fatalf("events[0] kind = %d, want EventError", events[0].Kind)
	}
	if !errors.Is(events[0].Err, meowire.ErrCellClosed) {
		t.Fatalf("events[0].Err = %v, want ErrCellClosed", events[0].Err)
	}
}
