// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// hook.go — wiring interception points (all required, no optional callbacks).
package nerve

import "context"

// CycleOutcome classifies how a Cycle/Resume invocation ended, delivered by
// OnCycleEnd so hosts never reverse-engineer the outcome from the event
// stream. The zero value is not a valid outcome — the loop always records
// exactly one before settlement.
type CycleOutcome int

const (
	OutcomeDone      CycleOutcome = iota + 1 // completed normally with final output
	OutcomeSuspended                         // suspended for external input; a Resume will continue it
	OutcomeMaxRounds                         // round budget exhausted with pending tool calls (ErrMaxRounds)
	OutcomeError                             // unrecoverable error (EventError)
	OutcomeAborted                           // consumer stopped early (yield returned false) before any terminal
)

// Hooks are the wiring interception points (all eight required — the
// sensory/decision/action regulation loop). Hosts that want no behavior at
// a point pass an explicit no-op; a nil callback fails assembly. An
// explicit no-op is a declared decision; an absent callback is a missing
// organ.
// BeforeStimulate/AfterStimulate fire exactly once per Stimulate:
// BeforeStimulate receives a Prompt prototype whose content fields
// (System/Identity/Methods/Tools/Context/Input/Plan) are written back to the
// loop after the hook returns, so modifications apply to every round of the
// Stimulate (State is loop-managed and not written back); an error terminates
// the whole Stimulate. AfterStimulate runs after the cycle ends — guaranteed
// on normal completion, error path, and early consumer stop (yield=false).
// AfterAct receives the tool execution error (err non-nil = effector failure).
// OnCycleEnd is guaranteed to run exactly once per Cycle on all paths,
// carrying the CycleOutcome classification (Done / Suspended / MaxRounds /
// Error / Aborted).
type Hooks struct {
	BeforeStimulate func(ctx context.Context, p *Prompt) error
	AfterStimulate  func(ctx context.Context, output string)
	BeforeThink     func(ctx context.Context, p *Prompt) error
	AfterThink      func(ctx context.Context, d *Decision) error
	BeforeAct       func(ctx context.Context, a *Action) error
	AfterAct        func(ctx context.Context, a *Action, e *Effect, err error)
	OnError         func(ctx context.Context, err error)
	OnCycleEnd      func(ctx context.Context, output string, outcome CycleOutcome)
}
