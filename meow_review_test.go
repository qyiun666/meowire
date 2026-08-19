// meow_review_test.go — port wiring and event sequence tests.
package meowire

import (
	"context"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestPortWiring verifies hooks are invoked during Stimulate.
func TestPortWiring(t *testing.T) {
	var hookCalled bool
	a, err := New(testOrgans(Organs{
		Hooks: &nerve.Hooks{
			OnCycleEnd: func(ctx context.Context, output string) {
				hookCalled = true
			},
		},
	}), Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Consume all events
	for range a.Stimulate(context.Background(), "work") {
	}

	if !hookCalled {
		t.Fatal("OnCycleEnd hook not invoked")
	}
}

// TestStimulateEventSequence verifies full event sequence: State→Text→Done.
func TestStimulateEventSequence(t *testing.T) {
	a, err := New(testOrgans(Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "final-output"}, nil
		}},
	}), Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var events []nerve.Event
	for ev := range a.Stimulate(context.Background(), "input") {
		events = append(events, ev)
	}

	// Expected sequence: State(thinking), Text, State(done), Done
	want := []struct {
		kind nerve.EventKind
		text string
	}{
		{nerve.EventState, ""},
		{nerve.EventText, "final-output"},
		{nerve.EventState, ""},
		{nerve.EventDone, ""},
	}

	if len(events) != len(want) {
		t.Fatalf("events count = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		if events[i].Kind != w.kind {
			t.Fatalf("events[%d].Kind = %d, want %d", i, events[i].Kind, w.kind)
		}
	}
	// Verify text content
	if events[1].Text != "final-output" {
		t.Fatalf("events[1].Text = %q, want %q", events[1].Text, "final-output")
	}
	if events[3].Output != "final-output" {
		t.Fatalf("events[3].Output = %q, want %q", events[3].Output, "final-output")
	}
}
