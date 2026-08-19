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

// newTestCell creates a Cell with mock ports for testing.
func newTestCell(t *testing.T, think nerve.Thinker, act nerve.Effector) *Cell {
	t.Helper()
	return &Cell{
		ID:       "test-cell",
		Identity: nerve.Identity{Name: "tester", Role: "test"},
		Think:    think,
		Act:      act,
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
		Identity: nerve.Identity{Name: "tester", Role: "test"},
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
		Identity: nerve.Identity{Name: "tester", Role: "test"},
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
