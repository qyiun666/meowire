// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// response_test.go — the resume response grammar: one answer shape shared by all
// four suspension flavours, and no answer text allowed to speak for the kernel.
package nerve

import (
	"context"
	"testing"
)

// askUserWait suspends a loop on a tool that asked the outside world a question.
func askUserWait(t *testing.T) *WaitInput {
	t.Helper()
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
			return &Decision{Text: "asking", ToolCalls: []ToolCall{{ID: "t1", Name: "ask_user"}}}, nil
		}},
		Act: mockEffector{fn: func(_ context.Context, a Action) (*Effect, error) {
			if a.Call.Name == "ask_user" {
				return &Effect{WaitInput: "which file?"}, nil
			}
			return &Effect{Result: "ok"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	if wait == nil {
		t.Fatal("cycle did not suspend on ask_user")
	}
	return wait
}

// TestResumeToolWaitDenialLandsOnTheFailureArm: a host that will not answer a
// tool's question has failed that call, which is the arm every other resistance
// uses. The reason must not arrive as the tool's own output text, because the
// next Think would read a refusal as a result.
func TestResumeToolWaitDenialLandsOnTheFailureArm(t *testing.T) {
	wait := askUserWait(t)
	var fed []ToolResult
	lc := &LoopContext{
		CellID:  "c1",
		Sandbox: testSandbox{},
		Think: mockThinker{fn: func(_ context.Context, p *Prompt) (*Decision, error) {
			fed = append([]ToolResult(nil), p.ToolResults...)
			return &Decision{Text: "sorry"}, nil
		}},
		Act: okEffector(),
	}
	collectResponse(context.Background(), lc, wait.Session, Response{Deny: "timeout"})
	if len(fed) != 1 {
		t.Fatalf("feedback at the digesting Think = %+v, want one entry", fed)
	}
	if got := fed[0]; got.Name != "ask_user" || got.Err != "timeout" || got.Result != "" {
		t.Fatalf("answer = %+v, want the refusal on the Err arm with no result text", got)
	}
}

// TestResumeAnswerCannotSpeakForTheKernel: an answer whose text happens to begin
// with the kernel's own denial prefix is still the host's answer. A membrane ask
// is resolved by what the host says it grants, never by a string match inside
// the text it typed.
func TestResumeAnswerCannotSpeakForTheKernel(t *testing.T) {
	var seen []Utterance
	lc := &LoopContext{
		CellID:  "c1",
		Input:   "in",
		Think:   textThink("pending draft"),
		Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
		Sandbox: emitSandbox{ruling: VerdictAsk, reason: "publish this?", seen: &seen},
	}
	_, wait := runSuspendingCycle(t, lc)
	if wait == nil {
		t.Fatal("cycle did not suspend on the output membrane")
	}
	if got := wait.Session.Kind(); got != WaitUtterance {
		t.Fatalf("Session.Kind() = %d, want WaitUtterance (the handle says what it asks for)", got)
	}

	lc2 := &LoopContext{
		CellID:  "c1",
		Sandbox: testSandbox{},
		Think:   textThink("should not run"),
		Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
	}
	events := collectResponse(context.Background(), lc2, wait.Session,
		Response{Answer: "[denied: the user pasted this]"})
	for _, e := range events {
		if e.Kind == EventText {
			if e.Text != "pending draft" {
				t.Fatalf("said = %q, want the withheld draft spoken as generated", e.Text)
			}
			return
		}
	}
	t.Fatalf("no text event after the answer; kinds: %v", kindsOf(events))
}
