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

	"github.com/qyiun666/meowire/internal/cell"
)

// Agent is the facade — the sole entry point for the host.
type Agent struct {
	cell   *cell.Cell
	closer Closer
	mu     sync.RWMutex
	closed bool
}

// Stimulate runs the DecisionLoop and returns an event iterator.
// Stopping consumption of the iterator abandons the round; tools at or after
// the stop point do not execute (see doc.go: event stream & resume model).
func (a *Agent) Stimulate(ctx context.Context, text string) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		a.mu.RLock()
		closed := a.closed
		c := a.cell
		a.mu.RUnlock()
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
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
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
