// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// loop_test.go — DecisionLoop white-box tests.
package nerve

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockThinker is a test-only Thinker stub (local: nerve white-box tests cannot
// import testutil without an import cycle).
type mockThinker struct {
	fn func(ctx context.Context, p *Prompt) (*Decision, error)
}

func (m mockThinker) Think(ctx context.Context, p *Prompt) (*Decision, error) {
	return m.fn(ctx, p)
}

// mockEffector is a test-only Effector stub (local for the same reason).
type mockEffector struct {
	fn func(ctx context.Context, a Action) (*Effect, error)
}

func (m mockEffector) Act(ctx context.Context, a Action) (*Effect, error) {
	return m.fn(ctx, a)
}

// collectEvents runs Cycle and returns all yielded events.
func collectEvents(ctx context.Context, lc *LoopContext) []Event {
	var events []Event
	(DecisionLoop{}).Cycle(ctx, lc, func(e Event) bool {
		events = append(events, e)
		return true
	})
	return events
}

// TestDecisionLoopBasicFlow: Think returns no ToolCalls → EventState(thinking) → EventText → EventState(done) → EventDone.
func TestDecisionLoopBasicFlow(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "hello",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "response"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	// Expect: State(thinking), Text, State(done), Done
	if len(events) != 4 {
		t.Fatalf("events count = %d, want 4; events: %+v", len(events), events)
	}
	if events[0].Kind != EventState || events[0].State != StateThinking {
		t.Fatalf("events[0] = %+v, want EventState(StateThinking)", events[0])
	}
	if events[1].Kind != EventText || events[1].Text != "response" {
		t.Fatalf("events[1] = %+v, want EventText(response)", events[1])
	}
	if events[2].Kind != EventState || events[2].State != StateDone {
		t.Fatalf("events[2] = %+v, want EventState(StateDone)", events[2])
	}
	if events[3].Kind != EventDone || events[3].Output != "response" {
		t.Fatalf("events[3] = %+v, want EventDone(response)", events[3])
	}
}

// TestDecisionLoopWithToolCalls: Think returns ToolCalls → EventToolCall + EventToolResult → loops back.
func TestDecisionLoopWithToolCalls(t *testing.T) {
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "compute",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{
					Text:      "need-tool",
					ToolCalls: []ToolCall{{ID: "t1", Name: "calc", Args: "1+1"}},
				}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "2"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	// Expect: State(thinking), Text("need-tool"), State(acting), EventToolCall, EventToolResult,
	//        State(thinking), Text("done"), State(done), Done
	kinds := make([]EventKind, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	wantKinds := []EventKind{
		EventState, EventText, EventState, EventToolCall, EventToolResult,
		EventState, EventText, EventState, EventDone,
	}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("event kinds = %v, want %v", kinds, wantKinds)
	}
	for i, k := range kinds {
		if k != wantKinds[i] {
			t.Fatalf("event[%d] kind = %d, want %d", i, k, wantKinds[i])
		}
	}
}

// TestDecisionLoopMaxRounds: Verify loop stops at MaxRounds and yields ErrMaxRounds when tool calls remain.
func TestDecisionLoopMaxRounds(t *testing.T) {
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "loop",
		MaxRounds: 2,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{
				Text:      "t",
				ToolCalls: []ToolCall{{ID: "x", Name: "tool"}},
			}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	// Last event should be EventError with ErrMaxRounds
	last := events[len(events)-1]
	if last.Kind != EventError {
		t.Fatalf("last event kind = %d, want EventError", last.Kind)
	}
	if !errors.Is(last.Err, ErrMaxRounds) {
		t.Fatalf("last event err = %v, want ErrMaxRounds", last.Err)
	}
	// EventState(StateError) should precede the error event
	prev := events[len(events)-2]
	if prev.Kind != EventState || prev.State != StateError {
		t.Fatalf("event[len-2] = %+v, want EventState(StateError)", prev)
	}
	// Should have exactly 2 rounds of thinking
	thinkCount := 0
	for _, e := range events {
		if e.Kind == EventState && e.State == StateThinking {
			thinkCount++
		}
	}
	if thinkCount != 2 {
		t.Fatalf("thinking count = %d, want 2 (MaxRounds)", thinkCount)
	}
}

// TestDecisionLoopHooks: Verify BeforeThink/AfterThink/BeforeAct/AfterAct/OnCycleEnd are called.
func TestDecisionLoopHooks(t *testing.T) {
	var hookCalls []string
	calls := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "hi",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{
					Text:      "act",
					ToolCalls: []ToolCall{{ID: "t1", Name: "fn"}},
				}, nil
			}
			return &Decision{Text: "end"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeThink: func(ctx context.Context, p *Prompt) error {
				hookCalls = append(hookCalls, "BeforeThink")
				return nil
			},
			AfterThink: func(ctx context.Context, d *Decision) error {
				hookCalls = append(hookCalls, "AfterThink")
				return nil
			},
			BeforeAct: func(ctx context.Context, a *Action) error {
				hookCalls = append(hookCalls, "BeforeAct")
				return nil
			},
			AfterAct: func(ctx context.Context, a *Action, e *Effect, err error) {
				hookCalls = append(hookCalls, "AfterAct")
			},
			OnCycleEnd: func(ctx context.Context, output string) {
				hookCalls = append(hookCalls, "OnCycleEnd")
			},
		},
	}
	collectEvents(context.Background(), lc)

	want := []string{"BeforeThink", "AfterThink", "BeforeAct", "AfterAct", "BeforeThink", "AfterThink", "OnCycleEnd"}
	if len(hookCalls) != len(want) {
		t.Fatalf("hook calls = %v, want %v", hookCalls, want)
	}
	for i, w := range want {
		if hookCalls[i] != w {
			t.Fatalf("hook[%d] = %q, want %q", i, hookCalls[i], w)
		}
	}
}

// TestDecisionLoopToolErrorBackfill: Tool returns error → feedback contains error text → next Think sees it in Context.
func TestDecisionLoopToolErrorBackfill(t *testing.T) {
	var capturedCtx []string
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "fix",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 2 {
				capturedCtx = p.Context
			}
			if calls == 1 {
				return &Decision{
					Text:      "try",
					ToolCalls: []ToolCall{{ID: "t1", Name: "broken"}},
				}, nil
			}
			return &Decision{Text: "recovered"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return nil, errors.New("boom")
		}},
	}
	collectEvents(context.Background(), lc)

	// Verify that the second Think call received context containing the error
	found := false
	for _, c := range capturedCtx {
		if strings.Contains(c, "boom") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("context = %v, want entry containing 'boom'", capturedCtx)
	}
}

// TestDecisionLoopSandboxDeny: Sandbox returns denied → feedback contains "[denied: ...]".
func TestDecisionLoopSandboxDeny(t *testing.T) {
	var capturedCtx []string
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "dangerous",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 2 {
				capturedCtx = p.Context
			}
			if calls == 1 {
				return &Decision{
					Text:      "do-it",
					ToolCalls: []ToolCall{{ID: "t1", Name: "rm"}},
				}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "deleted"}, nil
		}},
		Sandbox: &denySandbox{},
	}
	collectEvents(context.Background(), lc)

	found := false
	for _, c := range capturedCtx {
		if strings.Contains(c, "[denied:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("context = %v, want entry containing '[denied:'", capturedCtx)
	}
}

// denySandbox always denies.
type denySandbox struct{}

func (denySandbox) Allow(ctx context.Context, a Action) (bool, string, error) {
	return false, "not allowed", nil
}

// TestDecisionLoopYieldFalseStops: yield returns false → loop stops immediately.
func TestDecisionLoopYieldFalseStops(t *testing.T) {
	thinkCalls := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "stop",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinkCalls++
			return &Decision{Text: "text"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	var events []Event
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		events = append(events, e)
		return false // stop immediately
	})
	if len(events) != 1 {
		t.Fatalf("events count = %d, want 1 (stopped immediately)", len(events))
	}
}

// TestDecisionLoopOnCycleEndOnAbort verifies OnCycleEnd still runs exactly once,
// with the full final output, when the consumer stops at the State(done) event.
func TestDecisionLoopOnCycleEndOnAbort(t *testing.T) {
	cycleEndCalls := 0
	var cycleEndOutput string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "abort",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "final answer"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			OnCycleEnd: func(ctx context.Context, output string) {
				cycleEndCalls++
				cycleEndOutput = output
			},
		},
	}
	var got []EventKind
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		got = append(got, e.Kind)
		// Stop right after EventState(StateDone) — before EventDone is consumed.
		return len(got) < 3
	})
	if cycleEndCalls != 1 {
		t.Fatalf("OnCycleEnd calls = %d, want 1", cycleEndCalls)
	}
	if cycleEndOutput != "final answer" {
		t.Fatalf("OnCycleEnd output = %q, want %q", cycleEndOutput, "final answer")
	}
}

// TestDecisionLoopAbortBeforeActSkipsToolExecution verifies stopping at an
// EventToolCall prevents the tool from executing — the anchor point of the
// host-driven resume model (stop iterator, execute host-side, re-Stimulate).
func TestDecisionLoopAbortBeforeActSkipsToolExecution(t *testing.T) {
	actCalls := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "resume",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{
				Text:      "need-tool",
				ToolCalls: []ToolCall{{ID: "t1", Name: "slow"}},
			}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			return &Effect{Result: "ok"}, nil
		}},
	}
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		return e.Kind != EventToolCall // stop as soon as the tool call is announced
	})
	if actCalls != 0 {
		t.Fatalf("Act calls = %d, want 0 (tool must not execute after abort)", actCalls)
	}
}

// TestDecisionLoopThinkRetry: Think fails then succeeds → no error yielded.
func TestDecisionLoopThinkRetry(t *testing.T) {
	calls := 0
	lc := &LoopContext{
		CellID:     "c1",
		Input:      "retry",
		MaxRetries: 2,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("transient")
			}
			return &Decision{Text: "recovered"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	// No error event
	for _, e := range events {
		if e.Kind == EventError {
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}
	// Should have recovered
	last := events[len(events)-1]
	if last.Kind != EventDone || last.Output != "recovered" {
		t.Fatalf("last event = %+v, want EventDone(recovered)", last)
	}
}

// TestLoopStateString: Verify String() for each state.
func TestLoopStateString(t *testing.T) {
	tests := []struct {
		state LoopState
		want  string
	}{
		{StateIdle, "idle"},
		{StateThinking, "thinking"},
		{StateActing, "acting"},
		{StatePaused, "paused"},
		{StateDone, "done"},
		{StateError, "error"},
		{LoopState(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.state.String()
		if got != tt.want {
			t.Fatalf("LoopState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

// TestDecisionLoopOnCycleEndOnError verifies OnCycleEnd is called when Thinker returns error.
func TestDecisionLoopOnCycleEndOnError(t *testing.T) {
	var cycleEndCalled bool
	lc := &LoopContext{
		CellID: "c1",
		Input:  "fail",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return nil, errors.New("thinker failed")
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			OnCycleEnd: func(ctx context.Context, output string) {
				cycleEndCalled = true
			},
		},
	}
	collectEvents(context.Background(), lc)

	if !cycleEndCalled {
		t.Fatal("OnCycleEnd should have been called on error path")
	}
}

// TestToolFeedbackUTF8Truncation verifies toolFeedback handles multi-byte UTF-8 correctly.
func TestToolFeedbackUTF8Truncation(t *testing.T) {
	tc := ToolCall{Name: "test"}

	// Multi-byte string: "你好世界" is 12 bytes (3 bytes per char)
	eff := &Effect{Result: "你好世界"}

	// Truncate at 5 bytes (falls inside second character)
	fb := toolFeedback(tc, eff, nil, 5)
	if fb == "" {
		t.Fatal("expected non-empty feedback")
	}
	if !strings.Contains(fb, "[truncated,") {
		t.Fatalf("expected truncation marker, got: %q", fb)
	}

	// Truncate at 1 byte (falls inside first character)
	fb2 := toolFeedback(tc, eff, nil, 1)
	if fb2 == "" {
		t.Fatal("expected non-empty feedback for maxLen=1")
	}
	if !strings.Contains(fb2, "[truncated,") {
		t.Fatalf("expected truncation marker for maxLen=1, got: %q", fb2)
	}
}

// TestDecisionLoopContextBudget verifies Budget.Trimmer is called and context is trimmed.
func TestDecisionLoopContextBudget(t *testing.T) {
	trimmerCalled := false
	var capturedCtx []string
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "budget-test",
		MaxRounds: 3,
		Context:   []string{"old1", "old2", "old3"},
		Budget: &ContextBudget{
			MaxTokens: 10,
			Trimmer: func(ctx []string, maxTokens int) []string {
				trimmerCalled = true
				// Keep only last element
				if len(ctx) > 1 {
					return ctx[len(ctx)-1:]
				}
				return ctx
			},
		},
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				capturedCtx = p.Context
				return &Decision{Text: "done"}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	collectEvents(context.Background(), lc)

	if !trimmerCalled {
		t.Fatal("Budget.Trimmer should have been called")
	}
	// After trimming, context should have only 1 element
	if len(capturedCtx) != 1 {
		t.Fatalf("captured context len = %d, want 1 (trimmed)", len(capturedCtx))
	}
	if capturedCtx[0] != "old3" {
		t.Fatalf("captured context[0] = %q, want 'old3'", capturedCtx[0])
	}
}

// TestDecisionLoopCtxCancelBetweenRounds verifies loop terminates when context is cancelled.
func TestDecisionLoopCtxCancelBetweenRounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actCalls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "cancel-test",
		MaxRounds: 10,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{
				Text:      "need-tool",
				ToolCalls: []ToolCall{{ID: "t1", Name: "tool"}},
			}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			if actCalls == 1 {
				// Cancel context after first act
				cancel()
			}
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(ctx, lc)

	// Should have terminated before MaxRounds (10)
	thinkCount := 0
	for _, e := range events {
		if e.Kind == EventState && e.State == StateThinking {
			thinkCount++
		}
	}
	if thinkCount >= 10 {
		t.Fatalf("thinking count = %d, expected loop to terminate early due to ctx cancel", thinkCount)
	}

	// Should have an error event
	hasError := false
	for _, e := range events {
		if e.Kind == EventError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatal("expected EventError due to context cancellation")
	}
}

// TestCtxCancelOnCycleEndOnce verifies OnCycleEnd is called exactly once on ctx cancellation.
func TestCtxCancelOnCycleEndOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "cancel-once",
		MaxRounds: 5,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{
				Text:      "t",
				ToolCalls: []ToolCall{{ID: "t1", Name: "tool"}},
			}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			cancel() // cancel after first act
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			OnCycleEnd: func(ctx context.Context, output string) {
				calls++
			},
		},
	}
	collectEvents(ctx, lc)

	if calls != 1 {
		t.Fatalf("OnCycleEnd calls = %d, want 1", calls)
	}
}

// TestErrorPathYieldsStateError verifies error paths yield EventState(StateError) before EventError.
func TestErrorPathYieldsStateError(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "fail",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return nil, errors.New("thinker failed")
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	// Expect: State(thinking), State(error), EventError
	if len(events) != 3 {
		t.Fatalf("events count = %d, want 3; events: %+v", len(events), events)
	}
	if events[1].Kind != EventState || events[1].State != StateError {
		t.Fatalf("events[1] = %+v, want EventState(StateError)", events[1])
	}
	if events[2].Kind != EventError {
		t.Fatalf("events[2] = %+v, want EventError", events[2])
	}
}

// TestEventUsage verifies EventUsage is yielded after EventText when Decision.Usage is set.
func TestEventUsage(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "usage",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{
				Text:  "response",
				Usage: &Usage{Prompt: 10, Completion: 5, Total: 15},
			}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	// Expect: State(thinking), Text, Usage, State(done), Done
	if len(events) != 5 {
		t.Fatalf("events count = %d, want 5; events: %+v", len(events), events)
	}
	u := events[2]
	if u.Kind != EventUsage || u.Usage == nil {
		t.Fatalf("events[2] = %+v, want EventUsage with Usage", u)
	}
	if u.Usage.Total != 15 {
		t.Fatalf("usage total = %d, want 15", u.Usage.Total)
	}
	if events[3].Kind != EventState || events[3].State != StateDone {
		t.Fatalf("events[3] = %+v, want EventState(StateDone)", events[3])
	}
}

// TestDecisionLoopStimulateHooks verifies BeforeStimulate/AfterStimulate fire
// exactly once on the normal path, in boundary order.
func TestDecisionLoopStimulateHooks(t *testing.T) {
	var hookCalls []string
	var gotText, gotOutput string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "hello",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "response"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error {
				hookCalls = append(hookCalls, "BeforeStimulate")
				gotText = p.Input
				return nil
			},
			BeforeThink: func(ctx context.Context, p *Prompt) error {
				hookCalls = append(hookCalls, "BeforeThink")
				return nil
			},
			OnCycleEnd: func(ctx context.Context, output string) {
				hookCalls = append(hookCalls, "OnCycleEnd")
			},
			AfterStimulate: func(ctx context.Context, output string) {
				hookCalls = append(hookCalls, "AfterStimulate")
				gotOutput = output
			},
		},
	}
	collectEvents(context.Background(), lc)

	want := []string{"BeforeStimulate", "BeforeThink", "OnCycleEnd", "AfterStimulate"}
	if len(hookCalls) != len(want) {
		t.Fatalf("hook calls = %v, want %v", hookCalls, want)
	}
	for i, w := range want {
		if hookCalls[i] != w {
			t.Fatalf("hook[%d] = %q, want %q", i, hookCalls[i], w)
		}
	}
	if gotText != "hello" {
		t.Fatalf("BeforeStimulate text = %q, want %q", gotText, "hello")
	}
	if gotOutput != "response" {
		t.Fatalf("AfterStimulate output = %q, want %q", gotOutput, "response")
	}
}

// TestDecisionLoopStimulateHooksOnError verifies both boundary hooks fire
// exactly once when the Thinker fails.
func TestDecisionLoopStimulateHooksOnError(t *testing.T) {
	var beforeCalls, afterCalls int
	lc := &LoopContext{
		CellID: "c1",
		Input:  "fail",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return nil, errors.New("thinker failed")
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error {
				beforeCalls++
				return nil
			},
			AfterStimulate: func(ctx context.Context, output string) {
				afterCalls++
			},
		},
	}
	collectEvents(context.Background(), lc)

	if beforeCalls != 1 {
		t.Fatalf("BeforeStimulate calls = %d, want 1", beforeCalls)
	}
	if afterCalls != 1 {
		t.Fatalf("AfterStimulate calls = %d, want 1", afterCalls)
	}
}

// TestDecisionLoopStimulateHooksOnCanceledCtx verifies both boundary hooks
// still fire exactly once when the context is already canceled at entry
// (BeforeStimulate runs before the ctx check; AfterStimulate is deferred).
func TestDecisionLoopStimulateHooksOnCanceledCtx(t *testing.T) {
	var beforeCalls, afterCalls int
	thinkCalls := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "canceled",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinkCalls++
			return &Decision{Text: "text"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error {
				beforeCalls++
				return nil
			},
			AfterStimulate: func(ctx context.Context, output string) {
				afterCalls++
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := collectEvents(ctx, lc)

	if beforeCalls != 1 {
		t.Fatalf("BeforeStimulate calls = %d, want 1", beforeCalls)
	}
	if afterCalls != 1 {
		t.Fatalf("AfterStimulate calls = %d, want 1", afterCalls)
	}
	if thinkCalls != 0 {
		t.Fatalf("Think calls = %d, want 0 (cycle aborted before loop)", thinkCalls)
	}
	if len(events) < 2 || events[0].Kind != EventState || events[0].State != StateError {
		t.Fatalf("events = %+v, want first event State(StateError)", events)
	}
}

// TestDecisionLoopStimulateHooksOnAbort verifies AfterStimulate still runs
// exactly once when the consumer stops early (yield=false).
func TestDecisionLoopStimulateHooksOnAbort(t *testing.T) {
	var afterCalls int
	lc := &LoopContext{
		CellID: "c1",
		Input:  "abort",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "text"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			AfterStimulate: func(ctx context.Context, output string) {
				afterCalls++
			},
		},
	}
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		return false // stop immediately
	})
	if afterCalls != 1 {
		t.Fatalf("AfterStimulate calls = %d, want 1", afterCalls)
	}
}

// TestDecisionLoopBeforeStimulateError verifies a BeforeStimulate error
// terminates the Stimulate before any Think call.
func TestDecisionLoopBeforeStimulateError(t *testing.T) {
	thinkCalls := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "blocked",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinkCalls++
			return &Decision{Text: "unreachable"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error {
				return errors.New("stimulate blocked")
			},
		},
	}
	events := collectEvents(context.Background(), lc)

	if thinkCalls != 0 {
		t.Fatalf("Think calls = %d, want 0", thinkCalls)
	}
	// Expect: State(error), EventError
	if len(events) != 2 {
		t.Fatalf("events count = %d, want 2; events: %+v", len(events), events)
	}
	if events[0].Kind != EventState || events[0].State != StateError {
		t.Fatalf("events[0] = %+v, want EventState(StateError)", events[0])
	}
	if events[1].Kind != EventError {
		t.Fatalf("events[1] = %+v, want EventError", events[1])
	}
	if !strings.Contains(events[1].Err.Error(), "stimulate blocked") {
		t.Fatalf("error = %v, want containing 'stimulate blocked'", events[1].Err)
	}
}

// TestDecisionLoopBeforeStimulateMutatesPrompt verifies modifications made in
// BeforeStimulate apply to every round of the Stimulate (content fields are
// written back to LoopContext).
func TestDecisionLoopBeforeStimulateMutatesPrompt(t *testing.T) {
	var gotSystem, gotInput, gotPlan string
	var gotTools []ToolSpec
	var firstCtx, secondCtx []string
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "original-input",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			gotSystem = p.System
			gotInput = p.Input
			gotPlan = p.Plan
			gotTools = p.Tools
			if calls == 1 {
				firstCtx = append([]string{}, p.Context...)
				return &Decision{
					Text:      "act",
					ToolCalls: []ToolCall{{ID: "t1", Name: "fn"}},
				}, nil
			}
			secondCtx = append([]string{}, p.Context...)
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error {
				p.System = "injected-system"
				p.Tools = []ToolSpec{{Name: "injected-tool"}}
				p.Context = append(p.Context, "injected-history")
				p.Input = "rewritten-input"
				p.Plan = "injected-plan"
				return nil
			},
		},
	}
	collectEvents(context.Background(), lc)

	if gotSystem != "injected-system" {
		t.Fatalf("System = %q, want %q", gotSystem, "injected-system")
	}
	if gotInput != "rewritten-input" {
		t.Fatalf("Input = %q, want %q", gotInput, "rewritten-input")
	}
	if gotPlan != "injected-plan" {
		t.Fatalf("Plan = %q, want %q", gotPlan, "injected-plan")
	}
	if len(gotTools) != 1 || gotTools[0].Name != "injected-tool" {
		t.Fatalf("Tools = %+v, want [injected-tool]", gotTools)
	}
	if len(firstCtx) != 1 || firstCtx[0] != "injected-history" {
		t.Fatalf("first round context = %v, want [injected-history]", firstCtx)
	}
	// Tool feedback is appended after round 1; the injected entry must persist.
	found := false
	for _, c := range secondCtx {
		if c == "injected-history" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("second round context = %v, want it to contain 'injected-history'", secondCtx)
	}
}

// TestDecisionLoopAfterActReceivesErr verifies AfterAct receives the tool
// execution error.
func TestDecisionLoopAfterActReceivesErr(t *testing.T) {
	var gotErr error
	lc := &LoopContext{
		CellID: "c1",
		Input:  "fail",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{
				Text:      "do-it",
				ToolCalls: []ToolCall{{ID: "t1", Name: "boom"}},
			}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return nil, errors.New("executor exploded")
		}},
		Hooks: &Hooks{
			AfterAct: func(ctx context.Context, a *Action, e *Effect, err error) {
				gotErr = err
			},
		},
	}
	collectEvents(context.Background(), lc)

	if gotErr == nil {
		t.Fatal("AfterAct err = nil, want non-nil executor error")
	}
	if !strings.Contains(gotErr.Error(), "executor exploded") {
		t.Fatalf("AfterAct err = %v, want containing 'executor exploded'", gotErr)
	}
}
