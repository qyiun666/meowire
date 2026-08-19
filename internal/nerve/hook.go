// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// hook.go — wiring interception points (all optional, nil = skip).
package nerve

import "context"

// Hooks are wiring interception points (all optional, nil = skip).
// OnCycleEnd is guaranteed to run exactly once per Cycle — on normal
// completion, error path, and early consumer stop (yield=false).
type Hooks struct {
	BeforeThink func(ctx context.Context, p *Prompt) error
	AfterThink  func(ctx context.Context, d *Decision) error
	BeforeAct   func(ctx context.Context, a *Action) error
	AfterAct    func(ctx context.Context, a *Action, e *Effect)
	OnError     func(ctx context.Context, err error)
	OnCycleEnd  func(ctx context.Context, output string)
}
