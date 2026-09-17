// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow.go — Agent facade: the sole entry point for the host.
package meowire

import (
	"context"
	"errors"
	"fmt"
	"iter"
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

	paused atomic.Bool // pause request flag (atomic; consumed at gap points)
}

// Swappable port slot names for Replace (dynamic wiring). The blueprint owns
// these values (nerve.WirePoint.Slot); slots_sync_test.go fails on drift.
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
// EventWaitInput or EventPaused event: for a tool suspension (EventWaitInput)
// the external response is injected as the pending tool's structured result
// (a Prompt.ToolResults entry); for a pause suspension (EventPaused) the
// response must be empty — the loop just continues. The suspended round's
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
		// Resuming is the intent to continue — clear any stale pause request
		// so the resumed loop does not suspend again at its first gap point.
		a.paused.Store(false)
		for ev := range a.cell.Resume(ctx, sess, response) {
			if !yield(ev) {
				return
			}
		}
	}
}

// UpdateConfig swaps the scalar loop configuration wholesale (zero-value
// semantics identical to New). It takes effect at the next Stimulate/Resume
// — an in-flight loop keeps the values it started with. Every call records a
// ConfigAudit, emitted as EventConfig at the start of the next
// Stimulate/Resume (the moment the swap takes effect) — the counterpart of
// Replace's EventReplace audit. Pair with GetConfig for read-modify-write
// updates (a zero field resets to its default). Safe for concurrent use; a
// no-op after Close.
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

// Close shuts down the agent; subsequent Stimulate calls fail with
// ErrCellClosed.
func (a *Agent) Close() error {
	if !a.closed.CompareAndSwap(false, true) {
		return nil
	}
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
// Think or before a tool execution): the loop yields EventState(StatePaused)
// + EventPaused with a Session snapshot and ends the iterator normally —
// the host resumes via Resume(sess, "") (the unified suspension-resume
// path, v1.3.2). An in-flight Think/Act is not interrupted. Idempotent and
// safe for concurrent use; a no-op after Close.
func (a *Agent) Pause() {
	if a.closed.Load() {
		return
	}
	a.paused.Store(true)
}

// Unpause clears a pending pause request before it takes effect (the
// counterpart of Pause). Once the loop has honored the pause (EventPaused
// yielded with a Session), clearing the request does not resume it — the
// host must call Resume(sess, "") (v1.3.2 semantics). Idempotent and safe
// for concurrent use; a no-op after Close.
func (a *Agent) Unpause() {
	if a.closed.Load() {
		return
	}
	a.paused.Store(false)
}

// pauseGate builds a PauseGate bound to this Agent's pause state. It is
// injected into the cell at assembly; each Stimulate gets a fresh gate so a
// pause requested mid-Stimulate is honored at the next gap point.
func (a *Agent) pauseGate() *nerve.PauseGate {
	return &nerve.PauseGate{
		IsPaused: a.paused.Load,
	}
}
