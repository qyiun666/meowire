// cell.go — Cell: minimal agent kernel (ID + ports + DecisionLoop).
package cell

import (
	"context"
	"fmt"
	"iter"
	"sync"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Cell is the minimal agent kernel: ID + ports + DecisionLoop.
type Cell struct {
	ID       string
	Identity nerve.Identity

	// Required ports
	Think nerve.Thinker
	Act   nerve.Effector

	// Optional ports
	Hooks   *nerve.Hooks
	Sandbox nerve.Sandbox
	Budget  *nerve.ContextBudget

	// Config
	MaxRounds     int
	MaxToolOutput int
	MaxRetries    int

	// Host-injected fixed parts
	System  string
	Tools   []nerve.ToolSpec
	Context []string // Default context (host injected)

	mu     sync.RWMutex
	closed bool
}

// Stimulate runs the DecisionLoop and returns an event iterator.
func (c *Cell) Stimulate(ctx context.Context, text string) iter.Seq[nerve.Event] {
	return func(yield func(nerve.Event) bool) {
		c.mu.RLock()
		closed := c.closed
		c.mu.RUnlock()
		if closed {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: closed")})
			return
		}
		if c.Think == nil || c.Act == nil {
			yield(nerve.Event{Kind: nerve.EventError, Err: fmt.Errorf("cell: nil Think or Act port")})
			return
		}
		lc := &nerve.LoopContext{
			CellID:        c.ID,
			Identity:      c.Identity,
			Think:         c.Think,
			Act:           c.Act,
			Hooks:         c.Hooks,
			Sandbox:       c.Sandbox,
			Budget:        c.Budget,
			MaxRounds:     c.MaxRounds,
			MaxToolOutput: c.MaxToolOutput,
			MaxRetries:    c.MaxRetries,
			State:         nerve.StateIdle,
			Input:         text,
			System:        c.System,
			Tools:         append([]nerve.ToolSpec(nil), c.Tools...), // copy
			Context:       append([]string(nil), c.Context...),       // copy
		}
		nerve.DecisionLoop{}.Cycle(ctx, lc, yield)
	}
}

// Close marks the cell as closed (idempotent).
func (c *Cell) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return nil
}

// IsClosed returns whether the cell is closed.
func (c *Cell) IsClosed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.closed
}
