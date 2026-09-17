// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// sandbox_ask_test.go — Sandbox tri-state ruling (v1.3.x): VerdictAsk
// suspends the loop for external confirmation, resolved via the shared
// Resume channel (same channel as ask_user).
package nerve

import (
	"context"
	"slices"
	"testing"
)

// askSandbox asks on the call named "dangerous", allows everything else.
type askSandbox struct{}

func (askSandbox) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	if a.Call.Name == "dangerous" {
		return VerdictAsk, "confirm dangerous?", nil
	}
	return VerdictAllow, "", nil
}

func (askSandbox) Bounds() string { return "test-ask" }
func (askSandbox) Emit(context.Context, Utterance) (Verdict, string, error) {
	return VerdictAllow, "", nil
}

// runAskCycle runs one Stimulate whose first Think declares
// [alpha, dangerous, gamma] (alpha/gamma serial variants drop extras) and
// returns the events, the think counter, the execution log, and the
// OnCycleEnd counter. The second Think digests with plain text.
type askFixture struct {
	events    []Event
	thinks    int
	actLog    []string // executed call names, in order
	cycles    int      // OnCycleEnd invocations
	before    []string // BeforeAct action names
	suspended bool
	session   Session
}

func runAskCycle(t *testing.T, calls []ToolCall, parallel bool) *askFixture {
	t.Helper()
	f := &askFixture{}
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "in",
		ParallelActs: parallel,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			f.thinks++
			if f.thinks == 1 {
				return &Decision{ToolCalls: calls}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			f.actLog = append(f.actLog, a.Call.Name)
			return &Effect{Result: "ok-" + a.Call.Name}, nil
		}},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error { return nil },
			BeforeThink:     func(ctx context.Context, p *Prompt) error { return nil },
			AfterThink:      func(ctx context.Context, d *Decision) error { return nil },
			BeforeAct: func(ctx context.Context, a *Action) error {
				f.before = append(f.before, a.Call.Name)
				return nil
			},
			AfterAct: func(ctx context.Context, a *Action, e *Effect, err error) {},
			OnError:  func(ctx context.Context, err error) {},
			OnCycleEnd: func(ctx context.Context, output string, _ CycleOutcome) {
				f.cycles++
			},
		},
		Sandbox: askSandbox{},
	}
	fillRequired(lc)
	(DecisionLoop{}).Cycle(context.Background(), lc, func(e Event) bool {
		f.events = append(f.events, e)
		if e.Kind == EventWaitInput {
			f.session = e.Wait.Session
			f.suspended = true
			return false // stop consuming; host saves the handle
		}
		return true
	})
	return f
}

// idx returns the index of the first event of kind k.
func (f *askFixture) idx(k EventKind) int {
	return slices.IndexFunc(f.events, func(e Event) bool { return e.Kind == k })
}

// TestSandboxAskSuspendsLoop verifies an Ask ruling suspends the round at
// the asked call: one ask audit, StateWaiting + EventWaitInput, iterator
// ends without Done/Error, nothing after the ask executes, the remaining
// calls ride the Session, and OnCycleEnd fires exactly once (one Think —
// the suspension consumes no round).
func TestSandboxAskSuspendsLoop(t *testing.T) {
	calls := []ToolCall{
		{ID: "t1", Name: "alpha"},
		{ID: "t2", Name: "dangerous"},
		{ID: "t3", Name: "gamma"},
	}
	f := runAskCycle(t, calls, false)
	if !f.suspended {
		t.Fatalf("loop did not suspend; last events: %v", kindsOf(f.events))
	}
	sb := slices.IndexFunc(f.events, func(e Event) bool {
		return e.Kind == EventSandbox && e.Verdict != nil && e.Verdict.Ruling == VerdictAsk
	})
	wait := f.idx(EventWaitInput)
	if sb < 0 || wait < 0 || wait < sb {
		t.Fatalf("event order wrong: sandbox@%d wait@%d; kinds: %v", sb, wait, kindsOf(f.events))
	}
	v := f.events[sb].Verdict
	if v.Ruling != VerdictAsk || v.Question != "confirm dangerous?" || v.Call.ID != "t2" {
		t.Fatalf("ask audit = %+v, want Ruling=Ask Question=%q Call=t2", v, "confirm dangerous?")
	}
	st := slices.IndexFunc(f.events, func(e Event) bool {
		return e.Kind == EventState && e.State == StateWaiting
	})
	if st < 0 {
		t.Fatalf("missing StateWaiting event; kinds: %v", kindsOf(f.events))
	}
	w := f.events[wait].Wait
	if w.Call.ID != "t2" || w.Question != "confirm dangerous?" {
		t.Fatalf("WaitInput = %+v, want call t2 with the ask question", w.Call)
	}
	if rem := w.Session.RemainingCalls(); len(rem) != 1 || rem[0].ID != "t3" {
		t.Fatalf("RemainingCalls = %+v, want [t3 gamma]", rem)
	}
	for _, e := range f.events {
		if e.Kind == EventDone || e.Kind == EventError {
			t.Fatalf("suspension must end quietly, got %v; kinds: %v", e.Kind, kindsOf(f.events))
		}
	}
	if len(f.actLog) != 1 || f.actLog[0] != "alpha" {
		t.Fatalf("actLog = %v, want only alpha ran before the ask", f.actLog)
	}
	if f.thinks != 1 {
		t.Fatalf("thinks = %d, want 1 (no extra round consumed)", f.thinks)
	}
	if f.cycles != 1 {
		t.Fatalf("OnCycleEnd calls = %d, want exactly 1", f.cycles)
	}
}

// TestSandboxAskResumeDeny verifies the denial arm: empty response (or a
// "[denied: ...]" payload — the host's timeout recipe) resolves the ask as
// a denial, the pending call gets a [sandbox-denied: ...] structured
// feedback (input protocol and output feedback are distinct formats), the
// tool never executes, and the loop digests to Done on the same round quota.
func TestSandboxAskResumeDeny(t *testing.T) {
	cases := []struct {
		resp   string
		wantFb string
	}{
		{"", "[sandbox-denied: declined]"},
		{"[denied: timeout]", "[sandbox-denied: timeout]"},
	}
	for _, c := range cases {
		calls := []ToolCall{{ID: "t1", Name: "alpha"}, {ID: "t2", Name: "dangerous"}}
		f := runAskCycle(t, calls, false)
		if !f.suspended {
			t.Fatalf("%q: base cycle did not suspend", c.resp)
		}
		lc2 := &LoopContext{
			CellID:  "c1",
			Sandbox: askSandbox{},
			Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
				f.actLog = append(f.actLog, a.Call.Name)
				return &Effect{Result: "ok-" + a.Call.Name}, nil
			}},
			Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
				f.thinks++
				return &Decision{Text: "digested"}, nil
			}},
		}
		ev2 := collectResume(context.Background(), lc2, f.session, c.resp)
		last := ev2[len(ev2)-1]
		if last.Kind != EventDone {
			t.Fatalf("%q: resume ended with %v, want Done; kinds: %v", c.resp, last.Kind, kindsOf(ev2))
		}
		var gotFb string
		for _, e := range ev2 {
			if e.Kind == EventToolResult && e.ToolCall != nil && e.ToolCall.ID == "t2" {
				gotFb = e.Effect.Err
			}
		}
		if gotFb != c.wantFb {
			t.Fatalf("%q: denial feedback = %q, want %q", c.resp, gotFb, c.wantFb)
		}
		if slices.Contains(f.actLog, "dangerous") {
			t.Fatalf("%q: dangerous executed despite denial; log %v", c.resp, f.actLog)
		}
		// The ask chain closes with one terminal record carrying the verdict
		// note — plain gate audits carry no reason and must not confuse it.
		denies := 0
		for _, e := range ev2 {
			if e.Kind == EventSandbox && e.Verdict.Ruling == VerdictDeny && e.Verdict.Reason != "" {
				denies++
			}
		}
		if denies != 1 {
			t.Fatalf("%q: resolve denies = %d, want 1 (chain closure)", c.resp, denies)
		}
	}
}

// TestSandboxAskResumeApprove verifies the approval arm: a non-empty,
// non-[denied response approves the call — BeforeAct then Act run the
// pending call, the remaining calls follow, and the digest completes
// without an extra round.
func TestSandboxAskResumeApprove(t *testing.T) {
	calls := []ToolCall{
		{ID: "t1", Name: "alpha"},
		{ID: "t2", Name: "dangerous"},
		{ID: "t3", Name: "gamma"},
	}
	f := runAskCycle(t, calls, false)
	if !f.suspended {
		t.Fatal("base cycle did not suspend")
	}
	lc2 := &LoopContext{
		CellID:  "c1",
		Sandbox: askSandbox{},
		Hooks: &Hooks{
			BeforeStimulate: func(ctx context.Context, p *Prompt) error { return nil },
			AfterStimulate:  func(ctx context.Context, output string) {},
			BeforeThink:     func(ctx context.Context, p *Prompt) error { return nil },
			AfterThink:      func(ctx context.Context, d *Decision) error { return nil },
			BeforeAct: func(ctx context.Context, a *Action) error {
				f.before = append(f.before, a.Call.Name)
				return nil
			},
			AfterAct:   func(ctx context.Context, a *Action, e *Effect, err error) {},
			OnError:    func(ctx context.Context, err error) {},
			OnCycleEnd: func(ctx context.Context, output string, _ CycleOutcome) {},
		},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			f.actLog = append(f.actLog, a.Call.Name)
			return &Effect{Result: "ok-" + a.Call.Name}, nil
		}},
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			f.thinks++
			return &Decision{Text: "digested"}, nil
		}},
	}
	ev2 := collectResume(context.Background(), lc2, f.session, "yes, go ahead")
	if last := ev2[len(ev2)-1]; last.Kind != EventDone {
		t.Fatalf("resume ended with %v, want Done; kinds: %v", last.Kind, kindsOf(ev2))
	}
	wantOrder := []string{"alpha", "dangerous", "gamma"}
	if !slices.Equal(f.actLog, wantOrder) {
		t.Fatalf("actLog = %v, want %v", f.actLog, wantOrder)
	}
	if !slices.Equal(f.before, wantOrder) {
		t.Fatalf("BeforeAct sequence = %v, want %v (approved call gates through BeforeAct)", f.before, wantOrder)
	}
	var okRes string
	for _, e := range ev2 {
		if e.Kind == EventToolResult && e.ToolCall != nil && e.ToolCall.ID == "t2" {
			okRes = e.Effect.Result
		}
	}
	if okRes != "ok-dangerous" {
		t.Fatalf("approved result = %q, want ok-dangerous", okRes)
	}
	// The terminal resolve record carries the response note; gamma's plain
	// gate audit (empty reason) must not confuse it.
	allowed := 0
	for _, e := range ev2 {
		if e.Kind == EventSandbox && e.Verdict.Ruling == VerdictAllow && e.Verdict.Reason != "" {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("resolve allows = %d, want 1 (chain closure)", allowed)
	}
}

// TestSandboxAskMarshalRoundTrip verifies an ask-suspended handle survives
// Marshal → UnmarshalSession and approves correctly after restoration
// (cross-process recovery parity with ask_user sessions).
func TestSandboxAskMarshalRoundTrip(t *testing.T) {
	calls := []ToolCall{{ID: "t1", Name: "dangerous"}, {ID: "t2", Name: "gamma"}}
	f := runAskCycle(t, calls, false)
	if !f.suspended {
		t.Fatal("base cycle did not suspend")
	}
	b, err := f.session.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := UnmarshalSession(b)
	if err != nil {
		t.Fatalf("UnmarshalSession: %v", err)
	}
	lc2 := &LoopContext{
		CellID:  "c1",
		Sandbox: askSandbox{},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			f.actLog = append(f.actLog, a.Call.Name)
			return &Effect{Result: "ok-" + a.Call.Name}, nil
		}},
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			f.thinks++
			return &Decision{Text: "digested"}, nil
		}},
	}
	ev2 := collectResume(context.Background(), lc2, got, "approved")
	if last := ev2[len(ev2)-1]; last.Kind != EventDone {
		t.Fatalf("restored resume ended with %v, want Done; kinds: %v", last.Kind, kindsOf(ev2))
	}
	if !slices.Equal(f.actLog, []string{"dangerous", "gamma"}) {
		t.Fatalf("actLog = %v, want [dangerous gamma]", f.actLog)
	}
}

// TestSandboxAskParallelBatch verifies the batch rule: gating stops at the
// first Ask — phase 2 never starts, already-admitted-but-unexecuted
// siblings ride the snapshot, and the approved pending call executes first
// at Resume followed by the remaining calls in original order.
func TestSandboxAskParallelBatch(t *testing.T) {
	calls := []ToolCall{
		{ID: "t1", Name: "alpha"},
		{ID: "t2", Name: "dangerous"},
		{ID: "t3", Name: "gamma"},
	}
	f := runAskCycle(t, calls, true)
	if !f.suspended {
		t.Fatal("parallel batch did not suspend at the ask")
	}
	if len(f.actLog) != 0 {
		t.Fatalf("phase 2 ran despite ask: actLog = %v", f.actLog)
	}
	rem := f.session.RemainingCalls()
	if len(rem) != 2 || rem[0].Name != "alpha" || rem[1].Name != "gamma" {
		t.Fatalf("RemainingCalls = %+v, want alpha+gamma", rem)
	}
	lc2 := &LoopContext{
		CellID:  "c1",
		Sandbox: askSandbox{},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			f.actLog = append(f.actLog, a.Call.Name)
			return &Effect{Result: "ok-" + a.Call.Name}, nil
		}},
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			f.thinks++
			return &Decision{Text: "digested"}, nil
		}},
	}
	ev2 := collectResume(context.Background(), lc2, f.session, "approved")
	if last := ev2[len(ev2)-1]; last.Kind != EventDone {
		t.Fatalf("resume ended with %v, want Done; kinds: %v", last.Kind, kindsOf(ev2))
	}
	wantOrder := []string{"dangerous", "alpha", "gamma"}
	if !slices.Equal(f.actLog, wantOrder) {
		t.Fatalf("resume actLog = %v, want %v (pending first, then remaining in order)", f.actLog, wantOrder)
	}
}
