// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// colony.go — the cell's half of an agent-to-agent exchange: minting and
// stamping outbound signals, pairing replies back to the round that waited.
package cell

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/qyiun666/meowire/internal/nerve"
)

// emit stamps one outbound signal with what only this cell knows (its id, its
// name, whether it is a fresh request or an answer) and delivers it through the
// colony, reporting the minted id. A request opens a task lifecycle; an answer
// carries the state the loop already decided.
func (c *Cell) emit(ctx context.Context, sig nerve.Signal) (string, error) {
	sig.ID = c.mint()
	sig.From = c.ID
	if sig.ReplyTo == "" {
		sig.Kind = nerve.KindStimulus
		sig.Status = nerve.TaskSubmitted
	} else {
		sig.Kind = nerve.KindResponse
	}
	if err := c.Egress(ctx, sig); err != nil {
		return "", fmt.Errorf("cell: emit %s: %w", sig.ID, err)
	}
	return sig.ID, nil
}

// mint produces this cell's signal ids: the cell name makes them unique across
// the colony, the counter unique within it, and neither needs a clock.
func (c *Cell) mint() string {
	c.colonyMu.Lock()
	defer c.colonyMu.Unlock()
	c.seq++
	return c.ID + "/" + strconv.FormatUint(c.seq, 10)
}

// await records a delegation so an answer can find the round it belongs to even
// after that iterator has ended — the cell is suspended, not running.
func (c *Cell) await(signalID string, call nerve.ToolCall, sess nerve.Session) {
	c.colonyMu.Lock()
	defer c.colonyMu.Unlock()
	if c.delegations == nil {
		c.delegations = make(map[string]nerve.Correlation)
	}
	c.delegations[signalID] = nerve.Correlation{
		SignalID: signalID,
		CellID:   c.ID,
		Call:     call,
		Session:  sess,
	}
}

// ingest is the loop's Think-gap step: pair whatever arrived, then hand the
// rest of the queue to the round.
func (c *Cell) ingest() []nerve.Signal {
	c.take()
	c.colonyMu.Lock()
	defer c.colonyMu.Unlock()
	out := c.inbound
	c.inbound = nil
	return out
}

// Resumptions pairs what has arrived and returns the delegations waiting for a
// host to continue. Pairing is the kernel's bookkeeping; deciding when to
// Resume is not, so a paired answer sits here until the host takes it. The
// snapshot does not consume: Ack(signalID) ends a delegation, which is what
// lets one task report needs-input before it reports completed.
func (c *Cell) Resumptions() []nerve.Correlation {
	c.take()
	c.colonyMu.Lock()
	defer c.colonyMu.Unlock()
	return slices.Clone(c.resumptions)
}

// Ack ends one delegation: the request stops being paired and every resumption
// recorded under it is dropped.
func (c *Cell) Ack(signalID string) {
	c.colonyMu.Lock()
	defer c.colonyMu.Unlock()
	delete(c.delegations, signalID)
	c.resumptions = slices.DeleteFunc(c.resumptions, func(r nerve.Correlation) bool {
		return r.SignalID == signalID
	})
}

// take moves everything off the queue into the inbound buffer, pairing any
// reply that answers a delegation. It stops at the queue's own capacity: a
// signal is left in the channel rather than dropped, so a cell that is not
// running still applies backpressure to its senders instead of absorbing the
// colony in memory.
func (c *Cell) take() {
	c.colonyMu.Lock()
	defer c.colonyMu.Unlock()
	for len(c.inbound) < nerve.InboxCapacity {
		select {
		case s := <-c.inboxChan():
			if c.pairLocked(s) {
				continue
			}
			c.inbound = append(c.inbound, s)
		default:
			return
		}
	}
}

// pairLocked records s against the delegation it answers and reports whether it
// did. colonyMu must be held.
func (c *Cell) pairLocked(s nerve.Signal) bool {
	if s.Kind != nerve.KindResponse || s.ReplyTo == "" {
		return false
	}
	corr, ok := c.delegations[s.ReplyTo]
	if !ok {
		return false // never this cell's, or already acked: surface it as a signal
	}
	corr.Status = s.Status
	corr.Response = s.Payload
	c.resumptions = append(c.resumptions, corr)
	return true
}
