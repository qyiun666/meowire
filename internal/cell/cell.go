// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// cell.go — Cell: minimal agent kernel (ID + ports + DecisionLoop).
package cell

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Cell is the minimal agent kernel: ID + ports + DecisionLoop.
type Cell struct {
	ID       string
	Identity string

	// Required ports (all organs must be present; api assembly enforces)
	Think   nerve.Thinker
	Act     nerve.Effector
	Hooks   *nerve.Hooks
	Sandbox nerve.Sandbox
	Budget  *nerve.ContextBudget
	Mem     nerve.Memory
	// PauseGate returns a fresh pause gate per Stimulate (framework wiring,
	// injected by the api layer; nil = pause unsupported).
	PauseGate func() *nerve.PauseGate

	// Config is the scalar runtime configuration, swapped wholesale by
	// UpdateConfig; each Stimulate/Resume snapshots the current values.
	Config nerve.LoopConfig

	// Host-injected fixed parts
	System  string
	Methods []nerve.MethodSpec // Built-in capability description (describes only)
	Tools   []nerve.ToolSpec
	Context []string // Default context (host injected)

	closed atomic.Bool
	seq    atomic.Uint64 // emission order of every event this cell lets out
	wireMu sync.Mutex    // guards runtime-swappable ports, Config, and pendingReplace
	// pendingReplace: port swap audits recorded by Replace, drained into the
	// LoopContext of the next Stimulate/Resume (the moment the swap takes
	// effect) and emitted as EventReplace.
	pendingReplace []nerve.ReplaceAudit
	// pendingConfig: config swap audits recorded by UpdateConfig, drained
	// into the LoopContext of the next Stimulate/Resume (the moment the swap
	// takes effect) and emitted as EventConfig.
	pendingConfig []nerve.ConfigAudit
}

// Stimulate runs the DecisionLoop and returns an event iterator.
// The port references used by this Stimulate are snapshotted under the wire
// lock: a concurrent Replace takes effect at the next Stimulate, never
// mid-flight. Pending Replace audits are drained into this Stimulate's
// LoopContext and emitted as EventReplace before any other event.
func (c *Cell) Stimulate(ctx context.Context, text string) iter.Seq[nerve.Event] {
	return func(yield func(nerve.Event) bool) {
		yield = c.authored(yield)
		if c.IsClosed() {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: stimulate: %w", nerve.ErrCellClosed)})
			return
		}
		lc := c.snapshot(text)
		if lc == nil {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: nil Think or Act port")})
			return
		}
		nerve.DecisionLoop{}.Cycle(ctx, lc, yield)
	}
}

// Resume continues a suspended loop from a Session captured in
// EventWaitInput; the event stream is isomorphic with Stimulate. The port
// snapshot, pending Replace audits, and config are taken exactly like
// Stimulate — a concurrent Replace/UpdateConfig takes effect here, never
// mid-flight.
func (c *Cell) Resume(ctx context.Context, sess nerve.Session, resp nerve.Response) iter.Seq[nerve.Event] {
	return func(yield func(nerve.Event) bool) {
		yield = c.authored(yield)
		if c.IsClosed() {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: resume: %w", nerve.ErrCellClosed)})
			return
		}
		lc := c.snapshot("")
		if lc == nil {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: nil Think or Act port")})
			return
		}
		nerve.DecisionLoop{}.Resume(ctx, lc, sess, resp, yield)
	}
}

// authored returns a yield that stamps every event with the facts of its
// passage through this cell: which cell spoke, in what order, and when. The
// loop yields from many places; these three belong to the boundary, so one
// writer owns them and an event keeps them once it is journaled, replayed in
// another process, or merged with another agent's stream.
func (c *Cell) authored(yield func(nerve.Event) bool) func(nerve.Event) bool {
	return func(e nerve.Event) bool {
		e.CellID = c.ID
		e.Seq = c.seq.Add(1)
		e.TS = time.Now().UnixMilli()
		return yield(e)
	}
}

// snapshot snapshots the current ports, config, and pending Replace audits
// under the wire lock and builds a fresh LoopContext; the audits are drained
// so they are emitted at the start of this Stimulate/Resume (the moment the
// swaps take effect). Returns nil when a required port is missing.
func (c *Cell) snapshot(text string) *nerve.LoopContext {
	c.wireMu.Lock()
	think, act := c.Think, c.Act
	hooks, sandbox, budget := c.Hooks, c.Sandbox, c.Budget
	mem := c.Mem
	pauseGate := c.PauseGate
	cfg := c.Config
	pendingReplace := c.pendingReplace
	c.pendingReplace = nil
	pendingConfig := c.pendingConfig
	c.pendingConfig = nil
	c.wireMu.Unlock()
	if think == nil || act == nil {
		return nil
	}
	lc := &nerve.LoopContext{
		CellID:          c.ID,
		Identity:        c.Identity,
		Methods:         slices.Clone(c.Methods),
		Think:           think,
		Act:             act,
		Hooks:           hooks,
		Sandbox:         sandbox,
		Budget:          budget,
		Mem:             mem,
		MaxRounds:       cfg.MaxRounds,
		MaxToolOutput:   cfg.MaxToolOutput,
		MaxRetries:      cfg.MaxRetries,
		ToolTimeout:     cfg.ToolTimeout,
		ToolMaxRetries:  cfg.ToolMaxRetries,
		ParallelActs:    cfg.ParallelActs,
		MaxParallelActs: cfg.MaxParallelActs,
		State:           nerve.StateIdle,
		Input:           text,
		System:          c.System,
		Tools:           slices.Clone(c.Tools),
		Context:         slices.Clone(c.Context),
		PendingReplace:  pendingReplace,
		PendingConfig:   pendingConfig,
	}
	if pauseGate != nil {
		lc.Pause = pauseGate()
	}
	return lc
}

// UpdateConfig swaps the scalar loop configuration wholesale (zero-value
// semantics identical to New). It takes effect at the next Stimulate/Resume
// (each snapshots the config into a fresh LoopContext — an in-flight loop
// keeps the values it started with). Every call records a ConfigAudit,
// emitted as EventConfig at the start of the next Stimulate/Resume (the
// moment the swap takes effect) — the counterpart of Replace's EventReplace
// audit. Safe for concurrent use; a no-op after Close.
func (c *Cell) UpdateConfig(cfg nerve.LoopConfig) {
	if c.IsClosed() {
		return
	}
	c.wireMu.Lock()
	defer c.wireMu.Unlock()
	old := c.Config
	c.Config = cfg
	c.pendingConfig = append(c.pendingConfig, nerve.ConfigAudit{CellID: c.ID, Old: old, New: cfg})
}

// GetConfig returns the current scalar loop configuration.
func (c *Cell) GetConfig() nerve.LoopConfig {
	c.wireMu.Lock()
	defer c.wireMu.Unlock()
	return c.Config
}

// swapSpec is one swappable slot: how a port is validated and where the Cell
// keeps it. The slot names here must match nerve.SwappableSlots() — a drift
// would let a host swap an organ the blueprint does not expose (or refuse one
// it does), and TestSlotTableMatchesBlueprint fails when that happens.
type swapSpec struct {
	assert func(any) (any, error)
	load   func(*Cell) any
	store  func(*Cell, any)
}

// wantPort asserts one port interface and rejects both a wrong type and a nil
// implementation of the right type.
func wantPort[T any](slot, want string, port any) (T, error) {
	var zero T
	v, ok := port.(T)
	if !ok || any(v) == any(zero) {
		return zero, fmt.Errorf("cell.Replace: %s: got %T, want %s", slot, port, want)
	}
	return v, nil
}

var swapSlots = map[string]swapSpec{
	"act": {
		assert: func(p any) (any, error) { return wantPort[nerve.Effector]("act", "non-nil nerve.Effector", p) },
		load:   func(c *Cell) any { return c.Act },
		store:  func(c *Cell, v any) { c.Act = v.(nerve.Effector) },
	},
	"sandbox": {
		assert: func(p any) (any, error) { return wantPort[nerve.Sandbox]("sandbox", "non-nil nerve.Sandbox", p) },
		load:   func(c *Cell) any { return c.Sandbox },
		store:  func(c *Cell, v any) { c.Sandbox = v.(nerve.Sandbox) },
	},
	"budget": {
		assert: assertBudget,
		load:   func(c *Cell) any { return c.Budget },
		store:  func(c *Cell, v any) { c.Budget = v.(*nerve.ContextBudget) },
	},
	"mem": {
		assert: func(p any) (any, error) { return wantPort[nerve.Memory]("mem", "non-nil nerve.Memory", p) },
		load:   func(c *Cell) any { return c.Mem },
		store:  func(c *Cell, v any) { c.Mem = v.(nerve.Memory) },
	},
	"hooks": {
		assert: assertHooks,
		load:   func(c *Cell) any { return c.Hooks },
		store:  func(c *Cell, v any) { c.Hooks = v.(*nerve.Hooks) },
	},
}

// assertBudget rejects a regulator that cannot regulate: both tracks need a
// trimmer and the limit must be positive.
func assertBudget(p any) (any, error) {
	b, ok := p.(*nerve.ContextBudget)
	if !ok || !nerve.CompleteBudget(b) {
		return nil, fmt.Errorf("cell.Replace: budget: got %T, want a complete non-nil ContextBudget (Trimmer, TrimResults, MaxTokens > 0)", p)
	}
	return b, nil
}

// assertHooks rejects a container missing any of the eight callbacks.
func assertHooks(p any) (any, error) {
	h, ok := p.(*nerve.Hooks)
	if !ok || h == nil || !completeHooks(h) {
		return nil, fmt.Errorf("cell.Replace: hooks: got %T, want non-nil Hooks with all eight callbacks", p)
	}
	return h, nil
}

// Replace swaps one runtime port; it takes effect at the next Stimulate because
// each Stimulate snapshots the ports into a fresh LoopContext (an in-flight one
// keeps what it started with). A swapped port must be non-nil and complete: the
// loop dereferences Sandbox/Budget without a nil check, so an absent organ
// would panic rather than fail closed — removing an organ means swapping in an
// inert one, not nil. Closer is never swappable (it is the resource binding Close
// drains once) and PauseGate is framework wiring. Returns the previous port
// value (nil if none was set); a closed cell refuses the swap, because it would
// never be read.
func (c *Cell) Replace(slot string, port any) (any, error) {
	v, err := checkSwap(slot, port)
	if err != nil {
		return nil, err
	}
	c.wireMu.Lock()
	defer c.wireMu.Unlock()
	if c.IsClosed() {
		return nil, fmt.Errorf("cell.Replace: cell %s: %w", c.ID, nerve.ErrCellClosed)
	}
	spec := swapSlots[slot]
	old := spec.load(c)
	spec.store(c, v)
	c.recordReplace(slot, old, v)
	return old, nil
}

// CheckSwap reports whether a slot would accept this port, without committing.
// A caller that must bring the organ up before wiring it (api's Replace Boots a
// Bootable port first) asks here, so a mistyped slot name never costs an organ
// its one startup.
func (c *Cell) CheckSwap(slot string, port any) error {
	_, err := checkSwap(slot, port)
	return err
}

// checkSwap resolves a slot to the port value it accepts.
func checkSwap(slot string, port any) (any, error) {
	spec, ok := swapSlots[slot]
	if !ok {
		return nil, fmt.Errorf("cell.Replace: unknown slot %q", slot)
	}
	return spec.assert(port)
}

// recordReplace appends one ReplaceAudit for a successful swap; the audit is
// emitted as EventReplace at the start of the next Stimulate/Resume (the
// moment the swap takes effect). wireMu must be held by the caller.
func (c *Cell) recordReplace(slot string, old, new any) {
	c.pendingReplace = append(c.pendingReplace, nerve.ReplaceAudit{
		CellID:  c.ID,
		Slot:    slot,
		OldType: portTypeName(old),
		NewType: portTypeName(new),
	})
}

// portTypeName names a port by its Go type. The audit record outlives the swap
// and is written to a log, so the value itself is not what it can carry; the
// host that wants the displaced organ still has it (`Replace` returns it).
func portTypeName(port any) string {
	if port == nil {
		return ""
	}
	return fmt.Sprintf("%T", port)
}

// completeHooks reports whether all eight hook callbacks are non-nil.
func completeHooks(h *nerve.Hooks) bool {
	return h.BeforeStimulate != nil && h.AfterStimulate != nil &&
		h.BeforeThink != nil && h.AfterThink != nil &&
		h.BeforeAct != nil && h.AfterAct != nil &&
		h.OnError != nil && h.OnCycleEnd != nil
}

// Close marks the cell as closed and reports whether this call performed the
// transition — the facade runs the host Closer exactly once, on that answer.
// Idempotent and safe for concurrent use.
func (c *Cell) Close() bool {
	return c.closed.CompareAndSwap(false, true)
}

// IsClosed reports whether the cell has been closed. Every refusal an actor
// asks of a closed cell carries nerve.ErrCellClosed, so the host matches the
// condition by identity rather than by the wording of one call site.
func (c *Cell) IsClosed() bool { return c.closed.Load() }
