// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// direct_test.go — Direct white-box tests: delivery semantics (delivered/unlinked/no target/busy).
package synapse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
)

// fakeResolver is a test resolver backed by a fixed inbox table.
func fakeResolver(inboxes map[string]chan nerve.Signal) Resolver {
	return func(id string) (chan<- nerve.Signal, bool) {
		ch, ok := inboxes[id]
		return ch, ok
	}
}

// TestFireDelivered verifies delivery succeeds when linked and the inbox has room.
func TestFireDelivered(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 1)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s1", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(ctx, sig); err != nil {
		t.Fatalf("fire: %v", err)
	}
	select {
	case got := <-inbox:
		if got.ID != sig.ID {
			t.Fatalf("got %q, want %q", got.ID, sig.ID)
		}
	default:
		t.Fatal("signal not delivered")
	}
}

// TestFireCarriesTaskStatus: the A2A-style task status travels with the
// signal untouched — hosts track task lifecycle end to end.
func TestFireCarriesTaskStatus(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 1)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{
		ID: "task-1", From: "a", To: "b", Kind: nerve.KindStimulus,
		Status: nerve.TaskWorking, Payload: []byte("do it"),
	}
	if err := d.Fire(ctx, sig); err != nil {
		t.Fatalf("fire: %v", err)
	}
	select {
	case got := <-inbox:
		if got.Status != nerve.TaskWorking {
			t.Fatalf("status = %q, want working", got.Status)
		}
		if string(got.Payload) != "do it" {
			t.Fatalf("payload = %q, want 'do it'", got.Payload)
		}
	default:
		t.Fatal("signal not delivered")
	}
}

// TestFireUnlinked verifies Fire on an unlinked direction returns ErrNotLinked.
func TestFireUnlinked(t *testing.T) {
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{})})
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(context.Background(), sig); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want ErrNotLinked", err)
	}
}

// TestFireNoTarget verifies Fire returns ErrNoTarget when linked but the target is unknown.
func TestFireNoTarget(t *testing.T) {
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{})})
	if err := d.Link(context.Background(), "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(context.Background(), sig); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("err = %v, want ErrNoTarget", err)
	}
}

// TestFireClosedTarget verifies a closed target (resolver returns false) yields ErrNoTarget, never a silent drop.
func TestFireClosedTarget(t *testing.T) {
	d := NewDirect(DirectConfig{Resolver: func(id string) (chan<- nerve.Signal, bool) {
		return nil, false // resolver rejects a closed Cell
	}})
	if err := d.Link(context.Background(), "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(context.Background(), sig); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("err = %v, want ErrNoTarget", err)
	}
}

// TestFireTargetBusy verifies a full, unconsumed inbox returns ErrTargetBusy (non-blocking, no hang).
func TestFireTargetBusy(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 1) // buffer 1, fill it first
	inbox <- nerve.Signal{ID: "occupied"}
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s2", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(ctx, sig); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("err = %v, want ErrTargetBusy", err)
	}
}

// TestFireCtxCancel verifies ctx cancellation returns immediately on an unconsumed inbox.
func TestFireCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inbox := make(chan nerve.Signal, 1)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	// Fill the buffer then cancel: ctx.Done wins the three-way select.
	inbox <- nerve.Signal{ID: "occupied"}
	cancel()
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(ctx, sig); !errors.Is(err, context.Canceled) {
		t.Fatalf("fire err = %v, want context.Canceled", err)
	}
}

// TestSetResolver verifies Fire fails without a resolver and succeeds after SetResolver injects one.
func TestSetResolver(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{}) // resolver injected later at assembly
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(ctx, sig); err == nil {
		t.Fatal("fire with nil resolver should fail")
	}

	inbox := make(chan nerve.Signal, 1)
	d.SetResolver(fakeResolver(map[string]chan nerve.Signal{"b": inbox}))
	if err := d.Fire(ctx, sig); err != nil {
		t.Fatalf("fire after SetResolver: %v", err)
	}
	select {
	case got := <-inbox:
		if got.ID != sig.ID {
			t.Fatalf("got %q, want %q", got.ID, sig.ID)
		}
	default:
		t.Fatal("signal not delivered after SetResolver")
	}
}

// conducts reports whether a from→to edge exists in a fresh whole-graph
// snapshot. Tests use this read path because connectivity is observable only
// through Edges — the graph exposes no per-edge probe.
func conducts(t *testing.T, d *Direct, from, to string) bool {
	t.Helper()
	edges, err := d.Edges(context.Background(), "")
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	_, ok := findEdge(edges, from, to)
	return ok
}

// TestLinkShowsInSnapshot verifies Link makes an edge visible in the graph
// snapshot and that the connection is directed.
func TestLinkShowsInSnapshot(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{})})

	if conducts(t, d, "a", "b") {
		t.Fatal("a->b should not be connected initially")
	}
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	if !conducts(t, d, "a", "b") {
		t.Fatal("a->b should be connected after Link")
	}
	if conducts(t, d, "b", "a") {
		t.Fatal("b->a should not be connected (directed)")
	}
}

// findEdge locates an edge in a snapshot (map order is unspecified).
func findEdge(edges []Edge, from, to string) (Edge, bool) {
	for _, e := range edges {
		if e.From == from && e.To == to {
			return e, true
		}
	}
	return Edge{}, false
}

// TestLinkWeightAndClamp: Link stores the initial strength, overwrites on
// re-Link (idempotent), and clamps negative weights to 0.
func TestLinkWeightAndClamp(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{})
	if err := d.Link(ctx, "a", "b", 2.5); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := d.Link(ctx, "a", "c", -3.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	edges, err := d.Edges(ctx, "a")
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	b, ok := findEdge(edges, "a", "b")
	if !ok || b.Weight != 1.0 {
		t.Fatalf("a->b = %+v, want weight 1.0 (overwritten)", b)
	}
	c, ok := findEdge(edges, "a", "c")
	if !ok || c.Weight != 0.0 {
		t.Fatalf("a->c = %+v, want weight 0.0 (clamped)", c)
	}
}

// TestUnlinkRemoves: Unlink severs a connection; a missing one returns
// ErrNotLinked; empty rows are dropped.
func TestUnlinkRemoves(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	if !conducts(t, d, "a", "b") {
		t.Fatal("a->b should be connected")
	}
	if err := d.Unlink(ctx, "a", "b"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if conducts(t, d, "a", "b") {
		t.Fatal("a->b should be severed after Unlink")
	}
	if err := d.Unlink(ctx, "a", "b"); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("second unlink err = %v, want ErrNotLinked", err)
	}
}

// TestReinforceAdjusts: positive delta strengthens (LTP), negative weakens
// (LTD), the result clamps at 0, and a missing connection errors.
func TestReinforceAdjusts(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := d.Reinforce(ctx, "a", "b", 0.5); err != nil {
		t.Fatalf("reinforce up: %v", err)
	}
	if err := d.Reinforce(ctx, "a", "b", -0.2); err != nil {
		t.Fatalf("reinforce down: %v", err)
	}
	if err := d.Reinforce(ctx, "a", "b", -5.0); err != nil {
		t.Fatalf("reinforce clamp: %v", err)
	}
	edges, _ := d.Edges(ctx, "a")
	e, ok := findEdge(edges, "a", "b")
	if !ok || e.Weight != 0.0 {
		t.Fatalf("a->b = %+v, want weight 0.0 (clamped at floor)", e)
	}
	if err := d.Reinforce(ctx, "x", "y", 1.0); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("reinforce missing err = %v, want ErrNotLinked", err)
	}
}

// TestFireIncrementsFired: successful deliveries accumulate on the edge.
func TestFireIncrementsFired(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 3)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	for i := range 3 {
		if err := d.Fire(ctx, sig); err != nil {
			t.Fatalf("fire %d: %v", i, err)
		}
	}
	edges, _ := d.Edges(ctx, "a")
	e, ok := findEdge(edges, "a", "b")
	if !ok || e.Fired != 3 {
		t.Fatalf("a->b = %+v, want Fired=3", e)
	}
}

// TestEdgesSnapshotIsolated: the snapshot is a deep copy; mutations do not
// leak back; unknown from yields an empty snapshot, not an error.
func TestEdgesSnapshotIsolated(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{})
	if err := d.Link(ctx, "a", "b", 1.0); err != nil {
		t.Fatalf("link: %v", err)
	}
	snap, err := d.Edges(ctx, "a")
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	snap[0].Weight = 99
	snap[0].Fired = 99
	edges, _ := d.Edges(ctx, "a")
	e, ok := findEdge(edges, "a", "b")
	if !ok || e.Weight != 1.0 || e.Fired != 0 {
		t.Fatalf("internal edge mutated by snapshot = %+v", e)
	}
	empty, err := d.Edges(ctx, "nobody")
	if err != nil || len(empty) != 0 {
		t.Fatalf("unknown from: edges = %v, err = %v, want empty nil-err", empty, err)
	}
}

// TestSnapshotRoundTrip: Edges(全图) → NewDirect(cfg with Initial) → Edges(全图)
// is edge-equivalent including Weight, Fired and Spiked — the persistence loop.
func TestSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 2)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	_ = d.Link(ctx, "a", "b", 1.0)
	_ = d.Link(ctx, "a", "c", 0.5)
	_ = d.Fire(ctx, nerve.Signal{ID: "s1", From: "a", To: "b", Kind: nerve.KindNotice})
	_ = d.Reinforce(ctx, "a", "b", 0.2)

	snap, err := d.Edges(ctx, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(snap) != 2 {
		t.Fatalf("exported edges = %d, want 2", len(snap))
	}

	d2 := NewDirect(DirectConfig{Initial: snap})
	restored, err := d2.Edges(ctx, "")
	if err != nil {
		t.Fatalf("restore export: %v", err)
	}
	if len(restored) != len(snap) {
		t.Fatalf("restored edges = %d, want %d", len(restored), len(snap))
	}
	for _, e := range snap {
		got, ok := findEdge(restored, e.From, e.To)
		if !ok || got != e {
			t.Fatalf("round-trip mismatch: exported %+v, restored %+v", e, got)
		}
	}
}

// TestNewDirectInitialEdges: constructor-injected edges restore the graph;
// negative weights clamp at 0.
func TestNewDirectInitialEdges(t *testing.T) {
	d := NewDirect(DirectConfig{Initial: []Edge{
		{From: "a", To: "b", Weight: 1.5, Fired: 7, Spiked: 42},
		{From: "a", To: "c", Weight: -1},
	}})
	if !conducts(t, d, "a", "b") || !conducts(t, d, "a", "c") {
		t.Fatal("initial edges should be connected")
	}
	edges, _ := d.Edges(context.Background(), "a")
	b, _ := findEdge(edges, "a", "b")
	if b.Weight != 1.5 || b.Fired != 7 || b.Spiked != 42 {
		t.Fatalf("a->b = %+v, want weight 1.5 fired 7 spiked 42", b)
	}
	c, _ := findEdge(edges, "a", "c")
	if c.Weight != 0.0 {
		t.Fatalf("a->c = %+v, want weight 0.0 (clamped)", c)
	}
}

// TestPlasticConcurrent: mixed Link/Reinforce/Edges/Unlink from many
// goroutines races cleanly (run with -race) and converges to an empty graph.
func TestPlasticConcurrent(t *testing.T) {
	ctx := context.Background()
	inbox := make(chan nerve.Signal, 8)
	d := NewDirect(DirectConfig{Resolver: fakeResolver(map[string]chan nerve.Signal{"b": inbox})})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			from := fmt.Sprintf("a%d", i)
			for j := range 50 {
				_ = d.Link(ctx, from, "b", float64(j))
				_ = d.Reinforce(ctx, from, "b", 0.5)
				_, _ = d.Edges(ctx, from)
			}
			_ = d.Unlink(ctx, from, "b")
		}(i)
	}
	wg.Wait()
	edges, err := d.Edges(ctx, "")
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("edges after concurrent unlink = %d, want 0", len(edges))
	}
}
