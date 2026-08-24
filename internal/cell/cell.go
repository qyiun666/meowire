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
	Think nerve.Thinker
	Act   nerve.Effector
	Hooks   *nerve.Hooks
	Sandbox nerve.Sandbox
	Budget  *nerve.ContextBudget
	// PauseGate returns a fresh pause gate per Stimulate (framework wiring,
	// injected by the api layer; nil = pause unsupported).
	PauseGate func() *nerve.PauseGate

	// Config
	MaxRounds      int
	MaxToolOutput  int
	MaxRetries     int
	ToolTimeout    time.Duration
	ToolMaxRetries int

	// Host-injected fixed parts
	System  string
	Methods []nerve.MethodSpec // Built-in capability description (describes only)
	Tools   []nerve.ToolSpec
	Context []string // Default context (host injected)

	closed atomic.Bool
	wireMu sync.Mutex // guards runtime-swappable ports (Replace)
}

// Stimulate runs the DecisionLoop and returns an event iterator.
// The port references used by this Stimulate are snapshotted under the wire
// lock: a concurrent Replace takes effect at the next Stimulate, never
// mid-flight.
func (c *Cell) Stimulate(ctx context.Context, text string) iter.Seq[nerve.Event] {
	return func(yield func(nerve.Event) bool) {
		closed := c.closed.Load()
		if closed {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: closed")})
			return
		}
		c.wireMu.Lock()
		think, act := c.Think, c.Act
		hooks, sandbox, budget := c.Hooks, c.Sandbox, c.Budget
		pauseGate := c.PauseGate
		c.wireMu.Unlock()
		if think == nil || act == nil {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: nil Think or Act port")})
			return
		}
		lc := &nerve.LoopContext{
			CellID:         c.ID,
			Identity:       c.Identity,
			Methods:        slices.Clone(c.Methods),
			Think:          think,
			Act:            act,
			Hooks:          hooks,
			Sandbox:        sandbox,
			Budget:         budget,
			MaxRounds:      c.MaxRounds,
			MaxToolOutput:  c.MaxToolOutput,
			MaxRetries:     c.MaxRetries,
			ToolTimeout:    c.ToolTimeout,
			ToolMaxRetries: c.ToolMaxRetries,
			State:          nerve.StateIdle,
			Input:          text,
			System:         c.System,
			Tools:          slices.Clone(c.Tools),
			Context:        slices.Clone(c.Context),
		}
		if pauseGate != nil {
			lc.Pause = pauseGate()
		}
		nerve.DecisionLoop{}.Cycle(ctx, lc, yield)
	}
}

// Replace swaps one runtime port; it takes effect at the next Stimulate
// (each Stimulate snapshots the current ports into a fresh LoopContext — an
// in-flight Stimulate keeps the ports it started with). This is the
// dynamic-wiring counterpart of synaptic plasticity: hosts swap organs
// between stimuli (another LLM, a stricter permission policy) without
// rebuilding the agent.
//
// Supported slots and their port types (all required — a swapped port must
// be non-nil, a missing organ cannot be swapped in):
//
//	"think"   → nerve.Thinker
//	"act"     → nerve.Effector
//	"sandbox" → nerve.Sandbox
//	"budget"  → *nerve.ContextBudget
//	"hooks"   → *nerve.Hooks
//
// Closer and PauseGate are not swappable: Closer is a resource binding
// (Close drains it exactly once), PauseGate is framework wiring. Returns the
// previous port value (nil if none was set); a no-op after Close.
func (c *Cell) Replace(slot string, port any) (any, error) {
	if c.closed.Load() {
		return nil, nil
	}
	c.wireMu.Lock()
	defer c.wireMu.Unlock()
	switch slot {
	case "think":
		v, ok := port.(nerve.Thinker)
		if !ok || v == nil {
			return nil, fmt.Errorf("cell.Replace: think: got %T, want non-nil nerve.Thinker", port)
		}
		old := c.Think
		c.Think = v
		return old, nil
	case "act":
		v, ok := port.(nerve.Effector)
		if !ok || v == nil {
			return nil, fmt.Errorf("cell.Replace: act: got %T, want non-nil nerve.Effector", port)
		}
		old := c.Act
		c.Act = v
		return old, nil
	case "sandbox":
		v, ok := port.(nerve.Sandbox)
		if !ok || v == nil {
			return nil, fmt.Errorf("cell.Replace: sandbox: got %T, want non-nil nerve.Sandbox", port)
		}
		old := c.Sandbox
		c.Sandbox = v
		return old, nil
	case "budget":
		v, ok := port.(*nerve.ContextBudget)
		if !ok || v == nil || v.Trimmer == nil || v.MaxTokens <= 0 {
			return nil, fmt.Errorf("cell.Replace: budget: got %T, want a complete non-nil ContextBudget", port)
		}
		old := c.Budget
		c.Budget = v
		return old, nil
	case "hooks":
		v, ok := port.(*nerve.Hooks)
		if !ok || v == nil || !completeHooks(v) {
			return nil, fmt.Errorf("cell.Replace: hooks: got %T, want non-nil Hooks with all eight callbacks", port)
		}
		old := c.Hooks
		c.Hooks = v
		return old, nil
	default:
		return nil, fmt.Errorf("cell.Replace: unknown slot %q", slot)
	}
}

// completeHooks reports whether all eight hook callbacks are non-nil.
func completeHooks(h *nerve.Hooks) bool {
	return h.BeforeStimulate != nil && h.AfterStimulate != nil &&
		h.BeforeThink != nil && h.AfterThink != nil &&
		h.BeforeAct != nil && h.AfterAct != nil &&
		h.OnError != nil && h.OnCycleEnd != nil
}

// Close marks the cell as closed (idempotent).
func (c *Cell) Close() error {
	c.closed.Store(true)
	return nil
}

// IsClosed returns whether the cell is closed.
func (c *Cell) IsClosed() bool {
	return c.closed.Load()
}
