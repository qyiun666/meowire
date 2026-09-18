// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// hooks.go — hook adapters and terminal error emission shared by every loop path.
package nerve

import (
	"context"
	"fmt"
)

// hookBeforeThink calls Hooks.BeforeThink (required).
func hookBeforeThink(ctx context.Context, lc *LoopContext, p *Prompt) error {
	if err := lc.Hooks.BeforeThink(ctx, p); err != nil {
		return fmt.Errorf("nerve.hookBeforeThink: %w", err)
	}
	return nil
}

// hookAfterThink calls Hooks.AfterThink (required).
func hookAfterThink(ctx context.Context, lc *LoopContext, d *Decision) error {
	if err := lc.Hooks.AfterThink(ctx, d); err != nil {
		return fmt.Errorf("nerve.hookAfterThink: %w", err)
	}
	return nil
}

// hookBeforeAct calls Hooks.BeforeAct (required).
func hookBeforeAct(ctx context.Context, lc *LoopContext, a *Action) error {
	if err := lc.Hooks.BeforeAct(ctx, a); err != nil {
		return fmt.Errorf("nerve.hookBeforeAct: %w", err)
	}
	return nil
}

// hookBeforeStimulate calls Hooks.BeforeStimulate (required).
// The hook receives a Prompt prototype; its content fields are written back
// to the LoopContext after the call, so the modifications apply to every
// round of the Stimulate (State and Bounds are framework-managed and not
// written back — Bounds carries the Sandbox snapshot read-only).
func hookBeforeStimulate(ctx context.Context, lc *LoopContext) error {
	proto := &Prompt{
		System:   lc.System,
		Identity: lc.Identity,
		Methods:  lc.Methods,
		Tools:    lc.Tools,
		// Shallow copy: the prototype gets its own backing array so that
		// in-place mutations (or an early hook error) never leak into lc.
		// The write-back below then adopts the prototype's slice wholesale.
		// ToolResults is copied likewise but is read-only: hosts inject
		// history via Context, the structured track stays framework-managed.
		Context:     append([]string(nil), lc.Context...),
		ToolResults: append([]ToolResult(nil), lc.ToolResults...),
		Bounds:      lc.Bounds,
		Input:       lc.Input,
		Plan:        lc.Plan,
		Reflection:  lc.Reflection,
	}
	if err := lc.Hooks.BeforeStimulate(ctx, proto); err != nil {
		return fmt.Errorf("nerve.hookBeforeStimulate: %w", err)
	}
	// Write back the content fields the hook may have changed.
	lc.System = proto.System
	lc.Identity = proto.Identity
	lc.Methods = proto.Methods
	lc.Tools = proto.Tools
	lc.Context = proto.Context
	lc.Input = proto.Input
	lc.Plan = proto.Plan
	lc.Reflection = proto.Reflection
	return nil
}

// emitError sets the error state, yields EventState(StateError) + EventError,
// then calls the OnError hook (only when events are delivered normally;
// a consumer abort does not trigger OnError).
// OnCycleEnd is guaranteed by Cycle's defer.
// NOTE: callers always return immediately after calling emitError.
func emitError(ctx context.Context, lc *LoopContext, yield func(Event) bool, err error) {
	lc.endWith(OutcomeError) // first-wins: keeps a prior MaxRounds mark
	lc.State = StateError
	if !yield(Event{Kind: EventState, State: StateError}) {
		return
	}
	if !yield(Event{Kind: EventError, Err: err}) {
		return
	}
	lc.Hooks.OnError(ctx, err)
}
