// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_extensions_test.go — Pause/Resume, Sandbox.Bounds, tool timeout/retry.
package meowire_test

import (
	"context"
	"encoding/json"
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
// with a Session and ends the iterator; Resume(sess, Response{}) continues the loop
// (unified suspension-resume path).
func TestAgentPauseResumeMidLoop(t *testing.T) {
	a, err := testNew(testOrgans(t, meowire.Organs{},
		testutil.FakeCompletion{Text: "t", ToolCalls: []testutil.FakeCall{{ID: "x", Name: "tool"}}},
		testutil.FakeCompletion{Text: "done"},
	), meowire.Config{})
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

	// The paused loop continues via Resume(sess, Response{}) — no Unpause needed,
	// and the handle says for itself that this was the pause flavour.
	if got := sess.Kind(); got != meowire.WaitPause {
		t.Fatalf("Session.Kind() = %d, want meowire.WaitPause", got)
	}
	if !hasKind(collect(a.Resume(context.Background(), sess, meowire.Response{})), meowire.EventDone) {
		t.Fatal("expected EventDone after Resume from pause")
	}
}

// TestAgentPauseSuspendsNewStimulate verifies a pending pause suspends a new
// Stimulate at its first gap point: StatePaused + EventPaused are yielded
// and the iterator ends normally (no blocking, no Unpause needed to finish).
func TestAgentPauseSuspendsNewStimulate(t *testing.T) {
	a, err := testNew(testOrgans(t, meowire.Organs{}), meowire.Config{})
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
	if !hasKind(collect(a.Resume(context.Background(), sess, meowire.Response{})), meowire.EventDone) {
		t.Fatal("expected EventDone after Resume from entry pause")
	}
}

// TestResumeOfToolWaitKeepsPauseRequest verifies resuming one suspension does
// not consume a pause request aimed at the agent as a whole. A pause belongs to
// whichever loop is running; answering an unrelated tool wait has no claim on
// it, so the resumed round must honor the request at its next gap point instead
// of running to Done with the operator's stop swallowed.
func TestResumeOfToolWaitKeepsPauseRequest(t *testing.T) {
	a, err := testNew(testOrgans(t, meowire.Organs{
		Act: testutil.Effector{Fn: func(ctx context.Context, act meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{WaitInput: "question?"}, nil
		}},
	},
		testutil.FakeCompletion{Text: "ask", ToolCalls: []testutil.FakeCall{{ID: "t1", Name: "ask_user"}}},
		testutil.FakeCompletion{Text: "final"},
	), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var sess meowire.Session
	var sawWait bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventWaitInput {
			sess, sawWait = ev.Wait.Session, true
		}
	}
	if !sawWait {
		t.Fatal("the tool did not suspend the loop")
	}

	a.Pause() // stop requested while the tool wait is outstanding

	var sawPaused, sawDone bool
	for ev := range a.Resume(context.Background(), sess, meowire.Response{Answer: "yes"}) {
		switch ev.Kind {
		case meowire.EventPaused:
			sawPaused = true
		case meowire.EventDone:
			sawDone = true
		}
	}
	if sawDone || !sawPaused {
		t.Fatalf("resumed stream: paused=%v done=%v, want the pending pause honored (done must not be reached)", sawPaused, sawDone)
	}
}

// TestAgentPauseResumeIdempotent verifies Pause/Unpause are idempotent and
// safe under concurrent calls, and that a cleared request leaves the agent
// usable.
func TestAgentPauseResumeIdempotent(t *testing.T) {
	a, err := testNew(testOrgans(t, meowire.Organs{}), meowire.Config{})
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
	if !hasKind(collect(a.Stimulate(context.Background(), "work")), meowire.EventDone) {
		t.Fatal("expected EventDone after concurrent Pause/Resume storm")
	}
}

// TestAgentCloseAfterPauseVerifiesNoLeak verifies a paused Stimulate ends its
// iterator on its own (the pause yields EventPaused and finishes — no
// goroutine is left blocked, Close has nothing to unblock).
func TestAgentCloseAfterPauseVerifiesNoLeak(t *testing.T) {
	a, err := testNew(testOrgans(t, meowire.Organs{},
		testutil.FakeCompletion{Text: "t", ToolCalls: []testutil.FakeCall{{ID: "x", Name: "tool"}}},
		testutil.FakeCompletion{Text: "done"},
	), meowire.Config{})
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

// TestSandboxBoundsReachesBrain verifies Sandbox.Bounds is snapshotted per
// Stimulate and reaches the brain's request via the system bundle's bounds
// section.
func TestSandboxBoundsReachesBrain(t *testing.T) {
	fb := testutil.NewFakeBrain(t)
	a, err := testNew(testOrgans(t, meowire.Organs{
		Brain:   fb.Cfg(false),
		Sandbox: testutil.Sandbox{Bound: "read-only /workspace"},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	for range a.Stimulate(context.Background(), "work") {
	}
	system, _ := brainRequest(t, fb.Requests(), 0)
	if !strings.Contains(system, "## Execution bounds") || !strings.Contains(system, "read-only /workspace") {
		t.Fatalf("brain system bundle = %q, want the Sandbox.Bounds snapshot", system)
	}
}

// toolReplies decodes the tool-role messages (call id + content) of the n-th
// brain request.
func toolReplies(t *testing.T, bodies []string, n int) [][2]string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if n >= len(bodies) {
		t.Fatalf("brain request %d: only %d arrived", n, len(bodies))
	}
	if err := json.Unmarshal([]byte(bodies[n]), &req); err != nil {
		t.Fatalf("decode brain request %d: %v", n, err)
	}
	var out [][2]string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			out = append(out, [2]string{m.ToolCallID, m.Content})
		}
	}
	return out
}

// TestToolResultsReachBrain verifies structured tool feedback flows from the
// agent facade to the brain's request: the assistant/tool pairing carries
// the LLM-provided call IDs with Result as the reply text (host view of the
// F1 structured-feedback contract).
func TestToolResultsReachBrain(t *testing.T) {
	fb := testutil.NewFakeBrain(t,
		testutil.FakeCompletion{Text: "r1", ToolCalls: []testutil.FakeCall{
			{ID: "call_001", Name: "tool1"},
			{ID: "call_002", Name: "tool2"},
		}},
		testutil.FakeCompletion{Text: "r2"},
	)
	a, err := testNew(testOrgans(t, meowire.Organs{
		Brain: fb.Cfg(false),
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "out-" + a.Call.Name}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	if !hasKind(collect(a.Stimulate(context.Background(), "work")), meowire.EventDone) {
		t.Fatal("expected EventDone")
	}
	replies := toolReplies(t, fb.Requests(), 1)
	if len(replies) != 2 {
		t.Fatalf("round-2 tool replies = %v, want 2 entries", replies)
	}
	if replies[0][0] != "call_001" || replies[0][1] != "out-tool1" {
		t.Fatalf("tool reply 1 = %v, want call_001/out-tool1", replies[0])
	}
	if replies[1][0] != "call_002" || replies[1][1] != "out-tool2" {
		t.Fatalf("tool reply 2 = %v, want call_002/out-tool2", replies[1])
	}
}

// TestConfigToolTimeout verifies Config.ToolTimeout bounds each tool execution
// and the timeout error flows back as a structured ToolResults.Err entry
// (loop continues, no retry).
func TestConfigToolTimeout(t *testing.T) {
	actCalls := 0
	fb := testutil.NewFakeBrain(t,
		testutil.FakeCompletion{Text: "t", ToolCalls: []testutil.FakeCall{{ID: "x", Name: "slow"}}},
		testutil.FakeCompletion{Text: "recovered"},
	)
	a, err := testNew(testOrgans(t, meowire.Organs{
		Brain: fb.Cfg(false),
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

	if !hasKind(collect(a.Stimulate(context.Background(), "work")), meowire.EventDone) {
		t.Fatal("expected EventDone (timeout is feedback, not failure)")
	}
	if actCalls != 1 {
		t.Fatalf("Act calls = %d, want 1 (timeout error must not be retried)", actCalls)
	}
	replies := toolReplies(t, fb.Requests(), 1)
	if len(replies) != 1 || replies[0][0] != "x" {
		t.Fatalf("tool replies = %v, want one entry for call x", replies)
	}
	if !strings.Contains(replies[0][1], "deadline exceeded") {
		t.Fatalf("tool reply = %q, want the timeout text as the call's answer", replies[0][1])
	}
}

// TestConfigToolMaxRetries verifies Config.ToolMaxRetries retries transient
// effector errors and the loop completes without an error event.
func TestConfigToolMaxRetries(t *testing.T) {
	actCalls := 0
	a, err := testNew(testOrgans(t, meowire.Organs{
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			actCalls++
			if actCalls < 3 {
				return nil, errors.New("transient")
			}
			return &meowire.Effect{Result: "ok"}, nil
		}},
	},
		testutil.FakeCompletion{Text: "t", ToolCalls: []testutil.FakeCall{{ID: "x", Name: "flaky"}}},
		testutil.FakeCompletion{Text: "done"},
	), meowire.Config{ToolMaxRetries: 2})
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
