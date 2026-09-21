// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// emit_test.go — the output membrane: Sandbox.Emit rules each round's draft
// before the consumer, the accumulated output or the next Think hears it.
package nerve

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// emitSandbox rules the output side from a fixed script and admits every tool
// call, isolating the egress half of the membrane. seen collects the utterances
// it was asked to rule on.
type emitSandbox struct {
	ruling Verdict
	reason string
	err    error
	seen   *[]Utterance
}

func (s emitSandbox) Allow(context.Context, Action) (Verdict, string, error) {
	return VerdictAllow, "", nil
}
func (s emitSandbox) Bounds() string { return "emit-scripted" }
func (s emitSandbox) Emit(_ context.Context, u Utterance) (Verdict, string, error) {
	*s.seen = append(*s.seen, u)
	return s.ruling, s.reason, s.err
}

func TestEmitDenyReplacesTheDraft(t *testing.T) {
	var seen []Utterance
	lc := &LoopContext{
		CellID:  "c1",
		Input:   "in",
		Think:   textThink("secret draft"),
		Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
		Sandbox: emitSandbox{ruling: VerdictDeny, reason: "carries a key", seen: &seen},
	}
	events := collectEvents(context.Background(), lc)

	if len(seen) != 1 || seen[0].CellID != "c1" || seen[0].Round != 1 || seen[0].Text != "secret draft" {
		t.Fatalf("membrane asked = %+v, want one c1/round 1/'secret draft'", seen)
	}
	want := "[sandbox-denied: carries a key]"
	if e := events[2]; e.Kind != EventText || e.Text != want {
		t.Fatalf("events[2] = %+v, want EventText(%q)", e, want)
	}
	if e := events[len(events)-1]; e.Kind != EventDone || e.Output != want {
		t.Fatalf("last event = %+v, want EventDone(%q)", e, want)
	}
	for _, e := range events {
		if strings.Contains(e.Text, "secret draft") || strings.Contains(e.Output, "secret draft") {
			t.Fatalf("withheld draft leaked into %+v", e)
		}
	}
}

// TestEmitDenyJoinsContextTrack: the veto is feedback for the brain, not a tool
// result — the next Think reads it in the Context track.
func TestEmitDenyJoinsContextTrack(t *testing.T) {
	var seen []Utterance
	rounds := 0
	var nextCtx []string
	lc := &LoopContext{
		CellID: "c1",
		Input:  "in",
		Think: mockThinker{fn: func(_ context.Context, p *Prompt) (*Decision, error) {
			rounds++
			if rounds == 1 {
				return &Decision{Text: "draft", ToolCalls: []ToolCall{{ID: "t1", Name: "work"}}}, nil
			}
			nextCtx = append([]string(nil), p.Context...)
			return &Decision{Text: "done"}, nil
		}},
		Act:     okEffector(),
		Sandbox: emitSandbox{ruling: VerdictDeny, reason: "policy", seen: &seen},
	}
	collectEvents(context.Background(), lc)
	if want := "[sandbox-denied: policy]"; !slices.Contains(nextCtx, want) {
		t.Fatalf("context at the next Think = %v, want it to carry %q", nextCtx, want)
	}
}

func TestEmitErrorFailsClosed(t *testing.T) {
	var seen []Utterance
	lc := &LoopContext{
		CellID:  "c1",
		Input:   "in",
		Think:   textThink("draft"),
		Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
		Sandbox: emitSandbox{err: errors.New("filter down"), seen: &seen},
	}
	events := collectEvents(context.Background(), lc)

	verdicts := utteranceVerdicts(events)
	if len(verdicts) != 1 {
		t.Fatalf("utterance verdicts = %d, want 1", len(verdicts))
	}
	v := verdicts[0]
	if v.Ruling != VerdictDeny || v.Err == nil || !strings.Contains(v.Reason, "filter down") {
		t.Fatalf("verdict = %+v, want fail-closed Deny carrying the evaluation error", v)
	}
	if e := events[2]; e.Kind != EventText || e.Text != "[sandbox-denied: sandbox error: filter down]" {
		t.Fatalf("events[2] = %+v, want the denial text", e)
	}
}

// TestEmitAskSuspendsWithDraftWithheld: an Ask ruling stops the round before the
// text event exists at all; the draft rides the Session, the question rides the
// wait, and the round's tool calls wait behind the draft.
func TestEmitAskSuspendsWithDraftWithheld(t *testing.T) {
	var seen []Utterance
	lc := &LoopContext{
		CellID: "c1",
		Input:  "in",
		Think: mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
			return &Decision{Text: "pending draft", ToolCalls: []ToolCall{{ID: "t1", Name: "work"}}}, nil
		}},
		Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
		Sandbox: emitSandbox{ruling: VerdictAsk, reason: "confirm this?", seen: &seen},
	}
	events, wait := runSuspendingCycle(t, lc)

	if slices.Contains(kindsOf(events), EventText) {
		t.Fatalf("a withheld round must emit no text event; kinds: %v", kindsOf(events))
	}
	if wait == nil || wait.Question != "confirm this?" || wait.Call != (ToolCall{}) {
		t.Fatalf("wait = %+v, want the ask question and no pending call", wait)
	}
	if rem := wait.Session.RemainingCalls(); len(rem) != 1 || rem[0].Name != "work" {
		t.Fatalf("RemainingCalls = %+v, want [work]", rem)
	}
	verdicts := utteranceVerdicts(events)
	if len(verdicts) != 1 || verdicts[0].Ruling != VerdictAsk || verdicts[0].Question != "confirm this?" {
		t.Fatalf("utterance verdicts = %+v, want one Ask record carrying the question", verdicts)
	}
	for _, e := range events {
		if strings.Contains(e.Text, "pending draft") {
			t.Fatalf("draft leaked into the audit stream: %+v", e)
		}
	}
}

// TestEmitAskResumeArms closes the confirmation chain through the shared
// Response grammar. The round had nothing left to execute, so the resumed cell
// says the outcome and finishes without another Think.
func TestEmitAskResumeArms(t *testing.T) {
	cases := []struct {
		name     string
		response Response
		wantSaid string
	}{
		{"answered", Response{Answer: "go ahead"}, "pending draft"},
		{"silent", Response{}, "[sandbox-denied: declined]"},
		{"refused", Response{Deny: "not now"}, "[sandbox-denied: not now]"},
		{"refusal outranks an answer", Response{Answer: "go ahead", Deny: "not now"}, "[sandbox-denied: not now]"},
	}
	for _, c := range cases {
		var seen []Utterance
		lc := &LoopContext{
			CellID:  "c1",
			Input:   "in",
			Think:   textThink("pending draft"),
			Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
			Sandbox: emitSandbox{ruling: VerdictAsk, reason: "confirm?", seen: &seen},
		}
		_, wait := runSuspendingCycle(t, lc)
		if wait == nil {
			t.Fatalf("%s: base cycle did not suspend", c.name)
		}

		thinks := 0
		lc2 := &LoopContext{
			CellID:  "c1",
			Sandbox: testSandbox{},
			Think: mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
				thinks++
				return &Decision{Text: "should not run"}, nil
			}},
			Act: mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
		}
		events := collectResponse(context.Background(), lc2, wait.Session, c.response)
		if thinks != 0 {
			t.Fatalf("%s: resumed Think calls = %d, want 0 (the round already thought)", c.name, thinks)
		}
		said := slices.IndexFunc(events, func(e Event) bool { return e.Kind == EventText })
		if said < 0 || events[said].Text != c.wantSaid {
			t.Fatalf("%s: said = %+v, want %q", c.name, events[said:], c.wantSaid)
		}
		if last := events[len(events)-1]; last.Kind != EventDone || last.Output != c.wantSaid {
			t.Fatalf("%s: last event = %+v, want EventDone(%q)", c.name, last, c.wantSaid)
		}
	}
}

// TestEmitAskResumeRunsRoundCalls: approval unblocks the whole round — the
// draft is said as generated, the calls that waited behind it execute, and the
// digest Think closes the cycle on the suspended round's quota.
func TestEmitAskResumeRunsRoundCalls(t *testing.T) {
	var seen []Utterance
	lc := &LoopContext{
		CellID: "c1",
		Input:  "in",
		Think: mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
			return &Decision{Text: "pending draft", ToolCalls: []ToolCall{{ID: "t1", Name: "work"}}}, nil
		}},
		Act:     okEffector(),
		Sandbox: emitSandbox{ruling: VerdictAsk, reason: "confirm?", seen: &seen},
	}
	_, wait := runSuspendingCycle(t, lc)

	digests := 0
	lc2 := &LoopContext{
		CellID:  "c1",
		Sandbox: testSandbox{},
		Think: mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
			digests++
			return &Decision{Text: " digested"}, nil
		}},
		Act: mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{Result: "done"}, nil }},
	}
	events := collectResume(context.Background(), lc2, wait.Session, "approved")
	if digests != 1 {
		t.Fatalf("digest Think calls = %d, want 1", digests)
	}
	last := events[len(events)-1]
	if last.Kind != EventDone || last.Output != "pending draft digested" {
		t.Fatalf("last event = %+v, want EventDone(%q)", last, "pending draft digested")
	}
}

// TestEmitAskMarshalRoundTrip: the withheld draft is part of the suspension, so
// it must survive the wire — a resume in another process says the same text.
func TestEmitAskMarshalRoundTrip(t *testing.T) {
	var seen []Utterance
	lc := &LoopContext{
		CellID:  "c1",
		Input:   "in",
		Think:   textThink("draft across the wire"),
		Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
		Sandbox: emitSandbox{ruling: VerdictAsk, reason: "confirm?", seen: &seen},
	}
	_, wait := runSuspendingCycle(t, lc)
	b, err := wait.Session.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := UnmarshalSession(b)
	if err != nil {
		t.Fatalf("UnmarshalSession: %v", err)
	}
	lc2 := &LoopContext{
		CellID:  "c1",
		Sandbox: testSandbox{},
		Think:   textThink("should not run"),
		Act:     mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
	}
	events := collectResume(context.Background(), lc2, got, "approved")
	if last := events[len(events)-1]; last.Kind != EventDone || last.Output != "draft across the wire" {
		t.Fatalf("restored resume = %+v, want EventDone with the withheld draft", last)
	}
}
