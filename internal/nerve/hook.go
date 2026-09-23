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

// cycleOutcomeNames is the name of each outcome, indexed by value. Slot 0 is
// empty because the zero value is not an outcome: it means no terminal point
// was reached. A guard test pins the table against the last constant, so an
// outcome added to the iota without a name is caught at build time.
var cycleOutcomeNames = []string{
	"", "done", "suspended", "max_rounds", "error", "aborted",
}

// String returns the outcome's name; the zero value and any value outside the
// table read as "unknown" rather than being guessed at.
func (o CycleOutcome) String() string {
	if name := nameOf(cycleOutcomeNames, o); name != "" {
		return name
	}
	return "unknown"
}

// Hooks are the wiring interception points; all eight are required. An
// explicit no-op is a declared decision, an absent callback a missing organ —
// so a nil callback fails assembly.
// BeforeStimulate fires once before any event and receives a Prompt prototype
// whose content fields are written back, applying to every round of the
// Stimulate; an error terminates the whole Stimulate. AfterStimulate and
// OnCycleEnd each run exactly once per
// Stimulate on all paths (normal completion, error, suspension, consumer
// stop); OnCycleEnd carries the CycleOutcome classification. AfterAct receives
// the tool execution error (err non-nil = effector failure).
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
