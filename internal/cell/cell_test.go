// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// cell_test.go — Cell white-box tests: basic event flow, close behavior.
package cell

import (
	"context"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/testutil"
)

// newTestCell creates a Cell with mock ports for testing. Hooks/Sandbox/
// Budget are required ports — the cell test helper fills no-op defaults
// (the api assembly is where requiredness is enforced; cell tests focus on
// the kernel behavior).
func newTestCell(t *testing.T, think nerve.Thinker, act nerve.Effector) *Cell {
	t.Helper()
	return &Cell{
		ID:       "test-cell",
		Identity: "tester",
		Think:    think,
		Act:      act,
		Hooks: &nerve.Hooks{
			BeforeStimulate: func(ctx context.Context, p *nerve.Prompt) error { return nil },
			AfterStimulate:  func(ctx context.Context, output string) {},
			BeforeThink:     func(ctx context.Context, p *nerve.Prompt) error { return nil },
			AfterThink:      func(ctx context.Context, d *nerve.Decision) error { return nil },
			BeforeAct:       func(ctx context.Context, a *nerve.Action) error { return nil },
			AfterAct:        func(ctx context.Context, a *nerve.Action, e *nerve.Effect, err error) {},
			OnError:         func(ctx context.Context, err error) {},
			OnCycleEnd:      func(ctx context.Context, output string, _ nerve.CycleOutcome) {},
		},
		Sandbox: testutil.Sandbox{Bound: "test"},
		Budget: &nerve.ContextBudget{
			MaxTokens:   100,
			Trimmer:     func(c []string, _ int) []string { return c },
			TrimResults: func(rs []nerve.ToolResult, _ int) []nerve.ToolResult { return rs }},
	}
}

// TestCellStimulate verifies basic event flow: State→Text→Done.
func TestCellStimulate(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "hello-response"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)

	var events []nerve.Event
	for ev := range c.Stimulate(context.Background(), "do work") {
		events = append(events, ev)
	}

	// Expect: State(thinking), Text, State(done), Done
	if len(events) != 4 {
		t.Fatalf("events count = %d, want 4; events: %+v", len(events), events)
	}
	if events[0].Kind != nerve.EventState || events[0].State != nerve.StateThinking {
		t.Fatalf("events[0] = %+v, want EventState(StateThinking)", events[0])
	}
	if events[1].Kind != nerve.EventText || events[1].Text != "hello-response" {
		t.Fatalf("events[1] = %+v, want EventText(hello-response)", events[1])
	}
	if events[2].Kind != nerve.EventState || events[2].State != nerve.StateDone {
		t.Fatalf("events[2] = %+v, want EventState(StateDone)", events[2])
	}
	if events[3].Kind != nerve.EventDone || events[3].Output != "hello-response" {
		t.Fatalf("events[3] = %+v, want EventDone(hello-response)", events[3])
	}
}

// TestCellClose verifies Close marks the cell as closed.
func TestCellClose(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)

	if c.IsClosed() {
		t.Fatal("cell should not be closed initially")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !c.IsClosed() {
		t.Fatal("cell should be closed after Close()")
	}
}

// TestCellCloseIdempotent verifies Close can be called multiple times without error.
func TestCellCloseIdempotent(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)

	if err := c.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if !c.IsClosed() {
		t.Fatal("cell should be closed")
	}
}

// TestCellStimulateNilThink verifies nil Think yields EventError instead of panicking.
func TestCellStimulateNilThink(t *testing.T) {
	c := &Cell{
		ID:       "test-nil-think",
		Identity: "tester",
		Think:    nil,
		Act: testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	}

	var events []nerve.Event
	for ev := range c.Stimulate(context.Background(), "do work") {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("events count = %d, want 1", len(events))
	}
	if events[0].Kind != nerve.EventError {
		t.Fatalf("events[0] kind = %d, want EventError", events[0].Kind)
	}
	if events[0].Err == nil {
		t.Fatal("expected non-nil error")
	}
}

// TestCellStimulateNilAct verifies nil Act yields EventError instead of panicking.
func TestCellStimulateNilAct(t *testing.T) {
	c := &Cell{
		ID:       "test-nil-act",
		Identity: "tester",
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{
				Text:      "act",
				ToolCalls: []nerve.ToolCall{{ID: "t1", Name: "fn"}},
			}, nil
		}},
		Act: nil,
	}

	var events []nerve.Event
	for ev := range c.Stimulate(context.Background(), "do work") {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("events count = %d, want 1", len(events))
	}
	if events[0].Kind != nerve.EventError {
		t.Fatalf("events[0] kind = %d, want EventError", events[0].Kind)
	}
	if events[0].Err == nil {
		t.Fatal("expected non-nil error")
	}
}

// textOf runs one Stimulate and returns the first EventText ("" if none).
func textOf(c *Cell, input string) string {
	for ev := range c.Stimulate(context.Background(), input) {
		if ev.Kind == nerve.EventText {
			return ev.Text
		}
	}
	return ""
}

// TestCellReplaceThink: swapping the Think port takes effect at the next
// Stimulate and returns the previous port (dynamic wiring / plasticity).
func TestCellReplaceThink(t *testing.T) {
	first := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "old-brain"}, nil
	}}
	c := newTestCell(t, first, testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
		return &nerve.Effect{Result: "ok"}, nil
	}})
	if got := textOf(c, "x"); got != "old-brain" {
		t.Fatalf("before replace: text = %q, want old-brain", got)
	}

	second := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "new-brain"}, nil
	}}
	old, err := c.Replace("think", second)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	oldThinker, ok := old.(testutil.Thinker)
	if !ok {
		t.Fatalf("old port = %T, want testutil.Thinker", old)
	}
	if dec, err := oldThinker.Think(context.Background(), &nerve.Prompt{}); err != nil || dec.Text != "old-brain" {
		t.Fatalf("old port behaves like = (%v, %v), want old-brain text", dec, err)
	}
	if got := textOf(c, "x"); got != "new-brain" {
		t.Fatalf("after replace: text = %q, want new-brain", got)
	}
}

// TestCellReplaceErrors: unknown slots and wrong port types are rejected
// without mutating the cell.
func TestCellReplaceErrors(t *testing.T) {
	think := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "keep"}, nil
	}}
	c := newTestCell(t, think, testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
		return &nerve.Effect{Result: "ok"}, nil
	}})

	if _, err := c.Replace("closer", nil); err == nil {
		t.Fatal("unknown slot should error")
	}
	if _, err := c.Replace("think", "not-a-thinker"); err == nil {
		t.Fatal("wrong port type should error")
	}
	if got := textOf(c, "x"); got != "keep" {
		t.Fatalf("cell mutated by failed replace: text = %q, want keep", got)
	}
}

// TestCellReplaceAfterClose: Replace is a no-op after Close.
func TestCellReplaceAfterClose(t *testing.T) {
	think := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "old"}, nil
	}}
	c := newTestCell(t, think, testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
		return &nerve.Effect{Result: "ok"}, nil
	}})
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	old, err := c.Replace("think", testutil.Thinker{})
	if err != nil || old != nil {
		t.Fatalf("Replace after Close = (%v, %v), want (nil, nil)", old, err)
	}
}
