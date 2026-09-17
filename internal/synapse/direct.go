// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// direct.go — Direct: a connection-table synapse with Resolver-based delivery.
package synapse

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Synapse errors.
var (
	ErrNoTarget    = fmt.Errorf("synapse: target not found")
	ErrNotLinked   = fmt.Errorf("synapse: not linked")
	ErrTargetBusy  = fmt.Errorf("synapse: target inbox full")
	ErrWeakSynapse = fmt.Errorf("synapse: weight below conduction floor")
)

// Resolver resolves a target ID to its signal inbox (host-injected closure,
// keeps synapse decoupled from cell).
type Resolver func(id string) (chan<- nerve.Signal, bool)

// DirectConfig configures a Direct graph at construction.
type DirectConfig struct {
	// Resolver delivers by target ID; may be nil until SetResolver is called
	// at assembly time.
	Resolver Resolver
	// Floor is the conduction threshold: a connection weighing less than this
	// refuses to carry a signal (ErrWeakSynapse) while staying part of the
	// graph. Zero switches gating off — strength alone never decides
	// connectivity unless the host says so. This is not Prune's threshold:
	// the floor stops traffic, weightFloor deletes the connection.
	Floor float64
	// Initial restores a previously exported graph (an Edges snapshot from an
	// earlier run); omitted = empty graph.
	Initial []Edge
}

// Direct is a direct-connection synapse: a from→to edge table with weights,
// delivery counts and a Resolver closure for delivery.
type Direct struct {
	mu      sync.RWMutex
	links   map[string]map[string]Edge
	resolve Resolver
	floor   float64
}

// NewDirect creates a Direct synapse from its config.
func NewDirect(cfg DirectConfig) *Direct {
	d := &Direct{links: make(map[string]map[string]Edge), resolve: cfg.Resolver, floor: max(cfg.Floor, 0)}
	for _, e := range cfg.Initial {
		e.Weight = max(e.Weight, 0) // floor 0, same clamp as Link/Reinforce
		if d.links[e.From] == nil {
			d.links[e.From] = make(map[string]Edge)
		}
		d.links[e.From][e.To] = e
	}
	return d
}

// Floor reports the conduction threshold this graph gates Fire by (0 = off).
func (d *Direct) Floor() float64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.floor
}

// SetResolver injects the Resolver (composition-root assembly).
func (d *Direct) SetResolver(r Resolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolve = r
}

// Link establishes or updates a from→to connection with the given initial
// strength (synaptogenesis; idempotent — re-Link overwrites the weight;
// negative weight clamps to 0; rejects a cancelled ctx).
func (d *Direct) Link(ctx context.Context, from, to string, weight float64) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("synapse.Direct.Link: %w", ctx.Err())
	default:
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.links[from] == nil {
		d.links[from] = make(map[string]Edge)
	}
	d.links[from][to] = Edge{From: from, To: to, Weight: max(weight, 0)}
	return nil
}

// Unlink severs a from→to connection (synapse elimination). A missing
// connection returns ErrNotLinked.
func (d *Direct) Unlink(ctx context.Context, from, to string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("synapse.Direct.Unlink: %w", ctx.Err())
	default:
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	toSet, ok := d.links[from]
	if !ok {
		return fmt.Errorf("synapse.Direct.Unlink: %w: %s -> %s", ErrNotLinked, from, to)
	}
	if _, ok := toSet[to]; !ok {
		return fmt.Errorf("synapse.Direct.Unlink: %w: %s -> %s", ErrNotLinked, from, to)
	}
	delete(toSet, to)
	if len(toSet) == 0 {
		delete(d.links, from) // drop empty rows
	}
	return nil
}

// Reinforce adjusts a connection's strength by delta (LTP if positive, LTD
// if negative); the result never drops below 0. A missing connection returns
// ErrNotLinked.
func (d *Direct) Reinforce(ctx context.Context, from, to string, delta float64) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("synapse.Direct.Reinforce: %w", ctx.Err())
	default:
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	toSet, ok := d.links[from]
	if !ok {
		return fmt.Errorf("synapse.Direct.Reinforce: %w: %s -> %s", ErrNotLinked, from, to)
	}
	e, ok := toSet[to]
	if !ok {
		return fmt.Errorf("synapse.Direct.Reinforce: %w: %s -> %s", ErrNotLinked, from, to)
	}
	e.Weight = max(e.Weight+delta, 0)
	toSet[to] = e
	return nil
}

// Fire delivers a signal along an established connection. Delivery is
// non-blocking: it either succeeds immediately or fails immediately and
// never waits for the target to consume. If the target consumer is not
// running or the inbox is full, Fire returns ErrTargetBusy — hosts that
// need reliable delivery should retry or poll the target state. A connection
// weighing less than the graph's floor refuses with ErrWeakSynapse: it stays
// in the graph, learns and can be Reinforced back above the line, but carries
// nothing in the meantime.
func (d *Direct) Fire(ctx context.Context, sig nerve.Signal) error {
	at := time.Now().UnixNano()
	d.mu.RLock()
	edge, ok := d.links[sig.From][sig.To]
	resolve := d.resolve
	floor := d.floor
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("synapse.Direct.Fire: %w: %s -> %s", ErrNotLinked, sig.From, sig.To)
	}
	if floor > 0 && edge.Weight < floor {
		return fmt.Errorf("synapse.Direct.Fire: %w: %s -> %s (weight %.3f < floor %.3f)",
			ErrWeakSynapse, sig.From, sig.To, edge.Weight, floor)
	}
	if resolve == nil {
		return fmt.Errorf("synapse.Direct.Fire: nil resolver")
	}
	inbox, ok := resolve(sig.To)
	if !ok {
		return fmt.Errorf("synapse.Direct.Fire: %w: %s", ErrNoTarget, sig.To)
	}
	select {
	case inbox <- sig:
		d.bumpConducted(sig.From, sig.To, at)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("synapse.Direct.Fire: %w", ctx.Err())
	default:
		return fmt.Errorf("synapse.Direct.Fire: %w: %s", ErrTargetBusy, sig.To)
	}
}

// bumpConducted records a successful delivery on an existing edge; a
// concurrently unlinked edge is skipped (the connection is already gone).
// Count and timestamp move together, so a learning rule can never read a
// conduct time it has not also counted.
func (d *Direct) bumpConducted(from, to string, at int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if toSet, ok := d.links[from]; ok {
		if e, ok := toSet[to]; ok {
			e.Fired++
			e.Spiked = at
			toSet[to] = e
		}
	}
}

// Edges returns a deep-copied snapshot of from's outgoing edges; from == ""
// returns the whole graph (persistence export primitive). An unknown from
// yields an empty snapshot, not an error.
func (d *Direct) Edges(ctx context.Context, from string) ([]Edge, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("synapse.Direct.Edges: %w", ctx.Err())
	default:
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if from == "" {
		total := 0
		for _, toSet := range d.links {
			total += len(toSet)
		}
		out := make([]Edge, 0, total)
		for _, toSet := range d.links {
			for _, e := range toSet {
				out = append(out, e)
			}
		}
		return out, nil
	}
	toSet, ok := d.links[from]
	if !ok {
		return []Edge{}, nil
	}
	out := make([]Edge, 0, len(toSet))
	for _, e := range toSet {
		out = append(out, e)
	}
	return out, nil
}

// Compile-time assertion: Direct implements Synapse.
var _ Synapse = (*Direct)(nil)
