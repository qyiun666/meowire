// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// synapse.go — inter-agent connection and signal delivery contract.
package synapse

import (
	"context"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Edge is one synaptic edge: a from→to connection with its strength.
// Weight is the synaptic strength (read/written by host learning rules);
// Fired is the cumulative successful delivery count (host statistics).
type Edge struct {
	From   string
	To     string
	Weight float64 // synaptic strength (host learning rules read/write)
	Fired  int64   // cumulative successful deliveries (host statistics)
}

// Synapse is the inter-agent connectivity contract: a plastic synapse graph.
//
//	Link      — synaptogenesis: establish from→to with an initial strength
//	Unlink    — synapse elimination: sever a connection
//	Reinforce — LTP/LTD: adjust strength (delta may be negative, floor 0)
//	Fire      — signal delivery along an established connection
//	Edges     — graph snapshot (persistence export; from="" = whole graph)
//
// Learning rules (Hebbian / STDP) are host-side: the framework stores state
// and never decides when to change it. Implementations are wired by the
// composition root; persistence (export via Edges, restore via constructor
// initial edges) is host-managed.
type Synapse interface {
	Link(ctx context.Context, from, to string, weight float64) error
	Unlink(ctx context.Context, from, to string) error
	Reinforce(ctx context.Context, from, to string, delta float64) error
	Fire(ctx context.Context, sig nerve.Signal) error
	Edges(ctx context.Context, from string) ([]Edge, error)
}
