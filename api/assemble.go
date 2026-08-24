// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// assemble.go — composition root: the single assembly point.
package meowire

import (
	"cmp"
	"errors"
	"fmt"
	"time"

	"github.com/qyiun666/meowire/internal/cell"
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

// Config holds agent configuration.
// Zero-value semantics: MaxRounds<=0 uses DefaultMaxRounds(8);
// MaxToolOutput<=0 disables truncation; MaxRetries<=0 disables retry;
// ToolTimeout<=0 disables per-tool timeouts; ToolMaxRetries<=0 disables tool retry.
type Config struct {
	MaxRounds      int
	MaxToolOutput  int
	MaxRetries     int
	ToolTimeout    time.Duration // Per-tool execution timeout (<=0 = none)
	ToolMaxRetries int           // Tool retry count on effector error (<=0 = no retry)
}

// Blueprint is a host assembly blueprint instance: ports + config + optional
// strictness. Define once, New many times — every instance shares the same
// wiring and is validated the same way.
// Strict=true promotes warn-level Validate findings (half-wired hook pairs,
// incomplete memory/plan paths) to New-blocking errors; error-level findings
// (missing required ports) always block.
type Blueprint struct {
	Organs Organs
	Config Config
	Strict bool
}

// New creates a new Agent from a blueprint.
// Assembly is validated against the wiring blueprint: error-level findings
// (missing required ports) always abort New; with Strict=true, warn-level
// findings (half-wired hook pairs, incomplete memory/plan paths) also abort.
// Info findings never block — hosts surface them via Validate /
// WiringDiagram / RenderDiagram at assembly or test time.
// There are no default implementations.
func New(b Blueprint) (*Agent, error) {
	var errs []error
	for _, is := range Validate(b.Organs, b.Config) {
		if is.Level == LevelError || (b.Strict && is.Level == LevelWarn) {
			errs = append(errs, fmt.Errorf("meow: %s", is.Msg))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	o, cfg := b.Organs, b.Config
	c := &cell.Cell{
		ID:             cmp.Or(o.ID, "agent"),
		Identity:       o.Identity,
		Think:          o.Think,
		Act:            o.Act,
		Hooks:          o.Hooks,
		Sandbox:        o.Sandbox,
		Budget:         o.Budget,
		MaxRounds:      cfg.MaxRounds,
		MaxToolOutput:  cfg.MaxToolOutput,
		MaxRetries:     cfg.MaxRetries,
		ToolTimeout:    cfg.ToolTimeout,
		ToolMaxRetries: cfg.ToolMaxRetries,
		System:         o.System,
		Methods:        o.Methods,
		Tools:          o.Tools,
		Context:        o.Context,
	}

	a := &Agent{cell: c, closer: o.Closer}
	a.cell.PauseGate = a.pauseGate
	return a, nil
}
