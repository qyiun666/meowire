// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow.go — Agent facade: the sole entry point for the host.
package meowire

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"

	"github.com/qyiun666/meowire/internal/cell"
	"github.com/qyiun666/meowire/internal/nerve"
)

// Agent is the facade — the sole entry point for the host.
// cell and closer are set once at construction and never replaced;
// only closed and the pause state need synchronization.
type Agent struct {
	cell   *cell.Cell
	closer Closer
	closed atomic.Bool

	pauseMu  sync.Mutex
	paused   atomic.Bool
	resumeCh chan struct{} // closed = resumed; recreated on each Pause
}

// Swappable port slot names for Replace (dynamic wiring).
const (
	SlotThink   = "think"
	SlotAct     = "act"
	SlotSandbox = "sandbox"
	SlotBudget  = "budget"
	SlotHooks   = "hooks"
)

// Replace swaps one runtime port (SlotThink/SlotAct/SlotSandbox/SlotBudget/
// SlotHooks). It takes effect at the next Stimulate — each Stimulate builds a
// fresh LoopContext, so an in-flight Stimulate keeps the ports it started
// with. This is the dynamic-wiring counterpart of synaptic plasticity: hosts
// swap organs between stimuli (another LLM, a stricter permission policy)
// without rebuilding the agent. Closer is never swappable (resource
// binding). Safe for concurrent use; a no-op after Close. Returns the
// previous port value (nil if none was set); wrong slot or port type returns
// an error.
func (a *Agent) Replace(slot string, port any) (any, error) {
	if a.closed.Load() {
		return nil, nil
	}
	old, err := a.cell.Replace(slot, port)
	if err != nil {
		return nil, fmt.Errorf("meow: %w", err)
	}
	return old, nil
}

// Stimulate runs the DecisionLoop and returns an event iterator.
// Stopping consumption of the iterator abandons the round; tools at or after
// the stop point do not execute (see doc.go: event stream & resume model).
func (a *Agent) Stimulate(ctx context.Context, text string) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		closed := a.closed.Load()
		c := a.cell
		if closed {
			yield(Event{Kind: EventError, Err: ErrCellClosed})
			return
		}
		for ev := range c.Stimulate(ctx, text) {
			if !yield(ev) {
				return
			}
		}
	}
}

// Resume continues a suspended loop from the Session captured in an
// EventWaitInput event: the external response is injected as the pending
// tool's structured result (a Prompt.ToolResults entry), the suspended round's
// remaining tool calls run first, then the round loop resumes from the
// suspended round — the suspension consumes no extra round and no budget.
// The event stream is isomorphic with Stimulate (same hooks, same
// guarantees, same consumption model); timeouts are host-controlled
// (resume with "[denied: timeout]"). The Session is single-use: resuming it
// twice re-executes the remaining tool calls (host responsibility). Yields
// ErrCellClosed after Close.
func (a *Agent) Resume(ctx context.Context, sess Session, response string) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		if a.closed.Load() {
			yield(Event{Kind: EventError, Err: ErrCellClosed})
			return
		}
		for ev := range a.cell.Resume(ctx, sess, response) {
			if !yield(ev) {
				return
			}
		}
	}
}

// UpdateConfig swaps the scalar loop configuration wholesale (zero-value
// semantics identical to New). It takes effect at the next Stimulate/Resume
// — an in-flight loop keeps the values it started with. Pair with GetConfig
// for read-modify-write updates (a zero field resets to its default). Safe
// for concurrent use; a no-op after Close.
func (a *Agent) UpdateConfig(cfg Config) {
	if a.closed.Load() {
		return
	}
	a.cell.UpdateConfig(cfg)
}

// GetConfig returns the current scalar loop configuration.
func (a *Agent) GetConfig() Config {
	return a.cell.GetConfig()
}

// Close shuts down the agent. Any Stimulate blocked on a pending pause is
// unblocked so its iterator can finish (or be stopped by ctx cancellation);
// subsequent Stimulate calls fail with ErrCellClosed.
func (a *Agent) Close() error {
	if !a.closed.CompareAndSwap(false, true) {
		return nil
	}
	// Unblock any goroutine waiting in waitIfPaused before tearing down.
	a.pauseMu.Lock()
	a.paused.Store(false)
	if a.resumeCh != nil {
		close(a.resumeCh)
		a.resumeCh = nil
	}
	a.pauseMu.Unlock()
	var errs []error
	if err := a.cell.Close(); err != nil {
		errs = append(errs, fmt.Errorf("meow: cell: %w", err))
	}
	if a.closer != nil {
		if err := a.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("meow: closer: %w", err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Pause requests a pause. It takes effect at the next gap point (before a
// Think or before a tool execution); an in-flight Think/Act is not
// interrupted. Idempotent and safe for concurrent use; a no-op after Close.
func (a *Agent) Pause() {
	if a.closed.Load() {
		return
	}
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	if a.closed.Load() { // Close may have completed after the fast-path check
		return
	}
	if a.paused.Swap(true) {
		return // already paused
	}
	a.resumeCh = make(chan struct{})
}

// Unpause clears a pending pause (the counterpart of Pause). Idempotent and
// safe for concurrent use; a no-op after Close. The loop resumes at its next
// gap point. Note: the name changed from Resume in v1.2.1 — Resume now
// continues a suspended loop (see Resume(ctx, sess, response)).
func (a *Agent) Unpause() {
	if a.closed.Load() {
		return
	}
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	if a.closed.Load() { // Close may have completed after the fast-path check
		return
	}
	if !a.paused.Swap(false) {
		return // not paused
	}
	if a.resumeCh != nil {
		close(a.resumeCh)
		a.resumeCh = nil // closed channels are never re-closed
	}
}

// pauseGate builds a PauseGate bound to this Agent's pause state. It is
// injected into the cell at assembly; each Stimulate gets a fresh gate so a
// pause requested mid-Stimulate is honored at the next gap point.
func (a *Agent) pauseGate() *nerve.PauseGate {
	return &nerve.PauseGate{
		IsPaused: a.paused.Load,
		ResumeCh: func() <-chan struct{} {
			a.pauseMu.Lock()
			defer a.pauseMu.Unlock()
			return a.resumeCh
		},
	}
}
