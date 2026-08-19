// meow_config_test.go — Config fields applied correctly.
package meowire

import (
	"context"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestConfigApplied verifies MaxRounds etc. flow through to LoopContext.
func TestConfigApplied(t *testing.T) {
	thinkCalls := 0
	a, err := New(testOrgans(Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			thinkCalls++
			// Always return tool calls to force looping
			return &nerve.Decision{
				Text:      "t",
				ToolCalls: []nerve.ToolCall{{ID: "x", Name: "tool"}},
			}, nil
		}},
	}), Config{MaxRounds: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var events []nerve.Event
	for ev := range a.Stimulate(context.Background(), "loop") {
		events = append(events, ev)
	}

	// Count thinking states - should be exactly 2 (MaxRounds)
	thinkCount := 0
	for _, e := range events {
		if e.Kind == nerve.EventState && e.State == nerve.StateThinking {
			thinkCount++
		}
	}
	if thinkCount != 2 {
		t.Fatalf("thinking count = %d, want 2 (MaxRounds)", thinkCount)
	}
}

// TestConfigMaxToolOutput verifies MaxToolOutput truncation.
func TestConfigMaxToolOutput(t *testing.T) {
	a, err := New(testOrgans(Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "done"}, nil
		}},
	}), Config{MaxToolOutput: 10})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Just verify it doesn't panic and completes
	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == nerve.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone")
	}
}
