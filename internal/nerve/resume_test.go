// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// resume_test.go — DecisionLoop suspension (EventWaitInput) and Resume tests.
package nerve

import (
	"context"
	"strings"
	"testing"
)

// collectResume runs Resume and returns all yielded events.
func collectResume(ctx context.Context, lc *LoopContext, sess Session, response string) []Event {
	fillRequired(lc)
	var events []Event
	(DecisionLoop{}).Resume(ctx, lc, sess, response, func(e Event) bool {
		events = append(events, e)
		return true
	})
	return events
}

// kindsOf returns the EventKind sequence of events (test assertion helper).
func kindsOf(events []Event) []EventKind {
	kinds := make([]EventKind, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// runSuspendingCycle runs one Cycle with a tool list and returns the yielded
// events plus the first EventWaitInput payload (nil if none).
func runSuspendingCycle(t *testing.T, lc *LoopContext) ([]Event, *WaitInput) {
	t.Helper()
	var events []Event
	var wait *WaitInput
	fillRequired(lc)
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		events = append(events, e)
		if e.Kind == EventWaitInput {
			wait = e.Wait
		}
		return true
	})
	return events, wait
}

// TestDecisionLoopWaitInputSuspends verifies a tool returning
// Effect.WaitInput suspends the loop: StateWaiting + EventWaitInput are
// yielded, the iterator ends normally (no Done, no Error), and the Session
// snapshots the suspension state.
func TestDecisionLoopWaitInputSuspends(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "asking", ToolCalls: []ToolCall{{ID: "t1", Name: "ask_user", Args: `{"q":"ok?"}`}}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{WaitInput: "may I proceed?"}, nil
		}},
	}
	events, wait := runSuspendingCycle(t, lc)

	want := []EventKind{EventState, EventText, EventState, EventToolCall, EventSandbox, EventState, EventWaitInput}
	if !slicesEqual(kindsOf(events), want) {
		t.Fatalf("events = %v, want %v", kindsOf(events), want)
	}
	if wait == nil {
		t.Fatal("no EventWaitInput yielded")
	}
	if wait.CellID != "c1" || wait.Call.Name != "ask_user" || wait.Question != "may I proceed?" {
		t.Fatalf("wait = %+v, want c1/ask_user/may I proceed?", wait)
	}
	sess := wait.Session
	if !sess.valid() {
		t.Fatal("session invalid")
	}
	if len(sess.remaining) != 0 {
		t.Fatalf("session remaining = %+v, want none (single tool)", sess.remaining)
	}
	// The suspending tool has no result yet — the snapshot must not contain it.
	if strings.Contains(strings.Join(sess.context, "\n"), "ask_user") {
		t.Fatalf("session context already contains the suspending tool feedback: %v", sess.context)
	}
}

// TestDecisionLoopWaitInputMidList verifies a suspension in the middle of a
// round's tool list: tools before it run, the Session captures the calls
// after it, and tools after it do not execute until Resume.
func TestDecisionLoopWaitInputMidList(t *testing.T) {
	var executed []string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "multi", ToolCalls: []ToolCall{
				{ID: "t1", Name: "tool1"}, {ID: "t2", Name: "ask_user"}, {ID: "t3", Name: "tool3"},
			}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			if a.Call.Name == "ask_user" {
				return &Effect{WaitInput: "confirm?"}, nil
			}
			executed = append(executed, a.Call.Name)
			return &Effect{Result: "ok"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	if wait == nil {
		t.Fatal("no EventWaitInput yielded")
	}
	if len(executed) != 1 || executed[0] != "tool1" {
		t.Fatalf("executed = %v, want [tool1] (tool3 must wait for Resume)", executed)
	}
	rem := wait.Session.remaining
	if len(rem) != 1 || rem[0].Name != "tool3" {
		t.Fatalf("session remaining = %+v, want [tool3]", rem)
	}
	// The snapshot keeps the tool1 feedback in the accumulated context.
	if !strings.Contains(strings.Join(wait.Session.context, "\n"), "[tool1]") {
		t.Fatalf("session context missing tool1 feedback: %v", wait.Session.context)
	}
}

// TestDecisionLoopResumeNoExtraRound verifies the suspension consumes no
// round quota: with MaxRounds=1 the suspension followed by Resume still
// completes normally (the digesting Think uses the suspended round's quota).
func TestDecisionLoopResumeNoExtraRound(t *testing.T) {
	thinks := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "work",
		MaxRounds: 1,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinks++
			if thinks == 1 {
				return &Decision{Text: "ask", ToolCalls: []ToolCall{{ID: "t1", Name: "ask_user"}}}, nil
			}
			return &Decision{Text: "final"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{WaitInput: "q?"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	if wait == nil {
		t.Fatal("no EventWaitInput yielded")
	}
	events := collectResume(context.Background(), lc, wait.Session, "yes")
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone (no ErrMaxRounds); events: %v", last, kindsOf(events))
	}
	if last.Output != "askfinal" {
		t.Fatalf("done output = %q, want %q (suspension output must be preserved)", last.Output, "askfinal")
	}
}

// TestDecisionLoopResumeFeedbackForm verifies the external response enters
// the next Think's context as "[tool] <response>".
func TestDecisionLoopResumeFeedbackForm(t *testing.T) {
	thinks := 0
	var nextContext []string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinks++
			if thinks == 1 {
				return &Decision{Text: "ask", ToolCalls: []ToolCall{{ID: "t1", Name: "ask_user"}}}, nil
			}
			nextContext = append([]string(nil), p.Context...)
			return &Decision{Text: "final"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{WaitInput: "q?"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	collectResume(context.Background(), lc, wait.Session, "yes")
	found := false
	for _, c := range nextContext {
		if c == "[ask_user] yes" {
			found = true
		}
	}
	if !found {
		t.Fatalf("resume response missing from next Think context: %v", nextContext)
	}
}

// TestDecisionLoopResumeRunsRemaining verifies Resume first finishes the
// suspended round's remaining tool calls, then re-enters the Think phase.
func TestDecisionLoopResumeRunsRemaining(t *testing.T) {
	var executed []string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "multi", ToolCalls: []ToolCall{
				{ID: "t1", Name: "ask_user"}, {ID: "t2", Name: "tool2"},
			}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			if a.Call.Name == "ask_user" {
				return &Effect{WaitInput: "q?"}, nil
			}
			executed = append(executed, a.Call.Name)
			return &Effect{Result: "ok"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	events := collectResume(context.Background(), lc, wait.Session, "yes")
	if len(executed) != 1 || executed[0] != "tool2" {
		t.Fatalf("executed = %v, want [tool2]", executed)
	}
	// The remaining tool must run before the digesting Think.
	thinkIdx, toolIdx := -1, -1
	for i, e := range events {
		switch e.Kind {
		case EventState:
			if e.State == StateThinking {
				thinkIdx = i
			}
		case EventToolCall:
			if e.ToolCall != nil && e.ToolCall.Name == "tool2" {
				toolIdx = i
			}
		}
	}
	if toolIdx == -1 || thinkIdx == -1 || toolIdx > thinkIdx {
		t.Fatalf("tool2 (idx %d) must run before the digesting Think (idx %d); events: %v", toolIdx, thinkIdx, kindsOf(events))
	}
}

// TestDecisionLoopResumeReSuspends verifies a tool that suspends again
// during Resume yields a fresh EventWaitInput and ends the iterator normally.
func TestDecisionLoopResumeReSuspends(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "multi", ToolCalls: []ToolCall{
				{ID: "t1", Name: "ask_user"}, {ID: "t2", Name: "ask_again"},
			}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			if a.Call.Name == "ask_user" {
				return &Effect{WaitInput: "first?"}, nil
			}
			return &Effect{WaitInput: "second?"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	var second *WaitInput
	events := collectResume(context.Background(), lc, wait.Session, "yes")
	for _, e := range events {
		if e.Kind == EventWaitInput {
			second = e.Wait
		}
	}
	if second == nil {
		t.Fatalf("no second EventWaitInput yielded; events: %v", kindsOf(events))
	}
	if second.Call.Name != "ask_again" || second.Question != "second?" {
		t.Fatalf("second wait = %+v, want ask_again/second?", second)
	}
	last := events[len(events)-1]
	if last.Kind != EventWaitInput {
		t.Fatalf("last event = %+v, want EventWaitInput (iterator ends on suspension)", last)
	}
}

// TestDecisionLoopResumeInvalidSession verifies a zero-value Session is
// rejected with an error event (OnCycleEnd still runs exactly once).
func TestDecisionLoopResumeInvalidSession(t *testing.T) {
	cycleEnd := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "ok"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{OnCycleEnd: func(ctx context.Context, output string) { cycleEnd++ }},
	}
	events := collectResume(context.Background(), lc, Session{}, "x")
	if events[len(events)-1].Kind != EventError {
		t.Fatalf("last event = %+v, want EventError for invalid session", events[len(events)-1])
	}
	if cycleEnd != 1 {
		t.Fatalf("OnCycleEnd calls = %d, want 1", cycleEnd)
	}
}

// TestDecisionLoopResumeCtxCancel verifies a canceled Resume context fails
// fast through the prologue.
func TestDecisionLoopResumeCtxCancel(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "ok"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A valid session: the cancellation must fail through the prologue, not
	// through the session validity check.
	sess := Session{}.snapshot(1, "w", "", []string{}, "", ToolCall{ID: "t1", Name: "ask_user"}, nil)
	events := collectResume(ctx, lc, sess, "yes")
	last := events[len(events)-1]
	if last.Kind != EventError {
		t.Fatalf("last event = %+v, want EventError on canceled context", last)
	}
}

// TestDecisionLoopWaitInputNoBudgetDuringWait verifies the budget trimmer
// runs only before each Think — the suspension and the resume injection add
// no trimming on their own.
func TestDecisionLoopWaitInputNoBudgetDuringWait(t *testing.T) {
	trims := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Budget: &ContextBudget{MaxTokens: 100, Trimmer: func(c []string, _ int) []string { trims++; return c }},
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			if trims == 1 {
				return &Decision{Text: "ask", ToolCalls: []ToolCall{{ID: "t1", Name: "ask_user"}}}, nil
			}
			return &Decision{Text: "final"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{WaitInput: "q?"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	if trims != 1 {
		t.Fatalf("trimmer calls after suspension = %d, want 1 (only the first Think trimmed)", trims)
	}
	collectResume(context.Background(), lc, wait.Session, "yes")
	if trims != 2 {
		t.Fatalf("trimmer calls after resume = %d, want 2 (one per Think, none during wait)", trims)
	}
}

// TestDecisionLoopResumeCycleEndGuarantee verifies OnCycleEnd fires exactly
// once per invocation — Cycle on the suspension path and Resume on the
// completion path.
func TestDecisionLoopResumeCycleEndGuarantee(t *testing.T) {
	cycleEnd := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "ask", ToolCalls: []ToolCall{{ID: "t1", Name: "ask_user"}}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{WaitInput: "q?"}, nil
		}},
		Hooks: &Hooks{OnCycleEnd: func(ctx context.Context, output string) { cycleEnd++ }},
	}
	_, wait := runSuspendingCycle(t, lc)
	if cycleEnd != 1 {
		t.Fatalf("OnCycleEnd calls after suspension = %d, want 1", cycleEnd)
	}
	collectResume(context.Background(), lc, wait.Session, "yes")
	if cycleEnd != 2 {
		t.Fatalf("OnCycleEnd calls after resume = %d, want 2 (once per invocation)", cycleEnd)
	}
}

// TestDecisionLoopPendingReplaceEvents verifies pending Replace audits are
// emitted as EventReplace before any other event.
func TestDecisionLoopPendingReplaceEvents(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "ok"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	oldThink := lc.Think
	newThink := mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
		return &Decision{Text: "new"}, nil
	}}
	lc.PendingReplace = []ReplaceAudit{
		{CellID: "c1", Slot: "think", Old: oldThink, New: newThink},
		{CellID: "c1", Slot: "budget", Old: lc.Budget, New: lc.Budget},
	}
	events := collectEvents(context.Background(), lc)
	if len(events) < 2 || events[0].Kind != EventReplace || events[1].Kind != EventReplace {
		t.Fatalf("first events = %v, want two EventReplace before anything else", kindsOf(events))
	}
	if events[0].Replace.Slot != "think" || events[1].Replace.Slot != "budget" {
		t.Fatalf("replace slots = %q/%q, want think/budget (in order)", events[0].Replace.Slot, events[1].Replace.Slot)
	}
	if _, ok := events[0].Replace.Old.(mockThinker); !ok {
		t.Fatalf("think audit old = %T, want mockThinker", events[0].Replace.Old)
	}
	if _, ok := events[0].Replace.New.(mockThinker); !ok {
		t.Fatalf("think audit new = %T, want mockThinker", events[0].Replace.New)
	}
}

// slicesEqual reports whether two EventKind slices are equal in order.
func slicesEqual(a, b []EventKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
