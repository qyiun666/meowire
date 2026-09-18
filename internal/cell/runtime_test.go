// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// runtime_test.go — Cell runtime capabilities: Replace audit events,
// UpdateConfig/GetConfig, Resume facade.
package cell

import (
	"context"
	"strings"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestCellReplaceEmitsAuditAtNextStimulate verifies every successful Replace
// is drained as an EventReplace at the start of the next Stimulate (the
// moment the swap takes effect) and then cleared.
func TestCellReplaceEmitsAuditAtNextStimulate(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)
	newThink := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "swapped"}, nil
	}}
	if _, err := c.Replace("think", newThink); err != nil {
		t.Fatalf("replace: %v", err)
	}

	// First Stimulate: the audit precedes every other event.
	var events []nerve.Event
	for ev := range c.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}
	if len(events) == 0 || events[0].Kind != nerve.EventReplace {
		t.Fatalf("first event = %+v, want EventReplace; events: %+v", events[0], events)
	}
	ra := events[0].Replace
	if ra.Slot != "think" || ra.CellID != "test-cell" {
		t.Fatalf("audit = %+v, want slot think / cell test-cell", ra)
	}
	if ra.NewType != "testutil.Thinker" {
		t.Fatalf("audit new type = %q, want the swapped-in testutil.Thinker", ra.NewType)
	}

	// Second Stimulate: audits were drained — no more EventReplace.
	for ev := range c.Stimulate(context.Background(), "work") {
		if ev.Kind == nerve.EventReplace {
			t.Fatal("EventReplace emitted again after being drained")
		}
	}
}

// TestCellReplaceAuditOrder verifies multiple Replace calls are emitted in
// order and only successful swaps are recorded.
func TestCellReplaceAuditOrder(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)
	if _, err := c.Replace("think", testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "swapped"}, nil
	}}); err != nil {
		t.Fatalf("replace think: %v", err)
	}
	if _, err := c.Replace("act", testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
		return &nerve.Effect{Result: "swapped"}, nil
	}}); err != nil {
		t.Fatalf("replace act: %v", err)
	}
	// A rejected swap (wrong type) must not produce an audit.
	if _, err := c.Replace("sandbox", "not a sandbox"); err == nil {
		t.Fatal("replace sandbox with wrong type: want error")
	}

	var slots []string
	for ev := range c.Stimulate(context.Background(), "work") {
		if ev.Kind == nerve.EventReplace {
			slots = append(slots, ev.Replace.Slot)
		}
	}
	if len(slots) != 2 || slots[0] != "think" || slots[1] != "act" {
		t.Fatalf("replace audit slots = %v, want [think act] (failed swap excluded)", slots)
	}
}

// TestCellUpdateConfig verifies UpdateConfig swaps the config wholesale and
// GetConfig returns the current values (read-modify-write cycle).
func TestCellUpdateConfig(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)
	if got := c.GetConfig(); got.MaxRounds != 0 {
		t.Fatalf("initial MaxRounds = %d, want 0 (zero value)", got.MaxRounds)
	}
	next := c.GetConfig()
	next.MaxRounds = 3
	next.MaxToolOutput = 64
	c.UpdateConfig(next)
	got := c.GetConfig()
	if got.MaxRounds != 3 || got.MaxToolOutput != 64 {
		t.Fatalf("config after update = %+v, want MaxRounds=3 MaxToolOutput=64", got)
	}
}

// TestCellResumeInvalidSession verifies a zero-value Session fails through
// the Resume facade with an error event.
func TestCellResumeInvalidSession(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)
	var lastErr string
	for ev := range c.Resume(context.Background(), nerve.Session{}, nerve.Response{Answer: "x"}) {
		if ev.Kind == nerve.EventError {
			lastErr = ev.Err.Error()
		}
	}
	if !strings.Contains(lastErr, "invalid session") {
		t.Fatalf("resume error = %q, want %q", lastErr, "invalid session")
	}
}
