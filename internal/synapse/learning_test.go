// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// learning_test.go — reference learning rule tests: Hebbian, STDP timing
// window, pruning, and the HebbianFire host pattern.
package synapse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qyiun666/meowire/internal/nerve"
)

// weightOf fetches the from→to weight from a snapshot.
func weightOf(t *testing.T, d *Direct, from, to string) float64 {
	t.Helper()
	edges, err := d.Edges(context.Background(), from)
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	for _, e := range edges {
		if e.To == to {
			return e.Weight
		}
	}
	t.Fatalf("edge %s -> %s not found", from, to)
	return 0
}

// TestHebbianReinforces: a successful delivery strengthens the connection
// by the learning rate.
func TestHebbianReinforces(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(nil)
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := Hebbian(ctx, d, "a", "b", 0.1); err != nil {
		t.Fatalf("hebbian: %v", err)
	}
	if got := weightOf(t, d, "a", "b"); got != 1.1 {
		t.Fatalf("weight = %v, want 1.1", got)
	}
}

// TestSTDPDirectionAndMagnitude: dt > 0 (pre before post) strengthens
// (LTP), dt < 0 weakens (LTD), |dt| far outside tau changes little.
func TestSTDPDirectionAndMagnitude(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(nil)
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	p := STDPParams{APlus: 0.5, AMinus: 0.4, Tau: 20 * time.Millisecond}

	// LTP: pre fired 10ms before post → weight up by ~0.5·e^(-0.5) ≈ 0.303.
	if err := STDP(ctx, d, "a", "b", 10*time.Millisecond, p); err != nil {
		t.Fatalf("stdp ltp: %v", err)
	}
	if got := weightOf(t, d, "a", "b"); got <= 1.0 || got >= 1.5 {
		t.Fatalf("LTP weight = %v, want in (1.0, 1.5)", got)
	}

	// LTD: post fired 10ms before pre → weight down (below the LTP peak).
	afterLTP := weightOf(t, d, "a", "b")
	if err := STDP(ctx, d, "a", "b", -10*time.Millisecond, p); err != nil {
		t.Fatalf("stdp ltd: %v", err)
	}
	if got := weightOf(t, d, "a", "b"); got >= afterLTP {
		t.Fatalf("LTD weight = %v, want < %v (LTP peak)", got, afterLTP)
	}

	// Far outside the window (1s ≫ 20ms): decay ≈ e^-50 ≈ 0 → ~no change.
	before := weightOf(t, d, "a", "b")
	if err := STDP(ctx, d, "a", "b", time.Second, p); err != nil {
		t.Fatalf("stdp far: %v", err)
	}
	if got := weightOf(t, d, "a", "b"); got != before {
		t.Fatalf("far-window weight = %v, want unchanged %v", got, before)
	}
}

// TestSTDPZeroDelta: simultaneous spikes produce no change.
func TestSTDPZeroDelta(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(nil)
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	p := STDPParams{APlus: 0.5, AMinus: 0.4, Tau: 20 * time.Millisecond}
	if err := STDP(ctx, d, "a", "b", 0, p); err != nil {
		t.Fatalf("stdp: %v", err)
	}
	if got := weightOf(t, d, "a", "b"); got != 1.0 {
		t.Fatalf("weight = %v, want 1.0 (no change)", got)
	}
}

// TestSTDPParamValidation: non-positive tau or negative magnitudes error.
func TestSTDPParamValidation(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(nil)
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := STDP(ctx, d, "a", "b", time.Millisecond, STDPParams{APlus: 0.5, AMinus: 0.4}); err == nil {
		t.Fatal("zero tau should error")
	}
	if err := STDP(ctx, d, "a", "b", time.Millisecond, STDPParams{APlus: -1, AMinus: 0.4, Tau: time.Millisecond}); err == nil {
		t.Fatal("negative APlus should error")
	}
}

// TestSTDPMissingConnection: STDP on an unlinked pair returns ErrNotLinked.
func TestSTDPMissingConnection(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(nil)
	p := STDPParams{APlus: 0.5, AMinus: 0.4, Tau: 20 * time.Millisecond}
	if err := STDP(ctx, d, "a", "b", time.Millisecond, p); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want ErrNotLinked", err)
	}
}

// TestPruneRemovesWeak: young-and-weak edges are pruned; weak-but-used and
// strong edges survive; the count is exact.
func TestPruneRemovesWeak(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(nil)
	// weak + never fired → prune
	if err := d.Link(ctx, "a", "b", 0.1); err != nil {
		t.Fatalf("link: %v", err)
	}
	// weak + fired often → survive
	if err := d.Link(ctx, "a", "c", 0.2); err != nil {
		t.Fatalf("link: %v", err)
	}
	// strong + never fired → survive
	if err := d.Link(ctx, "a", "d", 0.9); err != nil {
		t.Fatalf("link: %v", err)
	}
	// weak + fired a bit → survive (fired >= minFired)
	if err := d.Link(ctx, "e", "f", 0.15); err != nil {
		t.Fatalf("link: %v", err)
	}

	inboxes := map[string]chan nerve.Signal{
		"c": make(chan nerve.Signal, 2), // a->c fires twice
		"f": make(chan nerve.Signal, 2), // e->f fires twice
	}
	d.SetResolver(fakeResolver(inboxes))
	// bump Fired: a->c twice, e->f twice.
	for _, sig := range []nerve.Signal{
		{From: "a", To: "c"}, {From: "a", To: "c"},
		{From: "e", To: "f"}, {From: "e", To: "f"},
	} {
		if err := d.Fire(ctx, sig); err != nil {
			t.Fatalf("fire %v: %v", sig, err)
		}
	}

	removed, err := Prune(ctx, d, 0.3, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if d.Connected("a", "b") {
		t.Fatal("a->b should be pruned")
	}
	for _, pair := range [][2]string{{"a", "c"}, {"a", "d"}, {"e", "f"}} {
		if !d.Connected(pair[0], pair[1]) {
			t.Fatalf("%s->%s should survive", pair[0], pair[1])
		}
	}
}

// TestHebbianFireLearnsOnSuccess: the end-to-end pattern fires and learns.
func TestHebbianFireLearnsOnSuccess(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 1)
	d := NewDirect(fakeResolver(map[string]chan nerve.Signal{"b": inbox}))
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s1", From: "a", To: "b"}
	if err := HebbianFire(ctx, d, sig, 0.1); err != nil {
		t.Fatalf("hebbian fire: %v", err)
	}
	if got := weightOf(t, d, "a", "b"); got != 1.1 {
		t.Fatalf("weight = %v, want 1.1 (learned)", got)
	}
	select {
	case got := <-inbox:
		if got.ID != sig.ID {
			t.Fatalf("delivered id = %q, want %q", got.ID, sig.ID)
		}
	default:
		t.Fatal("signal not delivered")
	}
}

// TestHebbianFireNoLearningOnFailure: a failed delivery leaves the weight
// untouched (no learning on failure).
func TestHebbianFireNoLearningOnFailure(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(nil) // no resolver → delivery fails
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{From: "a", To: "b"}
	if err := HebbianFire(ctx, d, sig, 0.1); err == nil {
		t.Fatal("expected delivery error")
	}
	if got := weightOf(t, d, "a", "b"); got != 1.0 {
		t.Fatalf("weight = %v, want 1.0 (no learning on failure)", got)
	}
}
