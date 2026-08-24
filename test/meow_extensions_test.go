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

// TestAgentPauseResumeMidLoop verifies Pause() takes effect at the next gap
// point (before a tool execution) and Resume() lets the loop continue.
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

	go func() {
		time.Sleep(20 * time.Millisecond)
		a.Resume()
	}()

	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
		if ev.Kind == meowire.EventToolCall {
			a.Pause() // request pause right after the tool call is announced
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
	if last.Kind != meowire.EventDone {
		t.Fatalf("last event = %+v, want EventDone after resume", last)
	}
}

// TestAgentPauseBlocksStimulateEntry verifies a pending pause suspends a new
// Stimulate at its first gap point until Resume is called.
func TestAgentPauseBlocksStimulateEntry(t *testing.T) {
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

	var mu sync.Mutex
	var events []meowire.Event
	pausedSeen := make(chan struct{})
	done := make(chan struct{})
	go func() {
		for ev := range a.Stimulate(context.Background(), "work") {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			if ev.Kind == meowire.EventState && ev.State == meowire.StatePaused {
				close(pausedSeen)
			}
		}
		close(done)
	}()

	select {
	case <-pausedSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventState(StatePaused)")
	}
	a.Resume()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Stimulate to finish after resume")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 || events[0].Kind != meowire.EventState || events[0].State != meowire.StatePaused {
		t.Fatalf("first event = %+v, want EventState(StatePaused)", events[0])
	}
	last := events[len(events)-1]
	if last.Kind != meowire.EventDone {
		t.Fatalf("last event = %+v, want EventDone", last)
	}
}

// TestAgentPauseResumeIdempotent verifies Pause/Resume are idempotent and
// safe under concurrent calls (no double-close panic, no lost resume).
func TestAgentPauseResumeIdempotent(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.Pause() }()
		go func() { defer wg.Done(); a.Resume() }()
	}
	wg.Wait()

	// After the storm the agent must still be usable: final Resume then run.
	a.Resume()
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

// TestAgentCloseUnblocksPausedStimulate verifies Close() unblocks a
// Stimulate blocked on a pending pause so its iterator finishes instead of
// leaking the consuming goroutine.
func TestAgentCloseUnblocksPausedStimulate(t *testing.T) {
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

	var mu sync.Mutex
	var events []meowire.Event
	pausedSeen := make(chan struct{})
	done := make(chan struct{})
	go func() {
		for ev := range a.Stimulate(context.Background(), "work") {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			if ev.Kind == meowire.EventToolCall {
				a.Pause() // request pause at the tool gap
			}
			if ev.Kind == meowire.EventState && ev.State == meowire.StatePaused {
				close(pausedSeen)
			}
		}
		close(done)
	}()

	select {
	case <-pausedSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventState(StatePaused)")
	}
	a.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out: Close() did not unblock the paused Stimulate")
	}

	mu.Lock()
	defer mu.Unlock()
	last := events[len(events)-1]
	if last.Kind != meowire.EventDone {
		t.Fatalf("last event = %+v, want EventDone after Close unblocks", last)
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

// TestConfigToolTimeout verifies Config.ToolTimeout bounds each tool execution
// and the timeout error flows back as feedback (loop continues, no retry).
func TestConfigToolTimeout(t *testing.T) {
	actCalls := 0
	calls := 0
	var capturedCtx []string
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			if calls == 2 {
				capturedCtx = p.Context
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
	found := false
	for _, c := range capturedCtx {
		if strings.Contains(c, "deadline exceeded") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("context = %v, want entry containing 'deadline exceeded'", capturedCtx)
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
