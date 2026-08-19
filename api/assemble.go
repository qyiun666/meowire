// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// assemble.go — composition root: the single assembly point.
package meowire

import (
	"cmp"
	"fmt"

	"github.com/qyiun666/meowire/internal/cell"
)

// Organs holds all host-provided ports (all required — no defaults, no stubs).
// Unlike internal packages, which tolerate nil ports defensively, the api
// layer rejects a missing port at assembly time.
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
	Tools    []ToolSpec
	Context  []string
	Identity Identity
}

// Config holds agent configuration.
// Zero-value semantics: MaxRounds<=0 uses DefaultMaxRounds(8);
// MaxToolOutput<=0 disables truncation; MaxRetries<=0 disables retry.
type Config struct {
	MaxRounds     int
	MaxToolOutput int
	MaxRetries    int
}

// New creates a new Agent with the given organs and config.
// All six ports (Think/Act/Closer/Hooks/Sandbox/Budget) are required;
// a missing port returns an error — there are no default implementations.
func New(o Organs, cfg Config) (*Agent, error) {
	required := []struct {
		name string
		ok   bool
	}{
		{"Think", o.Think != nil},
		{"Act", o.Act != nil},
		{"Closer", o.Closer != nil},
		{"Hooks", o.Hooks != nil},
		{"Sandbox", o.Sandbox != nil},
		{"Budget", o.Budget != nil},
	}
	for _, r := range required {
		if !r.ok {
			return nil, fmt.Errorf("meow: required port %s not injected", r.name)
		}
	}

	c := &cell.Cell{
		ID:            cmp.Or(o.ID, "agent"),
		Identity:      o.Identity,
		Think:         o.Think,
		Act:           o.Act,
		Hooks:         o.Hooks,
		Sandbox:       o.Sandbox,
		Budget:        o.Budget,
		MaxRounds:     cfg.MaxRounds,
		MaxToolOutput: cfg.MaxToolOutput,
		MaxRetries:    cfg.MaxRetries,
		System:        o.System,
		Tools:         o.Tools,
		Context:       o.Context,
	}

	return &Agent{cell: c, closer: o.Closer}, nil
}

