// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// cell_test.go — Cell white-box tests: basic event flow, close behavior.
package cell

import (
	"context"
	"errors"
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
		Mem: testutil.Memory{},
	}
}

// TestCellReplaceAuditSurvivesFailedPrelude: a run that dies before its
// prelude can emit the drained audits must not lose them — the next run
// delivers them first (event.go's exactly-once audit promise).
func TestCellReplaceAuditSurvivesFailedPrelude(t *testing.T) {
	think := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "ok"}, nil
	}}
	act := testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
		return &nerve.Effect{Result: "ok"}, nil
	}}
	c := newTestCell(t, think, act)
	c.Hooks.BeforeStimulate = func(ctx context.Context, p *nerve.Prompt) error {
		return errors.New("refused")
	}
	if _, err := c.Replace("act", act); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	var first []nerve.Event
	for ev := range c.Stimulate(context.Background(), "x") {
		first = append(first, ev)
	}
	if len(first) != 2 || first[0].Kind != nerve.EventState || first[0].State != nerve.StateError ||
		first[1].Kind != nerve.EventError {
		t.Fatalf("first run events = %+v, want state(error) then the prelude error", first)
	}
	c.Hooks.BeforeStimulate = func(ctx context.Context, p *nerve.Prompt) error { return nil }
	var second []nerve.Event
	for ev := range c.Stimulate(context.Background(), "x") {
		second = append(second, ev)
	}
	var audits int
	for _, ev := range second {
		if ev.Kind == nerve.EventReplace {
			audits++
		}
	}
	if audits != 1 || len(second) == 0 || second[0].Kind != nerve.EventReplace ||
		second[0].Replace == nil || second[0].Replace.Slot != "act" {
		t.Fatalf("second run = %d events, EventReplace count %d, first = %+v; want the requeued audit leading the stream",
			len(second), audits, second)
	}
}

// TestCellStimulate verifies basic event flow: State→Sandbox→Text→Done.
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

	// Expect: State(thinking), Sandbox(utterance allow), Text, State(done), Done
	if len(events) != 5 {
		t.Fatalf("events count = %d, want 5; events: %+v", len(events), events)
	}
	if events[0].Kind != nerve.EventState || events[0].State != nerve.StateThinking {
		t.Fatalf("events[0] = %+v, want EventState(StateThinking)", events[0])
	}
	if events[1].Kind != nerve.EventSandbox || events[1].Verdict.Ruling != nerve.VerdictAllow {
		t.Fatalf("events[1] = %+v, want EventSandbox(allow)", events[1])
	}
	if events[2].Kind != nerve.EventText || events[2].Text != "hello-response" {
		t.Fatalf("events[2] = %+v, want EventText(hello-response)", events[2])
	}
	if events[3].Kind != nerve.EventState || events[3].State != nerve.StateDone {
		t.Fatalf("events[3] = %+v, want EventState(StateDone)", events[3])
	}
	if events[4].Kind != nerve.EventDone || events[4].Output != "hello-response" {
		t.Fatalf("events[4] = %+v, want EventDone(hello-response)", events[4])
	}
}

// TestCellClose verifies Close marks the cell as closed and reports which call
// did it — the facade runs the host Closer exactly once, on that answer.
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
	if !c.Close() {
		t.Fatal("the first Close must report that it closed the cell")
	}
	if !c.IsClosed() {
		t.Fatal("cell should be closed after Close()")
	}
}

// TestCellCloseIdempotent verifies Close can be called repeatedly and only the
// call that performed the transition reports it.
func TestCellCloseIdempotent(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)

	if !c.Close() {
		t.Fatal("first Close must report the transition")
	}
	if c.Close() {
		t.Fatal("second Close must report that the cell was already closed")
	}
	if !c.IsClosed() {
		t.Fatal("cell should be closed")
	}
}

// TestClosedCellRefusesByIdentity verifies every refusal a closed cell makes
// carries the framework sentinel. The host matches "this agent is closed" with
// errors.Is and cannot import internal/, so the value must be the one the api
// re-exports — not a per-site message that only looks like one.
func TestClosedCellRefusesByIdentity(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)
	c.Close()

	var evs []nerve.Event
	for ev := range c.Resume(context.Background(), nerve.Session{}, nerve.Response{}) {
		evs = append(evs, ev)
	}
	if len(evs) != 1 || evs[0].Kind != nerve.EventError {
		t.Fatalf("resume on a closed cell = %+v, want one EventError", evs)
	}
	if !errors.Is(evs[0].Err, nerve.ErrCellClosed) {
		t.Fatalf("closed-cell resume err = %v, want it to match nerve.ErrCellClosed", evs[0].Err)
	}

	if _, err := c.Replace("act", testutil.Effector{}); !errors.Is(err, nerve.ErrCellClosed) {
		t.Fatalf("Replace after Close = %v, want it to match nerve.ErrCellClosed", err)
	}
}

// TestStimulateAfterCloseYieldsSentinel is the focused half of the identity
// contract above: the yielded EventError must match the sentinel.
func TestStimulateAfterCloseYieldsSentinel(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)
	c.Close()

	var evs []nerve.Event
	for ev := range c.Stimulate(context.Background(), "go") {
		evs = append(evs, ev)
	}
	if len(evs) != 1 || evs[0].Kind != nerve.EventError {
		t.Fatalf("stimulate on a closed cell = %+v, want one EventError", evs)
	}
	if !errors.Is(evs[0].Err, nerve.ErrCellClosed) {
		t.Fatalf("closed-cell event err = %v, want it to match nerve.ErrCellClosed", evs[0].Err)
	}
	// The wire is where identity has to survive a process: a journaled
	// refusal must come back matchable, not as text.
	b, err := nerve.EncodeEvent(evs[0])
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	back, err := nerve.DecodeEvent(b)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if !errors.Is(back.Err, nerve.ErrCellClosed) {
		t.Fatalf("restored event err = %v, want the sentinel back by identity (dropped: %v)", back.Err, back.Dropped)
	}
}

// TestCellStampsSequence verifies the cell stamps every event it lets out with
// an emission order and an emission moment, and that the count keeps running
// across invocations. The pair is what lets a host's journal tell "a record of
// this agent is missing" from "this agent had nothing to say".
func TestCellStampsSequence(t *testing.T) {
	c := newTestCell(t,
		testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
	)

	var seq []uint64
	var ts []int64
	for _, text := range []string{"one", "two"} {
		for ev := range c.Stimulate(context.Background(), text) {
			seq = append(seq, ev.Seq)
			ts = append(ts, ev.TS)
		}
	}
	if len(seq) < 4 {
		t.Fatalf("events = %d, want a stream long enough to order", len(seq))
	}
	for i, s := range seq {
		if s == 0 {
			t.Fatalf("event %d carries no Seq: the cell must stamp emission order", i)
		}
		if i > 0 && s <= seq[i-1] {
			t.Fatalf("Seq %d did not follow %d: emission order must strictly increase", s, seq[i-1])
		}
	}
	for i, m := range ts {
		if m <= 0 {
			t.Fatalf("event %d carries no emission moment (TS = %d)", i, m)
		}
		if i > 0 && m < ts[i-1] {
			t.Fatalf("TS went backwards: %d after %d", m, ts[i-1])
		}
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

// TestReplaceRefusesBrainSlot: the brain is the bundled organ, not a slot —
// a swap request for it is an unknown slot, and the cell is unchanged.
func TestReplaceRefusesBrainSlot(t *testing.T) {
	think := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "unchanged"}, nil
	}}
	c := newTestCell(t, think, testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
		return &nerve.Effect{Result: "ok"}, nil
	}})
	if _, err := c.Replace("think", testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "swapped"}, nil
	}}); err == nil {
		t.Fatal(`Replace("think") must error — the brain is not a swappable slot`)
	}
	if got := textOf(c, "x"); got != "unchanged" {
		t.Fatalf("after refused replace: text = %q, want unchanged", got)
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
	if _, err := c.Replace("act", "not-an-effector"); err == nil {
		t.Fatal("wrong port type should error")
	}
	if got := textOf(c, "x"); got != "keep" {
		t.Fatalf("cell mutated by failed replace: text = %q, want keep", got)
	}
}

// TestCellReplaceAfterClose: a closed cell refuses a swap — the port it would
// have held is never read, so reporting success would be a lie.
func TestCellReplaceAfterClose(t *testing.T) {
	think := testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
		return &nerve.Decision{Text: "old"}, nil
	}}
	c := newTestCell(t, think, testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
		return &nerve.Effect{Result: "ok"}, nil
	}})
	if !c.Close() {
		t.Fatal("Close must report the transition")
	}
	old, err := c.Replace("act", testutil.Effector{})
	if err == nil || old != nil {
		t.Fatalf("Replace after Close = (%v, %v), want an error and no previous port", old, err)
	}
}
