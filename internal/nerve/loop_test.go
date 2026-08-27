// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// loop_test.go — DecisionLoop white-box tests.
package nerve

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// testHooks returns no-op hooks with all eight required callbacks set.
func testHooks() *Hooks {
	return &Hooks{
		BeforeStimulate: func(ctx context.Context, p *Prompt) error { return nil },
		AfterStimulate:  func(ctx context.Context, output string) {},
		BeforeThink:     func(ctx context.Context, p *Prompt) error { return nil },
		AfterThink:      func(ctx context.Context, d *Decision) error { return nil },
		BeforeAct:       func(ctx context.Context, a *Action) error { return nil },
		AfterAct:        func(ctx context.Context, a *Action, e *Effect, err error) {},
		OnError:         func(ctx context.Context, err error) {},
		OnCycleEnd:      func(ctx context.Context, output string) {},
	}
}

// testSandbox allows every action.
type testSandbox struct{}

func (testSandbox) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	return VerdictAllow, "", nil
}

func (testSandbox) Bounds() string { return "test" }

// testBudget returns the context unchanged.
func testBudget() *ContextBudget {
	return &ContextBudget{MaxTokens: 100, Trimmer: func(c []string, _ int) []string { return c }}
}

// fillRequired fills the required ports (Hooks/Sandbox/Budget) with no-op
// defaults when a test does not target them. The loop requires all three
// (assembly enforces); tests that do not care get declared no-ops.
func fillRequired(lc *LoopContext) {
	if lc.Sandbox == nil {
		lc.Sandbox = testSandbox{}
	}
	if lc.Budget == nil {
		lc.Budget = testBudget()
	}
	if lc.Hooks == nil {
		lc.Hooks = testHooks()
	} else {
		def := testHooks()
		if lc.Hooks.BeforeStimulate == nil {
			lc.Hooks.BeforeStimulate = def.BeforeStimulate
		}
		if lc.Hooks.AfterStimulate == nil {
			lc.Hooks.AfterStimulate = def.AfterStimulate
		}
		if lc.Hooks.BeforeThink == nil {
			lc.Hooks.BeforeThink = def.BeforeThink
		}
		if lc.Hooks.AfterThink == nil {
			lc.Hooks.AfterThink = def.AfterThink
		}
		if lc.Hooks.BeforeAct == nil {
			lc.Hooks.BeforeAct = def.BeforeAct
		}
		if lc.Hooks.AfterAct == nil {
			lc.Hooks.AfterAct = def.AfterAct
		}
		if lc.Hooks.OnError == nil {
			lc.Hooks.OnError = def.OnError
		}
		if lc.Hooks.OnCycleEnd == nil {
			lc.Hooks.OnCycleEnd = def.OnCycleEnd
		}
	}
}

// collectEvents runs Cycle and returns all yielded events.
func collectEvents(ctx context.Context, lc *LoopContext) []Event {
	fillRequired(lc)
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
		EventState, EventText, EventState, EventToolCall, EventSandbox, EventToolResult,
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

// TestDecisionLoopToolErrorBackfill: Tool returns error → the failure text
// reaches the next Think via ToolResults.Err (structured track only).
func TestDecisionLoopToolErrorBackfill(t *testing.T) {
	var capturedResults []ToolResult
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "fix",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 2 {
				capturedResults = append([]ToolResult(nil), p.ToolResults...)
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

	// Verify that the second Think call received the error in the structured track.
	if len(capturedResults) != 1 || capturedResults[0].ID != "t1" || capturedResults[0].Name != "broken" {
		t.Fatalf("ToolResults = %+v, want one entry t1/broken", capturedResults)
	}
	if !strings.Contains(capturedResults[0].Err, "boom") || capturedResults[0].Result != "" {
		t.Fatalf("ToolResults[0] = %+v, want Err containing 'boom' and empty Result", capturedResults[0])
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

func (denySandbox) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	return VerdictDeny, "not allowed", nil
}

func (denySandbox) Bounds() string { return "deny-all" }

// auditSandbox denies "rm" with a policy reason and allows everything else.
type auditSandbox struct{}

func (auditSandbox) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	if a.Call.Name == "rm" {
		return VerdictDeny, "destructive", nil
	}
	return VerdictAllow, "", nil
}

func (auditSandbox) Bounds() string { return "audit-all" }

// TestDecisionLoopSandboxAuditAllow: the membrane yields an
// EventSandbox(allowed) verdict before the tool runs — the action-level
// audit record the Authority model requires (the membrane is required, so
// every tool execution is audited).
func TestDecisionLoopSandboxAuditAllow(t *testing.T) {
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "compute",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "go", ToolCalls: []ToolCall{{ID: "t1", Name: "calc"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "2"}, nil
		}},
		Sandbox: auditSandbox{},
	}
	events := collectEvents(context.Background(), lc)

	var verdicts []*SandboxVerdict
	for _, e := range events {
		if e.Kind == EventSandbox {
			verdicts = append(verdicts, e.Verdict)
		}
	}
	if len(verdicts) != 1 {
		t.Fatalf("sandbox verdicts = %d, want 1", len(verdicts))
	}
	v := verdicts[0]
	if v.Ruling != VerdictAllow || v.Reason != "" || v.Err != nil {
		t.Fatalf("verdict = %+v, want allowed with no reason/error", v)
	}
	if v.CellID != "c1" || v.Call.Name != "calc" {
		t.Fatalf("verdict = %+v, want CellID c1 and Call calc", v)
	}
}

// TestDecisionLoopSandboxAuditDeny: a denial yields EventSandbox(denied)
// with the policy reason, followed by the existing feedback path.
func TestDecisionLoopSandboxAuditDeny(t *testing.T) {
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "dangerous",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "do-it", ToolCalls: []ToolCall{{ID: "t1", Name: "rm"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "deleted"}, nil
		}},
		Sandbox: auditSandbox{},
	}
	events := collectEvents(context.Background(), lc)

	// Sequence: ... ToolCall, Sandbox(denied), ToolResult, ...
	for i := 0; i < len(events)-1; i++ {
		if events[i].Kind == EventToolCall {
			if events[i+1].Kind != EventSandbox {
				t.Fatalf("event after ToolCall = %+v, want EventSandbox(denied)", events[i+1])
			}
			v := events[i+1].Verdict
			if v == nil {
				t.Fatal("EventSandbox without a verdict")
			}
			if v.Ruling != VerdictDeny || v.Reason != "destructive" || v.Call.Name != "rm" {
				t.Fatalf("verdict = %+v, want denied with reason 'destructive' for rm", v)
			}
			return
		}
	}
	t.Fatal("no EventToolCall found in events")
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
	fillRequired(lc)
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
	fillRequired(lc)
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
	fillRequired(lc)
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

// TestTruncateTextUTF8Truncation verifies truncateText handles multi-byte UTF-8 correctly.
func TestTruncateTextUTF8Truncation(t *testing.T) {
	// Multi-byte string: "你好世界" is 12 bytes (3 bytes per char)
	fb := truncateText("你好世界", 5)
	if fb == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(fb, "[truncated,") {
		t.Fatalf("expected truncation marker, got: %q", fb)
	}

	// Truncate at 1 byte (falls inside first character)
	fb2 := truncateText("你好世界", 1)
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
	fillRequired(lc)
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
	// Tool results no longer enter Context (structured track only); the
	// injected entry must persist across rounds.
	found := slices.Contains(secondCtx, "injected-history")
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

// TestDecisionLoopPauseSuspends verifies a pause requested at a gap point
// yields EventState(StatePaused) + EventPaused with a Session snapshot and
// ends the iterator normally (no Done, no Error) — the unified
// suspension-resume path (v1.3.2): the host resumes via Resume(sess, "").
func TestDecisionLoopPauseSuspends(t *testing.T) {
	var paused atomic.Bool
	var executed []string
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "pause-test",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "t", ToolCalls: []ToolCall{{ID: "t1", Name: "tool"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			executed = append(executed, a.Call.Name)
			return &Effect{Result: "ok"}, nil
		}},
		Pause: &PauseGate{
			IsPaused: func() bool { return paused.Load() },
		},
	}

	var events []Event
	var wait *WaitInput
	fillRequired(lc)
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		events = append(events, e)
		if e.Kind == EventToolCall {
			paused.Store(true) // request pause right after the tool call is announced
		}
		if e.Kind == EventPaused {
			wait = e.Wait
		}
		return true
	})

	// Expect: State(thinking), Text, State(acting), ToolCall, State(paused), EventPaused
	want := []EventKind{EventState, EventText, EventState, EventToolCall, EventState, EventPaused}
	if !slicesEqual(kindsOf(events), want) {
		t.Fatalf("event kinds = %v, want %v", kindsOf(events), want)
	}
	if events[4].Kind != EventState || events[4].State != StatePaused {
		t.Fatalf("events[4] = %+v, want EventState(StatePaused)", events[4])
	}
	if wait == nil {
		t.Fatal("no EventPaused yielded")
	}
	sess := wait.Session
	if !sess.valid() {
		t.Fatal("pause session invalid")
	}
	// The pause point sits before the only tool, which has not run yet — the
	// snapshot must keep it in remaining so Resume runs it first.
	if len(sess.remaining) != 1 || sess.remaining[0].Name != "tool" {
		t.Fatalf("pause session remaining = %+v, want [tool] (the tool has not run yet)", sess.remaining)
	}
	if sess.pending.ID != "" {
		t.Fatalf("pause session pending = %+v, want zero value (pause has no pending tool)", sess.pending)
	}

	// The paused run continues via Resume(sess, ""): the tool runs first,
	// then the loop re-enters Think and completes normally.
	paused.Store(false)
	resumeEvents := collectResume(context.Background(), lc, sess, "")
	if len(executed) != 1 || executed[0] != "tool" {
		t.Fatalf("executed after resume = %v, want [tool]", executed)
	}
	last := resumeEvents[len(resumeEvents)-1]
	if last.Kind != EventDone {
		t.Fatalf("last resume event = %+v, want EventDone; kinds: %v", last, kindsOf(resumeEvents))
	}
	if last.Output != "tdone" {
		t.Fatalf("done output = %q, want %q (accumulated output preserved across the pause)", last.Output, "tdone")
	}
}

// TestDecisionLoopPauseMidList verifies a pause between tool calls snapshots
// the calls from the pause point on (the current tool has not run yet) into
// the Session; Resume runs them first, then re-enters the Think phase.
func TestDecisionLoopPauseMidList(t *testing.T) {
	var executed []string
	var paused atomic.Bool
	calls := 0
	lc := &LoopContext{
		CellID:    "c1",
		Input:     "work",
		MaxRounds: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "multi", ToolCalls: []ToolCall{
					{ID: "t1", Name: "tool1"}, {ID: "t2", Name: "tool2"}, {ID: "t3", Name: "tool3"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			executed = append(executed, a.Call.Name)
			return &Effect{Result: "ok"}, nil
		}},
		Pause: &PauseGate{
			IsPaused: func() bool { return paused.Load() },
		},
	}

	var wait *WaitInput
	fillRequired(lc)
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		if e.Kind == EventToolCall && e.ToolCall != nil && e.ToolCall.Name == "tool2" {
			paused.Store(true) // pause between tool1 and tool2 (tool2 has not run yet)
		}
		if e.Kind == EventPaused {
			wait = e.Wait
		}
		return true
	})
	if wait == nil {
		t.Fatal("no EventPaused yielded")
	}
	if len(executed) != 1 || executed[0] != "tool1" {
		t.Fatalf("executed = %v, want [tool1] (tool2/tool3 must wait for Resume)", executed)
	}
	rem := wait.Session.remaining
	if len(rem) != 2 || rem[0].Name != "tool2" || rem[1].Name != "tool3" {
		t.Fatalf("session remaining = %+v, want [tool2 tool3] (tool2 has not run yet)", rem)
	}

	// Resume runs the remaining tools, then the digesting Think completes.
	paused.Store(false)
	events := collectResume(context.Background(), lc, wait.Session, "")
	if len(executed) != 3 || executed[0] != "tool1" || executed[1] != "tool2" || executed[2] != "tool3" {
		t.Fatalf("executed after resume = %v, want [tool1 tool2 tool3]", executed)
	}
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone; kinds: %v", last, kindsOf(events))
	}
}

// TestDecisionLoopPauseConsumerAbort verifies stopping consumption at the
// StatePaused event aborts before the EventPaused snapshot is delivered —
// no deadlock, no resume needed.
func TestDecisionLoopPauseConsumerAbort(t *testing.T) {
	var paused atomic.Bool
	paused.Store(true)
	lc := &LoopContext{
		CellID: "c1",
		Input:  "pause-abort",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "unreachable"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Pause: &PauseGate{
			IsPaused: func() bool { return paused.Load() },
		},
	}

	var gotPaused, gotEventPaused bool
	fillRequired(lc)
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		if e.Kind == EventState && e.State == StatePaused {
			gotPaused = true
			return false // consumer aborts during the pause
		}
		if e.Kind == EventPaused {
			gotEventPaused = true
		}
		return true
	})
	if !gotPaused {
		t.Fatal("expected EventState(StatePaused) before consumer abort")
	}
	if gotEventPaused {
		t.Fatal("EventPaused must not be yielded after the consumer aborts")
	}
}

// TestActWithRetrySucceeds verifies a transient effector error is retried
// up to ToolMaxRetries and the loop completes without an error event.
func TestActWithRetrySucceeds(t *testing.T) {
	actCalls := 0
	calls := 0
	lc := &LoopContext{
		CellID:         "c1",
		Input:          "retry-tool",
		MaxRounds:      2,
		ToolMaxRetries: 2,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{
					Text:      "do-it",
					ToolCalls: []ToolCall{{ID: "t1", Name: "flaky"}},
				}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			if actCalls < 3 {
				return nil, errors.New("transient")
			}
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	if actCalls != 3 {
		t.Fatalf("Act calls = %d, want 3 (2 retries after 1 failure)", actCalls)
	}
	for _, e := range events {
		if e.Kind == EventError {
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone", last)
	}
}

// TestActWithRetryExhausted verifies retries are exhausted and the final
// error still flows through ToolResults.Err (loop continues).
func TestActWithRetryExhausted(t *testing.T) {
	actCalls := 0
	calls := 0
	lc := &LoopContext{
		CellID:         "c1",
		Input:          "retry-exhaust",
		MaxRounds:      2,
		ToolMaxRetries: 2,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{
					Text:      "do-it",
					ToolCalls: []ToolCall{{ID: "t1", Name: "broken"}},
				}, nil
			}
			return &Decision{Text: "recovered"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			return nil, errors.New("always-fails")
		}},
	}
	events := collectEvents(context.Background(), lc)

	if actCalls != 3 {
		t.Fatalf("Act calls = %d, want 3 (initial + 2 retries)", actCalls)
	}
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone (error is feedback, not failure)", last)
	}
}

// TestActEffectErrNoRetry verifies Effect.Err (a business error) is never
// retried — retrying could duplicate side effects.
func TestActEffectErrNoRetry(t *testing.T) {
	actCalls := 0
	calls := 0
	lc := &LoopContext{
		CellID:         "c1",
		Input:          "business-error",
		MaxRounds:      2,
		ToolMaxRetries: 3,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{
					Text:      "do-it",
					ToolCalls: []ToolCall{{ID: "t1", Name: "business"}},
				}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			return &Effect{Err: "rejected by business logic"}, nil
		}},
	}
	collectEvents(context.Background(), lc)

	if actCalls != 1 {
		t.Fatalf("Act calls = %d, want 1 (Effect.Err must not be retried)", actCalls)
	}
}

// TestToolTimeoutNoRetry verifies a timeout-derived error is not retried and
// flows through ToolResults.Err as a structured error entry.
func TestToolTimeoutNoRetry(t *testing.T) {
	actCalls := 0
	calls := 0
	var capturedResults []ToolResult
	lc := &LoopContext{
		CellID:         "c1",
		Input:          "timeout",
		MaxRounds:      2,
		ToolTimeout:    20 * time.Millisecond,
		ToolMaxRetries: 2,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 2 {
				capturedResults = append([]ToolResult(nil), p.ToolResults...)
			}
			if calls == 1 {
				return &Decision{
					Text:      "do-it",
					ToolCalls: []ToolCall{{ID: "t1", Name: "slow"}},
				}, nil
			}
			return &Decision{Text: "recovered"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			<-ctx.Done() // block until the per-tool timeout fires
			return nil, ctx.Err()
		}},
	}
	events := collectEvents(context.Background(), lc)

	if actCalls != 1 {
		t.Fatalf("Act calls = %d, want 1 (timeout error must not be retried)", actCalls)
	}
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone", last)
	}
	if len(capturedResults) != 1 || capturedResults[0].ID != "t1" {
		t.Fatalf("ToolResults = %+v, want one entry t1", capturedResults)
	}
	if !strings.Contains(capturedResults[0].Err, "deadline exceeded") && !strings.Contains(capturedResults[0].Err, "context canceled") {
		t.Fatalf("ToolResults[0].Err = %q, want timeout/cancel error", capturedResults[0].Err)
	}
}

// TestSandboxBoundsReachesPrompt verifies Sandbox.Bounds is snapshotted once
// per Stimulate and surfaced via Prompt.Bounds.
func TestSandboxBoundsReachesPrompt(t *testing.T) {
	var gotBounds string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "bounds",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			gotBounds = p.Bounds
			return &Decision{Text: "ok"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Sandbox: &boundsSandbox{},
	}
	collectEvents(context.Background(), lc)

	if gotBounds != "only /workspace" {
		t.Fatalf("Prompt.Bounds = %q, want %q", gotBounds, "only /workspace")
	}
}

// TestDecisionLoopBeforeStimulateSeesBounds verifies the BeforeStimulate
// prototype carries the Sandbox snapshot (read-only: hook overrides are not
// written back — the Thinker still receives the snapshot value).
func TestDecisionLoopBeforeStimulateSeesBounds(t *testing.T) {
	var hookBounds, thinkBounds string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "bounds",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinkBounds = p.Bounds
			return &Decision{Text: "ok"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
		Sandbox: &boundsSandbox{},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error {
				hookBounds = p.Bounds
				p.Bounds = "hooked" // must not leak into the loop
				return nil
			},
		},
	}
	collectEvents(context.Background(), lc)

	if hookBounds != "only /workspace" {
		t.Fatalf("hook Prompt.Bounds = %q, want %q", hookBounds, "only /workspace")
	}
	if thinkBounds != "only /workspace" {
		t.Fatalf("thinker Prompt.Bounds = %q, want snapshot %q (hook override must not write back)", thinkBounds, "only /workspace")
	}
}

// boundsSandbox permits everything and declares a fixed boundary.
type boundsSandbox struct{}

func (boundsSandbox) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	return VerdictAllow, "", nil
}

func (boundsSandbox) Bounds() string { return "only /workspace" }

// TestActWithRetryAndTimeoutTransientError verifies a transient effector error
// is still retried when ToolTimeout is configured but does not fire
// (regression: actCtx.Err() must be captured before cancel(), otherwise the
// retry path is skipped whenever a timeout is configured).
func TestActWithRetryAndTimeoutTransientError(t *testing.T) {
	actCalls := 0
	calls := 0
	lc := &LoopContext{
		CellID:         "c1",
		Input:          "retry-with-timeout",
		MaxRounds:      2,
		ToolTimeout:    5 * time.Second, // large: never fires
		ToolMaxRetries: 2,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{
					Text:      "do-it",
					ToolCalls: []ToolCall{{ID: "t1", Name: "flaky"}},
				}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			if actCalls < 3 {
				return nil, errors.New("transient")
			}
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	if actCalls != 3 {
		t.Fatalf("Act calls = %d, want 3 (2 retries after 1 failure)", actCalls)
	}
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone", last)
	}
}

// TestActNegativeRetriesExecutesOnce verifies a negative ToolMaxRetries still
// executes the tool once (contract: <=0 disables retry, not execution).
func TestActNegativeRetriesExecutesOnce(t *testing.T) {
	actCalls := 0
	calls := 0
	lc := &LoopContext{
		CellID:         "c1",
		Input:          "negative-retries",
		MaxRounds:      2,
		ToolMaxRetries: -1,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{
					Text:      "do-it",
					ToolCalls: []ToolCall{{ID: "t1", Name: "tool"}},
				}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls++
			return nil, errors.New("boom")
		}},
	}
	collectEvents(context.Background(), lc)

	if actCalls != 1 {
		t.Fatalf("Act calls = %d, want 1 (negative retries = execute once, no retry)", actCalls)
	}
}
