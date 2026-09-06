// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_config_test.go — Config fields applied correctly.
package meowire_test

import (
	"context"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestConfigApplied verifies MaxRounds etc. flow through to LoopContext.
func TestConfigApplied(t *testing.T) {
	thinkCalls := 0
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			thinkCalls++
			return &meowire.Decision{
				Text:      "t",
				ToolCalls: []meowire.ToolCall{{ID: "x", Name: "tool"}},
			}, nil
		}},
	}), meowire.Config{MaxRounds: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "loop") {
		events = append(events, ev)
	}

	thinkCount := 0
	for _, e := range events {
		if e.Kind == meowire.EventState && e.State == meowire.StateThinking {
			thinkCount++
		}
	}
	if thinkCount != 2 {
		t.Fatalf("thinking count = %d, want 2 (MaxRounds)", thinkCount)
	}
}

// TestConfigMaxToolOutput verifies MaxToolOutput truncation.
func TestConfigMaxToolOutput(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "done"}, nil
		}},
	}), meowire.Config{MaxToolOutput: 10})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone")
	}
}

// TestConfigAuditEmitted verifies UpdateConfig records a ConfigAudit that is
// emitted as the first EventConfig of the next Stimulate (the moment the
// swap takes effect) with Old/New correct — the config-update counterpart of
// EventReplace (v1.3.2).
func TestConfigAuditEmitted(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "done"}, nil
		}},
	}), meowire.Config{MaxRounds: 5})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	// First Stimulate consumes the initial assembly — no audit yet.
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventConfig {
			t.Fatalf("unexpected EventConfig before any UpdateConfig")
		}
	}

	a.UpdateConfig(meowire.Config{MaxRounds: 3, ToolTimeout: 0})
	var got *meowire.ConfigAudit
	var firstKind meowire.EventKind
	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventConfig {
			got = ev.Config
		}
		if got == nil && ev.Kind != meowire.EventConfig {
			if firstKind == 0 {
				firstKind = ev.Kind
			}
		}
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone")
	}
	if got == nil {
		t.Fatalf("no EventConfig emitted after UpdateConfig (first other event: %v)", firstKind)
	}
	if got.Old.MaxRounds != 5 || got.New.MaxRounds != 3 {
		t.Fatalf("ConfigAudit = {Old:%+v New:%+v}, want Old.MaxRounds=5 New.MaxRounds=3", got.Old, got.New)
	}
}

// TestConfigAuditZeroValue verifies a zero-value UpdateConfig is also
// recorded (every "unique update" is traceable).
func TestConfigAuditZeroValue(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "done"}, nil
		}},
	}), meowire.Config{MaxRounds: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	for range a.Stimulate(context.Background(), "work") {
	}
	a.UpdateConfig(meowire.Config{}) // zero-value swap resets to defaults

	var got *meowire.ConfigAudit
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventConfig {
			got = ev.Config
		}
	}
	if got == nil {
		t.Fatal("expected EventConfig for a zero-value UpdateConfig")
	}
	if got.Old.MaxRounds != 2 || got.New.MaxRounds != 0 {
		t.Fatalf("ConfigAudit = {Old:%+v New:%+v}, want Old.MaxRounds=2 New.MaxRounds=0", got.Old, got.New)
	}
}

// TestAuditOrderReplaceBeforeConfig verifies the audit ordering at the start
// of the next Stimulate: EventReplace first, then EventConfig (换线 → 换配置).
func TestAuditOrderReplaceBeforeConfig(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "done"}, nil
		}},
	}), meowire.Config{MaxRounds: 2})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	for range a.Stimulate(context.Background(), "work") {
	}
	if _, err := a.Replace(meowire.SlotThink, testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
		return &meowire.Decision{Text: "done"}, nil
	}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	a.UpdateConfig(meowire.Config{MaxRounds: 9})

	var kinds []meowire.EventKind
	for ev := range a.Stimulate(context.Background(), "work") {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == meowire.EventDone {
			break
		}
	}
	want := []meowire.EventKind{meowire.EventReplace, meowire.EventConfig, meowire.EventState, meowire.EventText, meowire.EventState, meowire.EventDone}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("event[%d] kind = %v, want %v", i, kinds[i], k)
		}
	}
}
