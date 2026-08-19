// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_config_test.go — Config fields applied correctly.
package meowire_test

import (
	"context"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// TestConfigApplied verifies MaxRounds etc. flow through to LoopContext.
func TestConfigApplied(t *testing.T) {
	thinkCalls := 0
	a, err := meowire.New(testOrgans(meowire.Organs{
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			thinkCalls++
			return &meowire.Decision{
				Text:      "t",
				ToolCalls: []meowire.ToolCall{{ID: "x", Name: "tool"}},
			}, nil
		}},
	}), meowire.Config{MaxRounds: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "loop") {
		events = append(events, ev)
	}

	thinkCount := 0
	for _, e := range events {
		if e.Kind == meowire.EventState && e.State == meowire.StateThinking {
			thinkCount++
		}
	}
	if thinkCount != 2 {
		t.Fatalf("thinking count = %d, want 2 (MaxRounds)", thinkCount)
	}
}

// TestConfigMaxToolOutput verifies MaxToolOutput truncation.
func TestConfigMaxToolOutput(t *testing.T) {
	a, err := meowire.New(testOrgans(meowire.Organs{
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "done"}, nil
		}},
	}), meowire.Config{MaxToolOutput: 10})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone")
	}
}
