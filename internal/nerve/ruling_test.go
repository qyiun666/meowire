// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// ruling_test.go — the membrane answers with only three things; anything else is
// not a permission.
package nerve

import (
	"context"
	"strings"
	"testing"
)

// weirdSandbox returns a ruling this build has no name for, on both sides.
type weirdSandbox struct{ ruling Verdict }

func (w weirdSandbox) Allow(context.Context, Action) (Verdict, string, error) {
	return w.ruling, "huh?", nil
}
func (w weirdSandbox) Emit(context.Context, Utterance) (Verdict, string, error) {
	return w.ruling, "huh?", nil
}
func (w weirdSandbox) Bounds() string { return "" }

// TestUnknownRulingRefusesTheCall: a call-side ruling outside the tri-state must
// not reach the switch's default branch and execute.
func TestUnknownRulingRefusesTheCall(t *testing.T) {
	round := 0
	acted := 0
	lc := &LoopContext{
		CellID: "c1", Input: "go", Sandbox: weirdSandbox{Verdict(7)}, MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			round++
			if round == 1 {
				return &Decision{Text: "working", ToolCalls: []ToolCall{{ID: "t1", Name: "x"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(context.Context, Action) (*Effect, error) {
			acted++
			return &Effect{Result: "y"}, nil
		}},
	}
	fillRequired(lc)
	var refusal string
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		if e.Kind == EventToolResult && e.Effect != nil {
			refusal = e.Effect.Err
		}
		return true
	})
	if acted != 0 {
		t.Fatalf("the Effector ran %d times under an unknown ruling, want 0", acted)
	}
	if !strings.Contains(refusal, "[sandbox-denied: sandbox returned an unknown ruling 7]") {
		t.Fatalf("refusal = %q, want the out-of-range ruling named as the reason", refusal)
	}
}

// TestUnknownRulingWithholdsTheUtterance: the output side rules the same way —
// an answer that is not Allow does not get to speak.
func TestUnknownRulingWithholdsTheUtterance(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1", Input: "go", Sandbox: weirdSandbox{Verdict(42)}, MaxRounds: 1,
		Think: mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
			return &Decision{Text: "a draft"}, nil
		}},
	}
	fillRequired(lc)
	var said []string
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		if e.Kind == EventText {
			said = append(said, e.Text)
		}
		return true
	})
	want := "[sandbox-denied: sandbox returned an unknown ruling 42]"
	if len(said) != 1 || said[0] != want {
		t.Fatalf("uttered %v, want exactly %q", said, want)
	}
}
