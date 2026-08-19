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
)

// Agent is the facade — the sole entry point for the host.
// cell and closer are set once at construction and never replaced;
// only closed needs synchronization.
type Agent struct {
	cell   *cell.Cell
	closer Closer
	closed atomic.Bool
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

// Close shuts down the agent.
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
