// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// hook.go — wiring interception points (all optional, nil = skip).
package nerve

import "context"

// Hooks are wiring interception points (all optional, nil = skip).
// BeforeStimulate/AfterStimulate fire exactly once per Stimulate:
// BeforeStimulate receives a Prompt prototype whose content fields
// (System/Identity/Methods/Tools/Context/Input/Plan) are written back to the
// loop after the hook returns, so modifications apply to every round of the
// Stimulate (State is loop-managed and not written back); an error terminates
// the whole Stimulate. AfterStimulate runs after the cycle ends — guaranteed
// on normal completion, error path, and early consumer stop (yield=false).
// AfterAct receives the tool execution error (err non-nil = effector failure).
// OnCycleEnd is guaranteed to run exactly once per Cycle on all three paths.
type Hooks struct {
	BeforeStimulate func(ctx context.Context, p *Prompt) error
	AfterStimulate  func(ctx context.Context, output string)
	BeforeThink     func(ctx context.Context, p *Prompt) error
	AfterThink      func(ctx context.Context, d *Decision) error
	BeforeAct       func(ctx context.Context, a *Action) error
	AfterAct        func(ctx context.Context, a *Action, e *Effect, err error)
	OnError         func(ctx context.Context, err error)
	OnCycleEnd      func(ctx context.Context, output string)
}
