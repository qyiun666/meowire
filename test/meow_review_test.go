// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_review_test.go — port wiring and event sequence tests.
package meowire_test

import (
	"context"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// TestPortWiring verifies hooks are invoked during Stimulate.
func TestPortWiring(t *testing.T) {
	var hookCalled bool
	a, err := meowire.New(testOrgans(meowire.Organs{
		Hooks: &meowire.Hooks{
			OnCycleEnd: func(ctx context.Context, output string) {
				hookCalled = true
			},
		},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for range a.Stimulate(context.Background(), "work") {
	}
	if !hookCalled {
		t.Fatal("OnCycleEnd hook not invoked")
	}
}

// TestStimulateEventSequence verifies full event sequence: State→Text→Done.
func TestStimulateEventSequence(t *testing.T) {
	a, err := meowire.New(testOrgans(meowire.Organs{
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "final-output"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "input") {
		events = append(events, ev)
	}

	want := []struct {
		kind meowire.EventKind
		text string
	}{
		{meowire.EventState, ""},
		{meowire.EventText, "final-output"},
		{meowire.EventState, ""},
		{meowire.EventDone, ""},
	}

	if len(events) != len(want) {
		t.Fatalf("events count = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		if events[i].Kind != w.kind {
			t.Fatalf("events[%d].Kind = %d, want %d", i, events[i].Kind, w.kind)
		}
	}
	if events[1].Text != "final-output" {
		t.Fatalf("events[1].Text = %q, want %q", events[1].Text, "final-output")
	}
	if events[3].Output != "final-output" {
		t.Fatalf("events[3].Output = %q, want %q", events[3].Output, "final-output")
	}
}

// TestStimulateBoundaryHooks verifies BeforeStimulate/AfterStimulate fire
// exactly once per Stimulate with the input text and final output.
func TestStimulateBoundaryHooks(t *testing.T) {
	var beforeCalls, afterCalls int
	var gotText, gotOutput string
	a, err := meowire.New(testOrgans(meowire.Organs{
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "final-output"}, nil
		}},
		Hooks: &meowire.Hooks{
			BeforeStimulate: func(ctx context.Context, p *meowire.Prompt) error {
				beforeCalls++
				gotText = p.Input
				return nil
			},
			AfterStimulate: func(ctx context.Context, output string) {
				afterCalls++
				gotOutput = output
			},
		},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for range a.Stimulate(context.Background(), "input-text") {
	}
	if beforeCalls != 1 {
		t.Fatalf("BeforeStimulate calls = %d, want 1", beforeCalls)
	}
	if afterCalls != 1 {
		t.Fatalf("AfterStimulate calls = %d, want 1", afterCalls)
	}
	if gotText != "input-text" {
		t.Fatalf("BeforeStimulate text = %q, want %q", gotText, "input-text")
	}
	if gotOutput != "final-output" {
		t.Fatalf("AfterStimulate output = %q, want %q", gotOutput, "final-output")
	}
}
