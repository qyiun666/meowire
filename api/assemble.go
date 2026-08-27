// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// assemble.go — composition root: the single assembly point.
package meowire

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/qyiun666/meowire/internal/cell"
	"github.com/qyiun666/meowire/internal/nerve"
)

// Organs holds all host-provided ports (all required — no defaults, no stubs).
// Unlike internal packages, which tolerate nil ports defensively, the api
// layer rejects a missing port at assembly time.
// Organs is carried by Blueprint; hosts write it once and reuse it for every
// Agent instance.
type Organs struct {
	ID      string         // Agent unique identifier (empty = "agent")
	Think   Thinker        // Required
	Act     Effector       // Required
	Closer  Closer         // Required
	Hooks   *Hooks         // Required
	Sandbox Sandbox        // Required
	Budget  *ContextBudget // Required

	// Fixed parts injected into every LoopContext
	System   string
	Methods  []MethodSpec
	Tools    []ToolSpec
	Context  []string
	Identity string
}

// FullHooks returns a copy of h with every nil callback filled by an
// explicit no-op — hosts declare only the hooks they need; the rest become
// declared no-ops (assembly requires all eight callbacks; an explicit no-op
// is a decision, an absent callback is a missing organ).
func FullHooks(h Hooks) *Hooks {
	if h.BeforeStimulate == nil {
		h.BeforeStimulate = func(context.Context, *Prompt) error { return nil }
	}
	if h.AfterStimulate == nil {
		h.AfterStimulate = func(context.Context, string) {}
	}
	if h.BeforeThink == nil {
		h.BeforeThink = func(context.Context, *Prompt) error { return nil }
	}
	if h.AfterThink == nil {
		h.AfterThink = func(context.Context, *Decision) error { return nil }
	}
	if h.BeforeAct == nil {
		h.BeforeAct = func(context.Context, *Action) error { return nil }
	}
	if h.AfterAct == nil {
		h.AfterAct = func(context.Context, *Action, *Effect, error) {}
	}
	if h.OnError == nil {
		h.OnError = func(context.Context, error) {}
	}
	if h.OnCycleEnd == nil {
		h.OnCycleEnd = func(context.Context, string, nerve.CycleOutcome) {}
	}
	return &h
}

// Config holds the scalar loop configuration (alias of the internal
// LoopConfig — the single source of truth for config semantics).
// Zero-value semantics: MaxRounds<=0 uses DefaultMaxRounds(8);
// MaxToolOutput<=0 disables truncation; MaxRetries<=0 disables retry;
// ToolTimeout<=0 disables per-tool timeouts; ToolMaxRetries<=0 disables tool retry.
// UpdateConfig swaps it wholesale at runtime; the next Stimulate/Resume
// snapshots the new values.
type Config = nerve.LoopConfig

// Blueprint is a host assembly blueprint instance: ports + config. Define
// once, New many times — every instance shares the same wiring and is
// validated the same way. Every wiring point is required (no optional
// organs): missing ports and incomplete ports (a ContextBudget without a
// Trimmer) abort New.
type Blueprint struct {
	Organs Organs
	Config Config
}

// New creates a new Agent from a blueprint.
// Assembly is validated against the wiring blueprint: error-level findings
// (missing required ports, incomplete ports) always abort New. Info findings
// never block — hosts surface them via Validate / WiringDiagram /
// RenderDiagram at assembly or test time.
// There are no default implementations and no optional wiring points.
func New(b Blueprint) (*Agent, error) {
	var errs []error
	for _, is := range Validate(b.Organs, b.Config) {
		if is.Level == LevelError {
			errs = append(errs, fmt.Errorf("meow: %s", is.Msg))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	o, cfg := b.Organs, b.Config
	c := &cell.Cell{
		ID:       cmp.Or(o.ID, "agent"),
		Identity: o.Identity,
		Think:    o.Think,
		Act:      o.Act,
		Hooks:    o.Hooks,
		Sandbox:  o.Sandbox,
		Budget:   o.Budget,
		Config:   cfg,
		System:   o.System,
		Methods:  o.Methods,
		Tools:    o.Tools,
		Context:  o.Context,
	}

	a := &Agent{cell: c, closer: o.Closer}
	a.cell.PauseGate = a.pauseGate
	return a, nil
}
