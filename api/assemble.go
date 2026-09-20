// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// assemble.go — composition root: the single assembly point.
package meowire

import (
	"context"
	"errors"
	"fmt"

	"github.com/qyiun666/meowire/internal/brain"
	"github.com/qyiun666/meowire/internal/cell"
	"github.com/qyiun666/meowire/internal/nerve"
)

// Organs holds the host-provided ports (all required — no stubs) plus the
// parameters of the bundled brain. Unlike internal packages, which tolerate
// nil ports defensively, the api layer rejects a missing port at assembly
// time.
// The wiring is carried by Blueprint and written once; the agent's name is not
// — every instance the host creates declares its own ID, which is what the
// events it emits and the suspension handles it produces are attributed to.
type Organs struct {
	// ID names this agent: every event carries it, and a Session is only valid
	// against the ID that produced it. It must be non-empty (New rejects an
	// unnamed agent — a default shared by every instance would attribute one
	// agent's events and handles to another) and unique among the agents a host
	// runs at once. This is the one Organs field to vary per New.
	ID string
	// Brain parameterizes the bundled brain — the one organ the framework
	// ships. A host passes model, key and transport switches here and never
	// implements a Thinker; BaseURL aims it at any OpenAI-compatible endpoint.
	// Required: Validate rejects an empty Model or Key.
	Brain   BrainConfig
	Act     Effector       // Required
	Closer  Closer         // Required
	Hooks   *Hooks         // Required
	Sandbox Sandbox        // Required
	Budget  *ContextBudget // Required
	Mem     Memory         // Required

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
// MaxToolOutput<=0 disables truncation; MaxRetries<=0 disables Think retry;
// ToolTimeout<=0 disables per-tool timeouts; ToolMaxRetries<=0 disables tool
// retry; MaxParallelActs<=0 runs a whole parallel batch at once.
// UpdateConfig swaps it wholesale at runtime; the next Stimulate/Resume
// snapshots the new values.
type Config = nerve.LoopConfig

// Blueprint is a host assembly blueprint instance: ports + config. Define the
// wiring once, New many times — every instance shares it and is validated the
// same way, with the one exception of Organs.ID, which names the instance and
// so must differ per New. Every wiring point is required (no optional
// organs): missing ports, an incomplete port (a ContextBudget without a
// Trimmer) and an unnamed agent abort New.
type Blueprint struct {
	Organs Organs
	Config Config
}

// New creates a new Agent from a blueprint. Assembly runs in three steps:
// validate against the wiring blueprint (error-level findings — missing or
// incomplete ports, an unnamed agent — always abort), Boot every organ that
// declares the capability (in PortOrder; a failure aborts and releases what the
// attempt opened through the host's Closer), then construct the cell. Info
// findings never block — hosts surface them via Validate / WiringDiagram /
// RenderDiagram at assembly or test time.
// There are no default implementations and no optional wiring points, and no
// default name: two agents sharing one Organs.ID is two agents claiming each
// other's events and suspension handles.
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
	thinker := brain.New(o.Brain)
	if err := bootOrgans(o); err != nil {
		return nil, err
	}
	c := &cell.Cell{
		ID:       o.ID,
		Identity: o.Identity,
		Think:    thinker,
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
	return a, nil
}

// organRef names one assembled host port, in the order the framework brings
// the ports up.
type organRef struct {
	name  string
	organ any
}

// hostOrgans lists every required host port in boot order. The order is the
// blueprint's (nerve.PortOrder) and a test pins the two against each other, so
// a port added to the blueprint has to be added here as well — otherwise its
// Boot would be skipped without a word. The brain is absent on purpose: the
// composition root constructs it from Organs.Brain and it declares no Boot
// (the SDK client does no network handshake).
func hostOrgans(o Organs) []organRef {
	return []organRef{
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
