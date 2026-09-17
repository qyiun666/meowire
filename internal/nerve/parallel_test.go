// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// parallel_test.go — ParallelActs white-box tests (v1.3.3 acceptance set).
package nerve

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingEffector reports each in-flight Act on started (buffered) and
// blocks until release is closed (or ctx cancels) — peak concurrency of the
// parallel execution phase becomes observable from outside the loop.
type blockingEffector struct {
	started chan string
	release chan struct{}
}

func (b blockingEffector) Act(ctx context.Context, a Action) (*Effect, error) {
	b.started <- a.Call.Name
	select {
	case <-b.release:
		return &Effect{Result: "ok:" + a.Call.Name}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestParallelActsPeakConcurrency: acceptance 2 — with ParallelActs on and
// one round producing 2 calls, both Acts are in flight simultaneously
// (blocking Effector + release channel).
func TestParallelActsPeakConcurrency(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	calls := 0
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "parallel",
		MaxRounds:    2,
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{
					{ID: "t1", Name: "tool1"}, {ID: "t2", Name: "tool2"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: blockingEffector{started: started, release: release},
	}

	done := make(chan []Event, 1)
	fillRequired(lc)
	go func() {
		var events []Event
		(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
			events = append(events, e)
			return true
		})
		done <- events
	}()

	// Both calls must be in flight before either can complete.
	inFlight := map[string]bool{}
	for range 2 {
		select {
		case name := <-started:
			inFlight[name] = true
		case <-time.After(2 * time.Second):
			close(release)
			<-done
			t.Fatal("timed out waiting for both Acts to start (calls are not parallel)")
		}
	}
	if !inFlight["tool1"] || !inFlight["tool2"] {
		t.Fatalf("in flight = %v, want tool1 and tool2 simultaneously", inFlight)
	}
	close(release)

	events := <-done
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone; kinds: %v", last, kindsOf(events))
	}
}

// TestParallelActsCeilingBoundsInFlight: MaxParallelActs narrows the execution
// window without changing what runs — four admitted calls under a ceiling of
// two means the third waits for a slot, and every call still executes.
func TestParallelActsCeilingBoundsInFlight(t *testing.T) {
	const ceiling = 2
	started := make(chan string, 4)
	release := make(chan struct{})
	calls := 0
	lc := &LoopContext{
		CellID:          "c1",
		Input:           "throttle",
		MaxRounds:       2,
		ParallelActs:    true,
		MaxParallelActs: ceiling,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{
					{ID: "t1", Name: "tool1"}, {ID: "t2", Name: "tool2"},
					{ID: "t3", Name: "tool3"}, {ID: "t4", Name: "tool4"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: blockingEffector{started: started, release: release},
	}

	done := make(chan []Event, 1)
	fillRequired(lc)
	go func() {
		var events []Event
		(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
			events = append(events, e)
			return true
		})
		done <- events
	}()

	inFlight := map[string]bool{}
	for range ceiling {
		select {
		case name := <-started:
			inFlight[name] = true
		case <-time.After(2 * time.Second):
			close(release)
			<-done
			t.Fatalf("timed out filling the window of %d (started: %v)", ceiling, inFlight)
		}
	}
	// The ceiling holds: a third call does not enter while two are blocked.
	select {
	case extra := <-started:
		close(release)
		<-done
		t.Fatalf("%s started beside the ceiling of %d (in flight: %v)", extra, ceiling, inFlight)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	for len(inFlight) < 4 {
		select {
		case name := <-started:
			inFlight[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 4 calls ever started", len(inFlight))
		}
	}

	events := <-done
	if last := events[len(events)-1]; last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone; kinds: %v", last, kindsOf(events))
	}
}

// TestParallelActsFeedbackOrderIsCallOrder: acceptance 3 — the slow call is
// announced first and finishes last, yet ToolResults and EventToolResult
// follow call order, never completion order.
func TestParallelActsFeedbackOrderIsCallOrder(t *testing.T) {
	var capturedResults []ToolResult
	calls := 0
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "order",
		MaxRounds:    2,
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 2 {
				capturedResults = append([]ToolResult(nil), p.ToolResults...)
			}
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{
					{ID: "t1", Name: "slow"}, {ID: "t2", Name: "fast"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			if a.Call.Name == "slow" {
				time.Sleep(100 * time.Millisecond) // finishes after "fast"
			}
			return &Effect{Result: a.Call.Name}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	// EventToolResult order = call order (slow first, fast second).
	var resultNames []string
	for _, e := range events {
		if e.Kind == EventToolResult && e.Effect != nil && e.Effect.Err == "" {
			resultNames = append(resultNames, e.Effect.Result)
		}
	}
	if len(resultNames) != 2 || resultNames[0] != "slow" || resultNames[1] != "fast" {
		t.Fatalf("EventToolResult order = %v, want [slow fast] (call order, not completion order)", resultNames)
	}
	// The digesting Think sees the same call-ordered structured track.
	if len(capturedResults) != 2 || capturedResults[0].ID != "t1" || capturedResults[1].ID != "t2" {
		t.Fatalf("ToolResults = %+v, want [t1 t2] in call order", capturedResults)
	}
}

// denyOddSandbox denies calls whose name starts with "odd".
type denyOddSandbox struct{}

func (denyOddSandbox) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	if len(a.Call.Name) >= 3 && a.Call.Name[:3] == "odd" {
		return VerdictDeny, "odd calls are forbidden", nil
	}
	return VerdictAllow, "", nil
}

func (denyOddSandbox) Bounds() string { return "deny-odd" }
func (denyOddSandbox) Emit(context.Context, Utterance) (Verdict, string, error) {
	return VerdictAllow, "", nil
}

// TestParallelActsBatchDenialIsolation: acceptance 4 — one denied call in
// the batch produces its denial feedback and is skipped; the sibling runs
// normally and the loop completes.
func TestParallelActsBatchDenialIsolation(t *testing.T) {
	var actCalls atomic.Int32
	var capturedCtx []string
	calls := 0
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "mixed",
		MaxRounds:    2,
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 2 {
				capturedCtx = append([]string(nil), p.Context...)
			}
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{
					{ID: "t1", Name: "oddOne"}, {ID: "t2", Name: "evenOne"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls.Add(1)
			return &Effect{Result: a.Call.Name}, nil
		}},
		Sandbox: denyOddSandbox{},
	}
	events := collectEvents(context.Background(), lc)

	// Both calls audited on the tool side: one denied verdict, one allowed verdict.
	var denied, allowed int
	for _, v := range toolVerdicts(events) {
		switch v.Ruling {
		case VerdictAllow:
			allowed++
		case VerdictDeny:
			if v.Reason == "odd calls are forbidden" {
				denied++
			}
		}
	}
	if denied != 1 || allowed != 1 {
		t.Fatalf("verdicts denied/allowed = %d/%d, want 1/1", denied, allowed)
	}
	// Only the admitted call executed.
	if n := actCalls.Load(); n != 1 {
		t.Fatalf("Act calls = %d, want 1 (denied call must not execute)", n)
	}
	// Denial went to the Context text track, sibling result to ToolResults.
	foundDenied := false
	for _, c := range capturedCtx {
		if c == "[sandbox-denied: odd calls are forbidden]" {
			foundDenied = true
		}
	}
	if !foundDenied {
		t.Fatalf("context = %v, want '[sandbox-denied: odd calls are forbidden]'", capturedCtx)
	}
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone (a denial must not kill the batch)", last)
	}
}

// TestParallelActsWaitInputSuspendsNoReplay: acceptance 5 — a WaitInput
// inside the batch suspends with RemainingCalls() empty (the batch fully
// executed, nothing may be replayed); sibling results — before AND after the
// suspending call — are preserved in the snapshot's ToolResults, and Resume
// injects the response without re-executing any call.
func TestParallelActsWaitInputSuspendsNoReplay(t *testing.T) {
	var actCounts sync.Map // call id -> *atomic.Int32
	countAct := func(id string) {
		v, _ := actCounts.LoadOrStore(id, &atomic.Int32{})
		v.(*atomic.Int32).Add(1)
	}
	var afterActIDs []string
	calls := 0
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "ask",
		MaxRounds:    3,
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{
					{ID: "t1", Name: "before"}, {ID: "t2", Name: "askUser"}, {ID: "t3", Name: "after"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			countAct(a.Call.ID)
			if a.Call.Name == "askUser" {
				return &Effect{WaitInput: "may I proceed?"}, nil
			}
			return &Effect{Result: a.Call.Name}, nil
		}},
		Hooks: &Hooks{
			AfterAct: func(ctx context.Context, a *Action, e *Effect, err error) {
				afterActIDs = append(afterActIDs, a.Call.ID)
			},
		},
	}

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

	if wait == nil {
		t.Fatalf("no EventWaitInput yielded; kinds: %v", kindsOf(events))
	}
	if wait.Question != "may I proceed?" {
		t.Fatalf("question = %q, want %q", wait.Question, "may I proceed?")
	}
	// StateWaiting precedes EventWaitInput.
	prev := events[len(events)-2]
	if prev.Kind != EventState || prev.State != StateWaiting {
		t.Fatalf("event before WaitInput = %+v, want EventState(StateWaiting)", prev)
	}
	// The batch fully executed — nothing may be replayed.
	if rem := wait.Session.RemainingCalls(); len(rem) != 0 {
		t.Fatalf("RemainingCalls() = %+v, want empty (replaying would duplicate side effects)", rem)
	}
	// Sibling feedback (before AND after) preserved; the suspending call is
	// pending (its result arrives via Resume).
	var ids []string
	for _, tr := range wait.Session.toolResults {
		ids = append(ids, tr.ID)
	}
	if len(ids) != 2 || ids[0] != "t1" || ids[1] != "t3" {
		t.Fatalf("session ToolResults ids = %v, want [t1 t3] in call order", ids)
	}
	// The suspending call got no AfterAct; its siblings did, in call order.
	if len(afterActIDs) != 2 || afterActIDs[0] != "t1" || afterActIDs[1] != "t3" {
		t.Fatalf("AfterAct ids = %v, want [t1 t3]", afterActIDs)
	}

	// Resume injects the response as the pending tool's result — no replay.
	resumeEvents := collectResume(context.Background(), lc, wait.Session, "yes")
	last := resumeEvents[len(resumeEvents)-1]
	if last.Kind != EventDone {
		t.Fatalf("last resume event = %+v, want EventDone; kinds: %v", last, kindsOf(resumeEvents))
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		v, _ := actCounts.Load(id)
		if n := v.(*atomic.Int32).Load(); n != 1 {
			t.Fatalf("Act(%s) calls = %d, want exactly 1 (no replay after WaitInput)", id, n)
		}
	}
}

// TestParallelActsPauseSnapshotsWholeBatch: acceptance 6 — a pause at the
// batch gap point snapshots the whole batch unexecuted (RemainingCalls() =
// the full batch); Resume replays it (again in parallel) to completion.
func TestParallelActsPauseSnapshotsWholeBatch(t *testing.T) {
	var paused atomic.Bool
	var executed sync.Map // call name -> struct{}
	calls := 0
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "pause-batch",
		MaxRounds:    3,
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{
					{ID: "t1", Name: "tool1"}, {ID: "t2", Name: "tool2"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			executed.Store(a.Call.Name, struct{}{})
			return &Effect{Result: a.Call.Name}, nil
		}},
		Pause: &PauseGate{
			IsPaused: func() bool { return paused.Load() },
		},
	}

	var wait *WaitInput
	fillRequired(lc)
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		if e.Kind == EventToolCall {
			paused.Store(true) // pause once the batch is announced
		}
		if e.Kind == EventPaused {
			wait = e.Wait
		}
		return true
	})

	if wait == nil {
		t.Fatal("no EventPaused yielded")
	}
	count := 0
	executed.Range(func(k, v any) bool {
		count++
		return true
	})
	if count != 0 {
		t.Fatalf("executed count = %d, want 0 (the batch must pause unexecuted)", count)
	}
	rem := wait.Session.RemainingCalls()
	if len(rem) != 2 || rem[0].Name != "tool1" || rem[1].Name != "tool2" {
		t.Fatalf("RemainingCalls() = %+v, want the whole batch [tool1 tool2]", rem)
	}

	// Resume replays the whole batch, then the digesting Think completes.
	paused.Store(false)
	resumeEvents := collectResume(context.Background(), lc, wait.Session, "")
	for _, name := range []string{"tool1", "tool2"} {
		if _, ok := executed.Load(name); !ok {
			t.Fatalf("%s did not execute after resume", name)
		}
	}
	last := resumeEvents[len(resumeEvents)-1]
	if last.Kind != EventDone {
		t.Fatalf("last resume event = %+v, want EventDone; kinds: %v", last, kindsOf(resumeEvents))
	}
}

// TestParallelActsSingleCallKeepsSerialPath: a single call with
// ParallelActs=true keeps the serial path — zero behavior difference
// (ToolCall → Sandbox → ToolResult per call, no batch phase split).
func TestParallelActsSingleCallKeepsSerialPath(t *testing.T) {
	calls := 0
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "single",
		MaxRounds:    2,
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{{ID: "t1", Name: "solo"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	want := []EventKind{
		EventState, EventSandbox, EventText, EventState, EventToolCall, EventSandbox, EventToolResult,
		EventState, EventSandbox, EventText, EventState, EventDone,
	}
	if !slicesEqual(kindsOf(events), want) {
		t.Fatalf("event kinds = %v, want serial shape %v", kindsOf(events), want)
	}
}

// TestParallelActsBeforeActMutationApplies: regression (v1.3.5 review) — a
// BeforeAct hook that rewrites the Action must reach the Effector on the
// parallel path exactly as on the serial path; the gated Action, not the
// raw ToolCall, is what executes.
func TestParallelActsBeforeActMutationApplies(t *testing.T) {
	var mu sync.Mutex
	seenArgs := map[string]string{}
	calls := 0
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "mutate",
		MaxRounds:    2,
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			calls++
			if calls == 1 {
				return &Decision{Text: "run", ToolCalls: []ToolCall{
					{ID: "t1", Name: "tool1", Args: "raw"}, {ID: "t2", Name: "tool2", Args: "raw"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			mu.Lock()
			seenArgs[a.Call.ID] = a.Call.Args
			mu.Unlock()
			return &Effect{Result: "ok"}, nil
		}},
		Hooks: &Hooks{
			BeforeAct: func(ctx context.Context, a *Action) error {
				a.Call.Args = "rewritten"
				return nil
			},
		},
	}
	events := collectEvents(context.Background(), lc)
	last := events[len(events)-1]
	if last.Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone; kinds: %v", last, kindsOf(events))
	}
	for _, id := range []string{"t1", "t2"} {
		if got := seenArgs[id]; got != "rewritten" {
			t.Fatalf("Act(%s) saw args=%q, want \"rewritten\" (BeforeAct mutation must apply on the parallel path)", id, got)
		}
	}
}

// TestParallelActsAbortBeforeExecutionSkipsBatch: stopping consumption at
// the first EventToolCall prevents the entire batch from executing.
func TestParallelActsAbortBeforeExecutionSkipsBatch(t *testing.T) {
	var actCalls atomic.Int32
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "abort",
		ParallelActs: true,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			return &Decision{Text: "run", ToolCalls: []ToolCall{
				{ID: "t1", Name: "tool1"}, {ID: "t2", Name: "tool2"},
			}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			actCalls.Add(1)
			return &Effect{Result: "ok"}, nil
		}},
	}
	fillRequired(lc)
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		return e.Kind != EventToolCall // stop as soon as the batch is announced
	})
	if n := actCalls.Load(); n != 0 {
		t.Fatalf("Act calls = %d, want 0 (batch must not execute after abort)", n)
	}
}
