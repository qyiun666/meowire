// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// direct_test.go — Direct white-box tests: delivery semantics (delivered/unlinked/no target/busy).
package synapse

import (
	"context"
	"errors"
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
	d := NewDirect(fakeResolver(map[string]chan nerve.Signal{"b": inbox}))
	if err := d.Link(ctx, "a", "b"); err != nil {
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

// TestFireUnlinked verifies Fire on an unlinked direction returns ErrNotLinked.
func TestFireUnlinked(t *testing.T) {
	d := NewDirect(fakeResolver(map[string]chan nerve.Signal{}))
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(context.Background(), sig); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want ErrNotLinked", err)
	}
}

// TestFireNoTarget verifies Fire returns ErrNoTarget when linked but the target is unknown.
func TestFireNoTarget(t *testing.T) {
	d := NewDirect(fakeResolver(map[string]chan nerve.Signal{}))
	if err := d.Link(context.Background(), "a", "b"); err != nil {
		t.Fatalf("link: %v", err)
	}
	sig := nerve.Signal{ID: "s", From: "a", To: "b", Kind: nerve.KindNotice}
	if err := d.Fire(context.Background(), sig); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("err = %v, want ErrNoTarget", err)
	}
}

// TestFireClosedTarget verifies a closed target (resolver returns false) yields ErrNoTarget, never a silent drop.
func TestFireClosedTarget(t *testing.T) {
	d := NewDirect(func(id string) (chan<- nerve.Signal, bool) {
		return nil, false // resolver rejects a closed Cell
	})
	if err := d.Link(context.Background(), "a", "b"); err != nil {
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
	d := NewDirect(fakeResolver(map[string]chan nerve.Signal{"b": inbox}))
	if err := d.Link(ctx, "a", "b"); err != nil {
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
	d := NewDirect(fakeResolver(map[string]chan nerve.Signal{"b": inbox}))
	if err := d.Link(ctx, "a", "b"); err != nil {
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
	d := NewDirect(nil) // resolver injected later at assembly
	if err := d.Link(ctx, "a", "b"); err != nil {
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

// TestConnected verifies Connected reflects Link state.
func TestConnected(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(fakeResolver(map[string]chan nerve.Signal{}))

	if d.Connected("a", "b") {
		t.Fatal("a->b should not be connected initially")
	}
	if err := d.Link(ctx, "a", "b"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if !d.Connected("a", "b") {
		t.Fatal("a->b should be connected after Link")
	}
	if d.Connected("b", "a") {
		t.Fatal("b->a should not be connected (directed)")
	}
}
