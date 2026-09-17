// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wait_test.go — the round's single suspension: who claims it, and what happens
// to a wait that arrives second.
package nerve

import (
	"context"
	"strings"
	"testing"
)

// waitHarness runs one Cycle with a fixed decision and per-call effects,
// returning the suspension the loop yielded and the effects it fed back.
func waitHarness(t *testing.T, lc *LoopContext, calls []ToolCall, act func(Action) (*Effect, error)) (*WaitInput, map[string]*Effect) {
	t.Helper()
	round := 0
	lc.Think = mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
		round++
		if round == 1 {
			return &Decision{Text: "run", ToolCalls: calls}, nil
		}
		return &Decision{Text: "done"}, nil
	}}
	lc.Act = mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) { return act(a) }}
	lc.MaxRounds = 3
	fillRequired(lc)
	var wait *WaitInput
	fedBack := map[string]*Effect{}
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		switch e.Kind {
		case EventWaitInput:
			wait = e.Wait
		case EventToolResult:
			if e.ToolCall != nil {
				fedBack[e.ToolCall.ID] = e.Effect
			}
		}
		return true
	})
	return wait, fedBack
}

// TestWaitInputWinsOverSend: an Effect that carries both its own question and a
// delegation asks the loop, not a peer. The port contract gives WaitInput
// precedence, so the send never leaves and the tool's own text is what the host
// is asked — not the delivery note of a request nobody made.
func TestWaitInputWinsOverSend(t *testing.T) {
	var delivered []Signal
	lc := &LoopContext{
		CellID: "c1", Input: "go",
		Send: func(ctx context.Context, sig Signal) (string, error) {
			delivered = append(delivered, sig)
			return "c1/1", nil
		},
	}
	wait, fedBack := waitHarness(t, lc,
		[]ToolCall{{ID: "t1", Name: "both"}},
		func(a Action) (*Effect, error) {
			return &Effect{WaitInput: "may I?", Send: &Signal{To: "peer"}}, nil
		})
	if wait == nil {
		t.Fatalf("no suspension yielded; feedback: %v", fedBack)
	}
	if wait.Question != "may I?" {
		t.Errorf("question = %q, want the tool's own text", wait.Question)
	}
	if len(delivered) != 0 {
		t.Errorf("delivered %v, want the delegation withheld while the call asks on its own", delivered)
	}
}

// TestParallelSecondWaitRefused: a round has one suspension to spend, and a
// parallel batch cannot queue a second one behind the first. The later wait is
// refused with tool feedback naming the claimant; it never returns as an empty
// success, because nothing would ever answer it.
func TestParallelSecondWaitRefused(t *testing.T) {
	lc := &LoopContext{CellID: "c1", Input: "go", ParallelActs: true}
	wait, fedBack := waitHarness(t, lc,
		[]ToolCall{{ID: "t1", Name: "askFirst"}, {ID: "t2", Name: "askSecond"}},
		func(a Action) (*Effect, error) {
			if a.Call.Name == "askFirst" {
				return &Effect{WaitInput: "first?"}, nil
			}
			return &Effect{WaitInput: "second?", Result: "ignored"}, nil
		})
	if wait == nil {
		t.Fatalf("no suspension yielded; feedback: %v", fedBack)
	}
	if wait.Question != "first?" {
		t.Errorf("question = %q, want the first call's", wait.Question)
	}
	second := fedBack["t2"]
	if second == nil {
		t.Fatalf("the sibling call produced no EventToolResult; got %v", fedBack)
	}
	if second.WaitInput != "" {
		t.Errorf("refused effect still carries WaitInput %q", second.WaitInput)
	}
	if !strings.Contains(second.Err, "one wait per round") || !strings.Contains(second.Err, "askFirst") {
		t.Errorf("refused effect err = %q, want it to name the claimant", second.Err)
	}
}
