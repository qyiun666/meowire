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
	Mem     Memory         // Required

	// Colony is the delivery organ a peer request or answer goes through
	// (optional, unlike the seven ports): without it a cell can still be sent
	// signals, it just cannot delegate to a neighbour or answer one. Wire it to
	// the same Synapse the colony's routing table was resolved from.
	Colony Colony

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

// New creates a new Agent from a blueprint. Assembly runs in three steps:
// validate against the wiring blueprint (error-level findings — missing or
// incomplete ports — always abort), Boot every organ that declares the
// capability (in PortOrder; a failure aborts and releases what the attempt
// opened through the host's Closer), then construct the cell. Info findings
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
	if err := bootOrgans(o); err != nil {
		return nil, err
	}
	c := &cell.Cell{
		ID:       cmp.Or(o.ID, "agent"),
		Identity: o.Identity,
		Think:    o.Think,
		Act:      o.Act,
		Hooks:    o.Hooks,
		Sandbox:  o.Sandbox,
		Budget:   o.Budget,
		Mem:      o.Mem,
		Config:   cfg,
		System:   o.System,
		Methods:  o.Methods,
		Tools:    o.Tools,
		Context:  o.Context,
	}

	a := &Agent{cell: c, closer: o.Closer}
	a.cell.PauseGate = a.pauseGate
	if o.Colony != nil {
		a.cell.Egress = o.Colony.Fire
	}
	return a, nil
}

// organRef names one assembled host port, in the order the framework brings
// the ports up.
type organRef struct {
	name  string
	organ any
}

// hostOrgans lists every required port in boot order. The order is the
// blueprint's (nerve.PortOrder) and a test pins the two against each other, so
// a port added to the blueprint has to be added here as well — otherwise its
// Boot would be skipped without a word.
func hostOrgans(o Organs) []organRef {
	return []organRef{
		{"Think", o.Think},
		{"Act", o.Act},
		{"Closer", o.Closer},
		{"Hooks", o.Hooks},
		{"Sandbox", o.Sandbox},
		{"Budget", o.Budget},
		{"Mem", o.Mem},
	}
}

// bootOrgans brings up every organ that declared the lifecycle capability, in
// port order, before an agent exists. The first failure aborts assembly, and
// what the attempt opened is released through the host's Closer — the one
// cleanup channel the framework knows, since an organ is not a resource owner.
func bootOrgans(o Organs) error {
	for _, ref := range hostOrgans(o) {
		b, ok := ref.organ.(Bootable)
		if !ok {
			continue
		}
		if err := b.Boot(context.Background()); err != nil {
			return errors.Join(
				fmt.Errorf("meow: boot %s: %w", ref.name, err),
				cleanupAfterBoot(o),
			)
		}
	}
	return nil
}

// cleanupAfterBoot reports the failed assembly's cleanup, including the case
// where the cleanup itself failed — a resource left open is never folded into
// the boot error silently.
func cleanupAfterBoot(o Organs) error {
	if err := o.Closer.Close(); err != nil {
		return fmt.Errorf("meow: cleanup after a failed boot: %w", err)
	}
	return nil
}
