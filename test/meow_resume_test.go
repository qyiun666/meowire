// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_resume_test.go — integration tests: suspension/resume protocol,
// runtime config updates, replace audit events (facade level).
package meowire_test

import (
	"context"
	"strings"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestAgentResumeFlow verifies the full suspension-resume protocol at the
// facade: Stimulate suspends with EventWaitInput, Resume continues with the
// response as tool feedback, and the loop completes normally.
func TestAgentResumeFlow(t *testing.T) {
	thinks := 0
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			thinks++
			if thinks == 1 {
				return &meowire.Decision{Text: "ask", ToolCalls: []meowire.ToolCall{{ID: "t1", Name: "ask_user"}}}, nil
			}
			return &meowire.Decision{Text: "done"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{WaitInput: "may I?"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var sess meowire.Session
	var gotWait bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventWaitInput {
			sess = ev.Wait.Session
			gotWait = true
		}
	}
	if !gotWait {
		t.Fatal("no EventWaitInput yielded")
	}

	// Resume with the external response; the stream is isomorphic and the
	// loop completes normally.
	var gotDone bool
	for ev := range a.Resume(context.Background(), sess, "yes") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("Resume stream: want EventDone")
	}
}

// TestAgentResumeAfterClose verifies Resume after Close yields ErrCellClosed.
func TestAgentResumeAfterClose(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "ok"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var gotErr bool
	for ev := range a.Resume(context.Background(), meowire.Session{}, "x") {
		if ev.Kind == meowire.EventError && ev.Err == meowire.ErrCellClosed {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("Resume after Close: want ErrCellClosed")
	}
}

// TestAgentUpdateConfig verifies UpdateConfig takes effect at the next
// Stimulate while the in-flight loop keeps its snapshot, and GetConfig
// supports read-modify-write.
func TestAgentUpdateConfig(t *testing.T) {
	calls := 0
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			return &meowire.Decision{Text: "ok"}, nil
		}},
	}), meowire.Config{MaxRounds: 1})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	// Baseline: MaxRounds=1, one Think, done.
	for range a.Stimulate(context.Background(), "work") {
	}
	if calls != 1 {
		t.Fatalf("think calls = %d, want 1", calls)
	}

	// Read-modify-write via GetConfig/UpdateConfig.
	cfg := a.GetConfig()
	if cfg.MaxRounds != 1 {
		t.Fatalf("GetConfig MaxRounds = %d, want 1", cfg.MaxRounds)
	}
	cfg.MaxRounds = 3
	a.UpdateConfig(cfg)
	if got := a.GetConfig(); got.MaxRounds != 3 {
		t.Fatalf("GetConfig after update = %d, want 3", got.MaxRounds)
	}

	// Next Stimulate uses the new value — exercised end-to-end in
	// TestAgentUpdateConfigTakesEffect.
}

// TestAgentUpdateConfigTakesEffect verifies the updated MaxRounds bounds the
// next Stimulate (the effective value is snapshotted per Stimulate).
func TestAgentUpdateConfigTakesEffect(t *testing.T) {
	calls := 0
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			calls++
			// Always request a tool so the loop only ends by exhausting rounds.
			return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "t", Name: "tool"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "ok"}, nil
		}},
	}), meowire.Config{MaxRounds: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	// MaxRounds=2 → 2 thinks, then ErrMaxRounds.
	var gotMaxRounds bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventError && strings.Contains(ev.Err.Error(), "max rounds") {
			gotMaxRounds = true
		}
	}
	if !gotMaxRounds {
		t.Fatal("want ErrMaxRounds with MaxRounds=2")
	}
	if calls != 2 {
		t.Fatalf("think calls = %d, want 2", calls)
	}

	// Hot update to MaxRounds=4 → next Stimulate runs 4 rounds.
	cfg := a.GetConfig()
	cfg.MaxRounds = 4
	a.UpdateConfig(cfg)
	for ev := range a.Stimulate(context.Background(), "again") {
		if ev.Kind == meowire.EventError && strings.Contains(ev.Err.Error(), "max rounds") {
			gotMaxRounds = true
		}
	}
	if calls != 2+4 {
		t.Fatalf("think calls = %d, want %d (updated MaxRounds=4)", calls, 2+4)
	}
}

// TestAgentReplaceAuditEvent verifies Replace yields EventReplace at the
// start of the next Stimulate with slot/old/new carried.
func TestAgentReplaceAuditEvent(t *testing.T) {
	oldThink := testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
		return &meowire.Decision{Text: "old"}, nil
	}}
	newThink := testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
		return &meowire.Decision{Text: "new"}, nil
	}}
	a, err := testNew(testOrgans(meowire.Organs{Think: oldThink}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	if _, err := a.Replace(meowire.SlotThink, newThink); err != nil {
		t.Fatalf("replace: %v", err)
	}

	var got *meowire.ReplaceAudit
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventReplace {
			got = ev.Replace
			break
		}
	}
	if got == nil {
		t.Fatal("want EventReplace at the start of the next Stimulate")
	}
	if got.Slot != "think" {
		t.Fatalf("audit = %+v, want slot think", got)
	}
	if got.NewType != "testutil.Thinker" {
		t.Fatalf("audit new type = %q, want the swapped-in Thinker", got.NewType)
	}
}
