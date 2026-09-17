// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// conduction_test.go — the weight-gated graph: a conduction floor the host
// injects, the conduct timestamp every delivery leaves behind, and the learning
// rule that reads those timestamps instead of a clock of its own.
package synapse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qyiun666/meowire/internal/nerve"
)

func TestBelowFloorDoesNotConduct(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 1)
	d := NewDirect(DirectConfig{
		Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox}),
		Floor:    0.5,
	})
	if err := d.Link(ctx, "a", "b", 0.4); err != nil {
		t.Fatalf("link: %v", err)
	}

	err := d.Fire(ctx, nerve.Signal{ID: "s", From: "a", To: "b"})
	if !errors.Is(err, ErrWeakSynapse) {
		t.Fatalf("fire below floor = %v, want ErrWeakSynapse", err)
	}
	select {
	case got := <-inbox:
		t.Fatalf("a refused signal was delivered: %+v", got)
	default:
	}
	// Refusal costs traffic, not the connection: it stays in the graph, its
	// counters stay untouched, and it can be reinforced back above the line.
	edge, ok := findEdge(mustEdges(t, d, "a"), "a", "b")
	if !ok {
		t.Fatal("a refused connection disappeared from the graph")
	}
	if edge.Fired != 0 || edge.Spiked != 0 {
		t.Fatalf("refused edge = %+v, want no count and no timestamp", edge)
	}
	if err := d.Reinforce(ctx, "a", "b", 0.2); err != nil {
		t.Fatalf("reinforce: %v", err)
	}
	if err := d.Fire(ctx, nerve.Signal{ID: "s2", From: "a", To: "b"}); err != nil {
		t.Fatalf("fire after climbing back over the floor: %v", err)
	}
}

// TestZeroFloorDisablesGating pins the opt-in reading of the floor: strength
// alone never decides connectivity until a host draws a line.
func TestZeroFloorDisablesGating(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 1)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	for _, w := range []float64{0, 0.0001} {
		if err := d.Link(ctx, "a", "b", w); err != nil {
			t.Fatalf("link at %v: %v", w, err)
		}
		if err := d.Fire(ctx, nerve.Signal{ID: "s", From: "a", To: "b"}); err != nil {
			t.Fatalf("fire at weight %v with no floor: %v", w, err)
		}
		<-inbox
	}
}

// TestWeakSynapseDistinctFromNotLinked keeps the two refusals apart: a host
// that retries on a busy or weak line must not retry on an absent connection.
func TestWeakSynapseDistinctFromNotLinked(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{
		Resolver: fakeResolver(map[string]chan nerve.Signal{}),
		Floor:    0.5,
	})
	if err := d.Link(ctx, "a", "b", 0.1); err != nil {
		t.Fatalf("link: %v", err)
	}
	weak := d.Fire(ctx, nerve.Signal{From: "a", To: "b"})
	unlinked := d.Fire(ctx, nerve.Signal{From: "a", To: "zz"})
	if !errors.Is(weak, ErrWeakSynapse) || errors.Is(weak, ErrNotLinked) {
		t.Fatalf("weak = %v, want ErrWeakSynapse and not ErrNotLinked", weak)
	}
	if !errors.Is(unlinked, ErrNotLinked) || errors.Is(unlinked, ErrWeakSynapse) {
		t.Fatalf("unlinked = %v, want ErrNotLinked and not ErrWeakSynapse", unlinked)
	}
}

// TestConductTimestampMovesWithTheCount: Fired and Spiked are one write, so a
// count is never recorded without the moment it counts.
func TestConductTimestampMovesWithTheCount(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 2)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	if err := d.Link(ctx, "a", "b", 1); err != nil {
		t.Fatalf("link: %v", err)
	}
	before := time.Now().Add(-time.Second).UnixNano()
	if err := d.Fire(ctx, nerve.Signal{From: "a", To: "b"}); err != nil {
		t.Fatalf("fire: %v", err)
	}
	edge, ok := findEdge(mustEdges(t, d, "a"), "a", "b")
	if !ok {
		t.Fatal("edge gone after a delivery")
	}
	if edge.Fired != 1 || edge.Spiked < before {
		t.Fatalf("edge = %+v, want one delivery stamped at or after %d", edge, before)
	}
}

func TestSTDPFromReadsGraphTimestamps(t *testing.T) {
	ctx := context.Background()
	p := STDPParams{APlus: 0.5, AMinus: 0.4, Tau: time.Second}

	// Causal: pre conducted first, so post's later spike strengthens pre→post.
	causal := NewDirect(DirectConfig{Initial: []Edge{
		{From: "pre", To: "post", Weight: 0.5, Fired: 1, Spiked: time.Unix(0, 100).UnixNano()},
		{From: "post", To: "other", Weight: 0.5, Fired: 1, Spiked: time.Unix(0, 200).UnixNano()},
	}})
	if err := STDPFrom(ctx, causal, "pre", "post", p); err != nil {
		t.Fatalf("stdp from: %v", err)
	}
	if w := edgeWeight(t, causal, "pre", "post"); w <= 0.5 {
		t.Fatalf("causal pairing left weight %v, want LTP above 0.5", w)
	}

	// Anti-causal: post spoke before pre, so the same call weakens the line.
	anti := NewDirect(DirectConfig{Initial: []Edge{
		{From: "pre", To: "post", Weight: 0.5, Fired: 1, Spiked: time.Unix(0, 200).UnixNano()},
		{From: "post", To: "other", Weight: 0.5, Fired: 1, Spiked: time.Unix(0, 100).UnixNano()},
	}})
	if err := STDPFrom(ctx, anti, "pre", "post", p); err != nil {
		t.Fatalf("stdp from (anti-causal): %v", err)
	}
	if w := edgeWeight(t, anti, "pre", "post"); w >= 0.5 {
		t.Fatalf("anti-causal pairing left weight %v, want LTD below 0.5", w)
	}

	// A side that never conducted has no spike to pair; that is reported, not
	// quietly treated as a zero difference.
	never := NewDirect(DirectConfig{Initial: []Edge{{From: "pre", To: "post", Weight: 0.5}}})
	if err := STDPFrom(ctx, never, "pre", "post", p); err == nil {
		t.Fatal("STDPFrom over an unspiked graph reported success")
	}
}

func edgeWeight(t *testing.T, d *Direct, from, to string) float64 {
	t.Helper()
	e, ok := findEdge(mustEdges(t, d, from), from, to)
	if !ok {
		t.Fatalf("edge %s -> %s missing", from, to)
	}
	return e.Weight
}

// mustEdges snapshots one node's out-edges, failing the test on error.
func mustEdges(t *testing.T, d *Direct, from string) []Edge {
	t.Helper()
	edges, err := d.Edges(context.Background(), from)
	if err != nil {
		t.Fatalf("edges(%s): %v", from, err)
	}
	return edges
}
