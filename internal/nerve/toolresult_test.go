// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// toolresult_test.go — structured tool feedback (Prompt.ToolResults) tests.
package nerve

import (
	"context"
	"strings"
	"testing"
)

// TestDecisionLoopToolResultsAccumulate verifies ToolResults accumulate
// across a multi-round tool chain, echoing the LLM-provided call IDs with
// Result carrying the raw output — and that tool results never enter
// the Context text track (single-track contract).
func TestDecisionLoopToolResultsAccumulate(t *testing.T) {
	var seen [][]ToolResult // snapshot of p.ToolResults per Think
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			seen = append(seen, append([]ToolResult(nil), p.ToolResults...))
			switch len(seen) {
			case 1: // round 1: two parallel tool calls
				return &Decision{Text: "r1", ToolCalls: []ToolCall{
					{ID: "call_001", Name: "tool1", Args: "{}"},
					{ID: "call_002", Name: "tool2", Args: "{}"},
				}}, nil
			case 2: // round 2: one more call, digesting prior results
				return &Decision{Text: "r2", ToolCalls: []ToolCall{{ID: "call_003", Name: "tool3", Args: "{}"}}}, nil
			default: // round 3: done
				return &Decision{Text: "r3"}, nil
			}
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: "out-" + a.Call.Name}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)
	if events[len(events)-1].Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone; events: %v", events[len(events)-1], kindsOf(events))
	}

	// Round 2 sees tool1 + tool2 results (with their call IDs, raw Result).
	if len(seen) < 2 || len(seen[1]) != 2 {
		t.Fatalf("round-2 ToolResults = %+v, want 2 entries (tool1, tool2)", seen[1])
	}
	if seen[1][0].ID != "call_001" || seen[1][0].Name != "tool1" || seen[1][0].Result != "out-tool1" || seen[1][0].Err != "" {
		t.Fatalf("round-2 ToolResults[0] = %+v, want call_001/tool1/Result=out-tool1", seen[1][0])
	}
	if seen[1][1].ID != "call_002" || seen[1][1].Result != "out-tool2" {
		t.Fatalf("round-2 ToolResults[1] = %+v, want call_002/Result=out-tool2", seen[1][1])
	}

	// Round 3 sees all three (accumulation across rounds, IDs preserved).
	if len(seen) < 3 || len(seen[2]) != 3 {
		t.Fatalf("round-3 ToolResults = %+v, want 3 entries", seen[2])
	}
	if seen[2][2].ID != "call_003" || seen[2][2].Result != "out-tool3" {
		t.Fatalf("round-3 ToolResults[2] = %+v, want call_003/Result=out-tool3", seen[2][2])
	}

	// Single-track contract: the Context text track carries no tool results.
	for _, c := range lc.Context {
		if strings.Contains(c, "out-") {
			t.Fatalf("tool result leaked into Context text track: %q (context: %v)", c, lc.Context)
		}
	}
}

// TestDecisionLoopToolResultsDeniedExcluded verifies a sandbox denial stays
// on the text track only ([sandbox-denied: ...] in Context) and never
// appears in ToolResults — a verdict, not a tool result.
func TestDecisionLoopToolResultsDeniedExcluded(t *testing.T) {
	var seen []ToolResult
	lc := &LoopContext{
		CellID:  "c1",
		Input:   "work",
		Sandbox: denyToolSandbox{name: "tool1"},
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			if p.ToolResults != nil {
				seen = append([]ToolResult(nil), p.ToolResults...)
			}
			if seen != nil {
				return &Decision{Text: "r2"}, nil // round 2: done
			}
			return &Decision{Text: "r1", ToolCalls: []ToolCall{
				{ID: "call_001", Name: "tool1"}, {ID: "call_002", Name: "tool2"},
			}}, nil
		}},
		Act: okEffector(),
	}
	events := collectEvents(context.Background(), lc)
	if events[len(events)-1].Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone (denial continues the loop); events: %v", events[len(events)-1], kindsOf(events))
	}
	if len(seen) != 1 || seen[0].Name != "tool2" {
		t.Fatalf("ToolResults = %+v, want only tool2 (denied tool1 excluded)", seen)
	}
	found := false
	for _, c := range lc.Context {
		if strings.HasPrefix(c, "[sandbox-denied:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("denied feedback missing from Context: %v", lc.Context)
	}

	// The denied call's EventToolResult echoes the ToolCall for ID
	// association, consistent with successful results.
	var denied *Event
	for i := range events {
		if events[i].Kind == EventToolResult && events[i].ToolCall != nil && events[i].ToolCall.Name == "tool1" {
			denied = &events[i]
		}
	}
	if denied == nil {
		t.Fatal("denied EventToolResult must echo the ToolCall (ID association)")
	}
	if denied.Effect == nil || !strings.HasPrefix(denied.Effect.Err, "[sandbox-denied:") {
		t.Fatalf("denied EventToolResult Effect = %+v, want [sandbox-denied: ...] Err", denied.Effect)
	}
	if denied.ToolCall.ID != "call_001" {
		t.Fatalf("denied EventToolResult ToolCall.ID = %q, want call_001", denied.ToolCall.ID)
	}
}

// TestDecisionLoopToolResultsResumePreserved verifies the suspension
// snapshot carries the accumulated ToolResults and Resume appends the
// external response as the pending tool's structured result (ID preserved).
func TestDecisionLoopToolResultsResumePreserved(t *testing.T) {
	thinks := 0
	var nextResults []ToolResult
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			thinks++
			if thinks == 1 {
				return &Decision{Text: "multi", ToolCalls: []ToolCall{
					{ID: "call_001", Name: "tool1"}, {ID: "call_ask", Name: "ask_user"},
				}}, nil
			}
			nextResults = append([]ToolResult(nil), p.ToolResults...)
			return &Decision{Text: "final"}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			if a.Call.Name == "ask_user" {
				return &Effect{WaitInput: "q?"}, nil
			}
			return &Effect{Result: "ok"}, nil
		}},
	}
	_, wait := runSuspendingCycle(t, lc)
	if wait == nil {
		t.Fatal("no EventWaitInput yielded")
	}
	// The snapshot keeps the executed tool1 result in the structured track.
	if len(wait.Session.toolResults) != 1 || wait.Session.toolResults[0].ID != "call_001" {
		t.Fatalf("session toolResults = %+v, want [call_001 tool1]", wait.Session.toolResults)
	}
	collectResume(context.Background(), lc, wait.Session, "yes")

	// After Resume the digesting Think sees both: tool1's result (from the
	// snapshot) and the pending ask_user's response (ID = pending tool's ID).
	if len(nextResults) != 2 {
		t.Fatalf("post-resume ToolResults = %+v, want 2 entries", nextResults)
	}
	if nextResults[0].ID != "call_001" || nextResults[0].Result != "ok" {
		t.Fatalf("post-resume ToolResults[0] = %+v, want call_001/Result=ok", nextResults[0])
	}
	if nextResults[1].ID != "call_ask" || nextResults[1].Name != "ask_user" || nextResults[1].Result != "yes" || nextResults[1].Err != "" {
		t.Fatalf("post-resume ToolResults[1] = %+v, want call_ask/ask_user/Result=yes", nextResults[1])
	}
}

// TestDecisionLoopToolResultsErrField verifies a failing tool populates Err
// (Result stays empty) — the structured error marker hosts render from.
func TestDecisionLoopToolResultsErrField(t *testing.T) {
	var seen []ToolResult
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			if p.ToolResults != nil {
				seen = append([]ToolResult(nil), p.ToolResults...)
				return &Decision{Text: "r2"}, nil
			}
			return &Decision{Text: "r1", ToolCalls: []ToolCall{{ID: "call_001", Name: "tool1"}}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Err: "rejected by business logic"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)
	if events[len(events)-1].Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone; events: %v", events[len(events)-1], kindsOf(events))
	}
	if len(seen) != 1 || seen[0].Err != "rejected by business logic" || seen[0].Result != "" {
		t.Fatalf("ToolResults = %+v, want Err=rejected by business logic, Result empty", seen)
	}
}

// TestDecisionLoopToolResultsTruncationConsistent verifies MaxToolOutput
// truncation applies to Result with a UTF-8-safe marker.
func TestDecisionLoopToolResultsTruncationConsistent(t *testing.T) {
	var seen []ToolResult
	lc := &LoopContext{
		CellID:        "c1",
		Input:         "work",
		MaxToolOutput: 20,
		Think: mockThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
			if p.ToolResults != nil {
				seen = append([]ToolResult(nil), p.ToolResults...)
				return &Decision{Text: "r2"}, nil // round 2: done
			}
			return &Decision{Text: "r1", ToolCalls: []ToolCall{{ID: "call_001", Name: "tool1"}}}, nil
		}},
		Act: mockEffector{fn: func(ctx context.Context, a Action) (*Effect, error) {
			return &Effect{Result: strings.Repeat("x", 200)}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)
	if events[len(events)-1].Kind != EventDone {
		t.Fatalf("last event = %+v, want EventDone; events: %v", events[len(events)-1], kindsOf(events))
	}
	if len(seen) != 1 {
		t.Fatalf("ToolResults = %+v, want 1 entry", seen)
	}
	tr := seen[0]
	if !strings.HasPrefix(tr.Result, "xxxxxxxx") || !strings.HasSuffix(tr.Result, "[truncated, 200 bytes total]") {
		t.Fatalf("ToolResults.Result = %q, want truncated feedback", tr.Result)
	}
	if tr.Err != "" {
		t.Fatalf("ToolResults.Err = %q, want empty on success", tr.Err)
	}
}

// denyToolSandbox denies a single named tool.
type denyToolSandbox struct {
	name string
}

func (d denyToolSandbox) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	if a.Call.Name == d.name {
		return VerdictDeny, "not allowed", nil
	}
	return VerdictAllow, "", nil
}

func (d denyToolSandbox) Emit(context.Context, Utterance) (Verdict, string, error) {
	return VerdictAllow, "", nil
}

func (d denyToolSandbox) Bounds() string { return "test" }
