// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// reflection_test.go — Reflexion-support primitives: the Prompt.Reflection
// injection slot and the CycleOutcome classification carried by OnCycleEnd.
package nerve

import (
	"context"
	"errors"
	"testing"
)

// TestPromptReflectionSlot verifies the Reflection slot is a first-class
// Prompt field written back from the BeforeStimulate prototype and visible
// to the Thinker on every round of the Stimulate.
func TestPromptReflectionSlot(t *testing.T) {
	var seen []string
	thinks := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "in",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinks++
			seen = append(seen, p.Reflection)
			if thinks == 1 {
				return &Decision{Text: "try", ToolCalls: []ToolCall{{ID: "t1", Name: "toolA"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error {
				p.Reflection = "previous attempt failed: narrow the goal"
				return nil
			},
		},
	}
	fillRequired(lc)
	events := collectEvents(context.Background(), lc)
	if last := events[len(events)-1]; last.Kind != EventDone {
		t.Fatalf("last event = %v, want Done; kinds: %v", last.Kind, kindsOf(events))
	}
	want := "previous attempt failed: narrow the goal"
	if len(seen) != 2 || seen[0] != want || seen[1] != want {
		t.Fatalf("Thinker saw Reflection = %v, want %q on both rounds", seen, want)
	}
}

// TestOnCycleEndOutcomeClassification verifies OnCycleEnd classifies how the
// cycle ended — Done / Error / MaxRounds / Aborted / Suspended — so hosts
// never reverse-engineer the outcome from the event stream.
func TestOnCycleEndOutcomeClassification(t *testing.T) {
	run := func(name string, maxRounds int, think mockThinker, act mockEffector, stop func(Event) bool) CycleOutcome {
		t.Helper()
		var got CycleOutcome
		lc := &LoopContext{
			CellID:    "c1",
			Input:     "in",
			MaxRounds: maxRounds,
			Think:     think,
			Act:       act,
			Hooks: &Hooks{
				OnCycleEnd: func(ctx context.Context, output string, o CycleOutcome) {
					got = o
				},
			},
		}
		fillRequired(lc)
		(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
			if stop != nil && stop(e) {
				return false
			}
			return true
		})
		if got == 0 {
			t.Fatalf("%s: outcome not recorded", name)
		}
		return got
	}
	textThink := mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
		return &Decision{Text: "final"}, nil
	}}
	okAct := mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
		return &Effect{Result: "ok"}, nil
	}}

	if got := run("done", 3, textThink, okAct, nil); got != OutcomeDone {
		t.Fatalf("done path outcome = %v, want OutcomeDone", got)
	}
	errThink := mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
		return nil, errors.New("brain failure")
	}}
	if got := run("error", 3, errThink, okAct, nil); got != OutcomeError {
		t.Fatalf("error path outcome = %v, want OutcomeError", got)
	}
	callsThink := mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
		return &Decision{ToolCalls: []ToolCall{{ID: "t1", Name: "toolA"}}}, nil
	}}
	if got := run("maxrounds", 1, callsThink, okAct, nil); got != OutcomeMaxRounds {
		t.Fatalf("max-rounds path outcome = %v, want OutcomeMaxRounds", got)
	}
	if got := run("aborted", 3, textThink, okAct, func(e Event) bool { return true }); got != OutcomeAborted {
		t.Fatalf("abort path outcome = %v, want OutcomeAborted", got)
	}
	waitAct := mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
		return &Effect{WaitInput: "need more"}, nil
	}}
	if got := run("suspended", 3, callsThink, waitAct, nil); got != OutcomeSuspended {
		t.Fatalf("suspension path outcome = %v, want OutcomeSuspended", got)
	}
}
