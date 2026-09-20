// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// compose_test.go — the organ combinators: a membrane stack that rules as one,
// and fallback ports that never mistake a result for a broken organ.
package nerve

import (
	"context"
	"errors"
	"testing"
)

// layer is a membrane whose ruling is fixed, with an optional error and a call
// counter, so short-circuiting is observable.
type layer struct {
	name    string
	verdict Verdict
	reason  string
	err     error
	bound   string
	calls   *int
	emit    *int
}

func (l layer) Allow(context.Context, Action) (Verdict, string, error) {
	if l.calls != nil {
		*l.calls++
	}
	return l.verdict, l.reason, l.err
}

func (l layer) Emit(context.Context, Utterance) (Verdict, string, error) {
	if l.emit != nil {
		*l.emit++
	}
	return l.verdict, l.reason, l.err
}

func (l layer) Bounds() string { return l.bound }

func TestGuardStackFirstDenyShortCircuits(t *testing.T) {
	var first, second int
	stack := GuardStack(
		layer{name: "strict", verdict: VerdictDeny, reason: "not here", calls: &first},
		layer{name: "permissive", verdict: VerdictAllow, calls: &second},
	)
	verdict, reason, err := stack.Allow(context.Background(), Action{})
	if verdict != VerdictDeny || reason != "not here" || err != nil {
		t.Fatalf("ruling = %v %q %v, want Deny \"not here\" nil", verdict, reason, err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("layer calls = %d/%d, want the stack to stop at the first Deny", first, second)
	}
}

// TestGuardStackDenyBeatsAskInEitherOrder: severity, not position, decides —
// an ask in front must not hide a denial behind it.
func TestGuardStackDenyBeatsAskInEitherOrder(t *testing.T) {
	ask := layer{name: "ask", verdict: VerdictAsk, reason: "confirm?"}
	deny := layer{name: "deny", verdict: VerdictDeny, reason: "never"}
	for _, stack := range []Sandbox{GuardStack(ask, deny), GuardStack(deny, ask)} {
		if v, _, _ := stack.Allow(context.Background(), Action{}); v != VerdictDeny {
			t.Fatalf("stack %T ruled %v, want Deny", stack, v)
		}
	}
}

func TestGuardStackAsksWhenNothingDenies(t *testing.T) {
	stack := GuardStack(
		layer{verdict: VerdictAllow},
		layer{verdict: VerdictAsk, reason: "first ask wins"},
		layer{verdict: VerdictAsk, reason: "second ask ignored"},
		layer{verdict: VerdictAllow},
	)
	verdict, reason, err := stack.Emit(context.Background(), Utterance{Text: "hi"})
	if verdict != VerdictAsk || reason != "first ask wins" || err != nil {
		t.Fatalf("ruling = %v %q %v, want the first ask", verdict, reason, err)
	}
}

// TestGuardStackMemberErrorIsDeny keeps the port's fail-closed rule inside the
// stack: a layer that could not decide is a layer that said no.
func TestGuardStackMemberErrorIsDeny(t *testing.T) {
	var later int
	stack := GuardStack(
		layer{err: errors.New("policy store down")},
		layer{verdict: VerdictAllow, calls: &later},
	)
	verdict, reason, err := stack.Allow(context.Background(), Action{})
	if verdict != VerdictDeny || err != nil {
		t.Fatalf("ruling = %v err %v, want a Deny the loop can feed back", verdict, err)
	}
	if !contains(reason, "policy store down") {
		t.Fatalf("reason = %q, want the member's error to survive", reason)
	}
	if later != 0 {
		t.Fatal("a failed layer did not end the stack")
	}
}

func TestGuardStackBoundsAndEmptiness(t *testing.T) {
	stack := GuardStack(layer{bound: ""}, layer{bound: "declared once"})
	if got := stack.Bounds(); got != "declared once" {
		t.Fatalf("bounds = %q, want the first non-empty declaration", got)
	}
	verdict, reason, err := GuardStack().Allow(context.Background(), Action{})
	if verdict != VerdictDeny || err != nil || reason == "" {
		t.Fatalf("an empty stack ruled %v %q %v, want a Deny that says why", verdict, reason, err)
	}
}

// tool is an effector that either executes or breaks.
type tool struct {
	effect *Effect
	err    error
}

func (t tool) Act(context.Context, Action) (*Effect, error) {
	if t.err != nil {
		return nil, t.err
	}
	return t.effect, nil
}

func TestFallbackEffectorSkipsBrokenTool(t *testing.T) {
	broken := errors.New("sandbox process gone")
	eff, err := FallbackEffector(tool{err: broken}, tool{effect: &Effect{Result: "ok"}}).
		Act(context.Background(), Action{})
	if err != nil || eff.Result != "ok" {
		t.Fatalf("act = %+v err %v, want the healthy tool's effect", eff, err)
	}
}

// TestFallbackEffectorNeverRetriesABusinessResult: a tool that ran and said no
// has answered; running the same call against a second tool would repeat the
// side effect the refusal was about.
func TestFallbackEffectorNeverRetriesABusinessResult(t *testing.T) {
	var second int
	eff, err := FallbackEffector(
		tool{effect: &Effect{Err: "quota exceeded"}},
		countingTool{calls: &second},
	).Act(context.Background(), Action{})
	if err != nil {
		t.Fatalf("act: %v", err)
	}
	if eff.Err != "quota exceeded" {
		t.Fatalf("effect = %+v, want the first tool's refusal carried through", eff)
	}
	if second != 0 {
		t.Fatal("a refused call was retried on the next organ")
	}
}

type countingTool struct{ calls *int }

func (c countingTool) Act(context.Context, Action) (*Effect, error) {
	*c.calls++
	return &Effect{Result: "should not run"}, nil
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
