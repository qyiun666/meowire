// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// learning.go — reference learning rules for the plastic synapse graph.
//
// The framework stores state (weights, delivery counts) and never decides
// when to change it; these rules are reference implementations hosts may
// use directly or adapt (1.1.2). They operate on the Synapse interface only
// (Reinforce / Edges / Unlink), so they work with any implementation.
package synapse

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Hebbian applies the classic Hebbian rule — neurons that fire together
// wire together: a successful delivery strengthens the connection.
// Call it after a successful Fire (rate is the learning step).
func Hebbian(ctx context.Context, s Synapse, from, to string, rate float64) error {
	return s.Reinforce(ctx, from, to, rate)
}

// STDPParams tunes the spike-timing-dependent plasticity rule.
// APlus/AMinus: LTP/LTD magnitudes (typically APlus > AMinus so a
// balanced pre/post pair still nets positive learning).
// Tau: exponential time constant — the pairing window halves the
// weight change every tau.
type STDPParams struct {
	APlus  float64       // LTP magnitude (pre fired before post)
	AMinus float64       // LTD magnitude (post fired before pre)
	Tau    time.Duration // time constant of the exponential window
}

// STDP applies spike-timing-dependent plasticity to the from→to
// connection based on the spike-time difference dt = tPre − tPost:
//
//	dt > 0 (pre before post) → LTP:  Δw =  APlus · exp(−dt/τ)
//	dt < 0 (post before pre) → LTD:  Δw = −AMinus · exp(dt/τ)
//
// |dt| ≫ τ yields a negligible change (the pairing window closes).
// A missing connection returns ErrNotLinked.
func STDP(ctx context.Context, s Synapse, from, to string, dt time.Duration, p STDPParams) error {
	if p.Tau <= 0 {
		return fmt.Errorf("synapse.STDP: tau must be positive")
	}
	if p.APlus < 0 || p.AMinus < 0 {
		return fmt.Errorf("synapse.STDP: magnitudes must be non-negative")
	}
	abs := time.Duration(math.Abs(float64(dt)))
	decay := math.Exp(-float64(abs) / float64(p.Tau))
	var delta float64
	if dt > 0 {
		delta = p.APlus * decay
	} else if dt < 0 {
		delta = -p.AMinus * decay
	} else {
		delta = 0 // simultaneous spikes: no change
	}
	return s.Reinforce(ctx, from, to, delta)
}

// Prune removes weak connections — the periodic pruning counterpart of
// synaptic elimination. A connection is pruned when its weight is below
// weightFloor AND its cumulative deliveries are below minFired (young and
// weak; a weak but heavily used connection survives). Returns the number
// of removed edges.
func Prune(ctx context.Context, s Synapse, weightFloor float64, minFired int64) (int, error) {
	edges, err := s.Edges(ctx, "")
	if err != nil {
		return 0, fmt.Errorf("synapse.Prune: %w", err)
	}
	removed := 0
	for _, e := range edges {
		if e.Weight < weightFloor && e.Fired < minFired {
			if err := s.Unlink(ctx, e.From, e.To); err != nil {
				return removed, fmt.Errorf("synapse.Prune: %w", err)
			}
			removed++
		}
	}
	return removed, nil
}

// HebbianFire is the end-to-end host reference pattern: fire a signal and
// apply the Hebbian step on success — the "fire together, wire together"
// loop. It returns the delivery error unchanged when delivery fails (no
// learning on failure).
func HebbianFire(ctx context.Context, s Synapse, sig nerve.Signal, rate float64) error {
	if err := s.Fire(ctx, sig); err != nil {
		return err
	}
	return s.Reinforce(ctx, sig.From, sig.To, rate)
}
