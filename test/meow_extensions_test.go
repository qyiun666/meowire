// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_extensions_test.go — Pause/Resume, Sandbox.Bounds, tool timeout/retry.
package meowire_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// kinds returns the EventKind sequence of events (test assertion helper).
func kinds(events []meowire.Event) []meowire.EventKind {
	k := make([]meowire.EventKind, 0, len(events))
	for _, e := range events {
		k = append(k, e.Kind)
	}
	return k
}

// TestAgentPauseResumeMidLoop verifies Pause() takes effect at the next gap
// point (before a tool execution): the loop yields StatePaused + EventPaused
// with a Session and ends the iterator; Resume(sess, "") continues the loop
// (unified suspension-resume path, v1.4.0).
func TestAgentPauseResumeMidLoop(t *testing.T) {
	calls := 0
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			if calls == 1 {
				return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "x", Name: "tool"}}}, nil
			}
			return &meowire.Decision{Text: "done"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "ok"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var events []meowire.Event
	var sess meowire.Session
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
		if ev.Kind == meowire.EventToolCall {
			a.Pause() // request pause right after the tool call is announced
		}
		if ev.Kind == meowire.EventPaused {
			sess = ev.Wait.Session
		}
	}

	foundPaused := false
	for _, e := range events {
		if e.Kind == meowire.EventState && e.State == meowire.StatePaused {
			foundPaused = true
			break
		}
	}
	if !foundPaused {
		t.Fatalf("events = %+v, want an EventState(StatePaused) entry", events)
	}
	last := events[len(events)-1]
	if last.Kind != meowire.EventPaused {
		t.Fatalf("last event = %+v, want EventPaused (iterator ends on the pause); events: %v", last, kinds(events))
	}

	// The paused loop continues via Resume(sess, "") — no Unpause needed.
	var gotDone bool
	for ev := range a.Resume(context.Background(), sess, "") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone after Resume from pause")
	}
}

// TestAgentPauseSuspendsNewStimulate verifies a pending pause suspends a new
// Stimulate at its first gap point: StatePaused + EventPaused are yielded
// and the iterator ends normally (no blocking, no Unpause needed to finish).
func TestAgentPauseSuspendsNewStimulate(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "ok"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	a.Pause()

	var events []meowire.Event
	var sess meowire.Session
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
		if ev.Kind == meowire.EventPaused {
			sess = ev.Wait.Session
		}
	}
	if len(events) != 2 || events[0].Kind != meowire.EventState || events[0].State != meowire.StatePaused || events[1].Kind != meowire.EventPaused {
		t.Fatalf("events = %+v, want [StatePaused, EventPaused]", events)
	}

	// Resume continues the suspended run (Resume clears the pause request).
	var gotDone bool
	for ev := range a.Resume(context.Background(), sess, "") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone after Resume from entry pause")
	}
}

// TestAgentPauseResumeIdempotent verifies Pause/Unpause are idempotent and
// safe under concurrent calls, and that a cleared request leaves the agent
// usable.
func TestAgentPauseResumeIdempotent(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() { defer wg.Done(); a.Pause() }()
		go func() { defer wg.Done(); a.Unpause() }()
	}
	wg.Wait()

	// After the storm the agent must still be usable: clear the request then
	// run (a leftover request would suspend the run at its first gap point).
	a.Unpause()
	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone after concurrent Pause/Resume storm")
	}
}

// TestAgentCloseAfterPauseVerifiesNoLeak verifies a paused Stimulate ends its
// iterator on its own (the pause yields EventPaused and finishes — no
// goroutine is left blocked, Close has nothing to unblock).
func TestAgentCloseAfterPauseVerifiesNoLeak(t *testing.T) {
	calls := 0
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			if calls == 1 {
				return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "x", Name: "tool"}}}, nil
			}
			return &meowire.Decision{Text: "done"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "ok"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range a.Stimulate(context.Background(), "work") {
			if ev.Kind == meowire.EventToolCall {
				a.Pause() // request pause at the tool gap
			}
		}
	}()

	select {
	case <-done:
		// The paused iterator finished by itself — nothing is left blocked.
	case <-time.After(2 * time.Second):
		t.Fatal("timed out: paused Stimulate must end its iterator without Close")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSandboxBoundsReachesThinker verifies Sandbox.Bounds is snapshotted per
// Stimulate and surfaced to the Thinker via Prompt.Bounds.
func TestSandboxBoundsReachesThinker(t *testing.T) {
	var gotBounds string
	a, err := testNew(testOrgans(meowire.Organs{
		Sandbox: testutil.Sandbox{Bound: "read-only /workspace"},
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			gotBounds = p.Bounds
			return &meowire.Decision{Text: "ok"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	for range a.Stimulate(context.Background(), "work") {
	}
	if gotBounds != "read-only /workspace" {
		t.Fatalf("Prompt.Bounds = %q, want %q", gotBounds, "read-only /workspace")
	}
}

// TestToolResultsReachThinker verifies structured tool feedback flows from
// the agent facade to the Thinker: Prompt.ToolResults accumulates across
// rounds with the LLM-provided call IDs preserved and Result carrying the
// raw output (host view of the B1 structured-feedback contract).
func TestToolResultsReachThinker(t *testing.T) {
	calls := 0
	var results []meowire.ToolResult
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			if calls == 1 {
				return &meowire.Decision{Text: "r1", ToolCalls: []meowire.ToolCall{
					{ID: "call_001", Name: "tool1"},
					{ID: "call_002", Name: "tool2"},
				}}, nil
			}
			results = append([]meowire.ToolResult(nil), p.ToolResults...)
			return &meowire.Decision{Text: "r2"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "out-" + a.Call.Name}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone")
	}
	if len(results) != 2 {
		t.Fatalf("Prompt.ToolResults = %+v, want 2 entries", results)
	}
	if results[0].ID != "call_001" || results[0].Name != "tool1" || results[0].Result != "out-tool1" || results[0].Err != "" {
		t.Fatalf("ToolResults[0] = %+v, want call_001/tool1/Result=out-tool1", results[0])
	}
	if results[1].ID != "call_002" || results[1].Result != "out-tool2" {
		t.Fatalf("ToolResults[1] = %+v, want call_002/Result=out-tool2", results[1])
	}
}

// TestConfigToolTimeout verifies Config.ToolTimeout bounds each tool execution
// and the timeout error flows back as a structured ToolResults.Err entry
// (loop continues, no retry).
func TestConfigToolTimeout(t *testing.T) {
	actCalls := 0
	calls := 0
	var capturedResults []meowire.ToolResult
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			if calls == 2 {
				capturedResults = append([]meowire.ToolResult(nil), p.ToolResults...)
			}
			if calls == 1 {
				return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "x", Name: "slow"}}}, nil
			}
			return &meowire.Decision{Text: "recovered"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			actCalls++
			<-ctx.Done() // block until the per-tool timeout fires
			return nil, ctx.Err()
		}},
	}), meowire.Config{ToolTimeout: 20 * time.Millisecond, ToolMaxRetries: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone (timeout is feedback, not failure)")
	}
	if actCalls != 1 {
		t.Fatalf("Act calls = %d, want 1 (timeout error must not be retried)", actCalls)
	}
	if len(capturedResults) != 1 || capturedResults[0].ID != "x" {
		t.Fatalf("ToolResults = %+v, want one entry x", capturedResults)
	}
	if !strings.Contains(capturedResults[0].Err, "deadline exceeded") {
		t.Fatalf("ToolResults[0].Err = %q, want entry containing 'deadline exceeded'", capturedResults[0].Err)
	}
}

// TestConfigToolMaxRetries verifies Config.ToolMaxRetries retries transient
// effector errors and the loop completes without an error event.
func TestConfigToolMaxRetries(t *testing.T) {
	actCalls := 0
	calls := 0
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			if calls == 1 {
				return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "x", Name: "flaky"}}}, nil
			}
			return &meowire.Decision{Text: "done"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			actCalls++
			if actCalls < 3 {
				return nil, errors.New("transient")
			}
			return &meowire.Effect{Result: "ok"}, nil
		}},
	}), meowire.Config{ToolMaxRetries: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var gotError bool
	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		switch ev.Kind {
		case meowire.EventError:
			gotError = true
		case meowire.EventDone:
			gotDone = true
		}
	}
	if gotError {
		t.Fatal("unexpected EventError after retry succeeded")
	}
	if !gotDone {
		t.Fatal("expected EventDone")
	}
	if actCalls != 3 {
		t.Fatalf("Act calls = %d, want 3 (initial + 2 retries)", actCalls)
	}
}
