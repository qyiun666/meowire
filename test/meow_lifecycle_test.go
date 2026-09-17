// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_lifecycle_test.go — lifecycle tests: Close behavior.
package meowire_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestCloseBehavior verifies Close calls the host closer and subsequent Stimulate fails.
func TestCloseBehavior(t *testing.T) {
	cs := &testutil.Closer{}
	a, err := testNew(testOrgans(meowire.Organs{Closer: cs}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Stimulate should work before close
	var gotDone bool
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Fatal("expected EventDone before close")
	}

	// Close
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !cs.Called {
		t.Fatal("host closer should have been called")
	}

	// Stimulate after close should yield ErrCellClosed
	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("post-close events = %d, want 1", len(events))
	}
	if events[0].Kind != meowire.EventError {
		t.Fatalf("post-close event kind = %d, want EventError", events[0].Kind)
	}
	if !errors.Is(events[0].Err, meowire.ErrCellClosed) {
		t.Fatalf("post-close event err = %v, want ErrCellClosed", events[0].Err)
	}
}

// TestStimulateAfterClose verifies Stimulate on a closed agent yields EventError with ErrCellClosed.
func TestStimulateAfterClose(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("events count = %d, want 1", len(events))
	}
	if events[0].Kind != meowire.EventError {
		t.Fatalf("events[0] kind = %d, want EventError", events[0].Kind)
	}
	if !errors.Is(events[0].Err, meowire.ErrCellClosed) {
		t.Fatalf("events[0].Err = %v, want ErrCellClosed", events[0].Err)
	}
}

// TestEveryEventNamesItsAuthor: CellID is stamped at the cell boundary, so a
// stream written to a log still says which cell spoke — including the event a
// closed facade raises without ever running a loop.
func TestEveryEventNamesItsAuthor(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{ID: "author"}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	seen := 0
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.CellID != "author" {
			t.Fatalf("event %v carries author %q, want \"author\"", ev.Kind, ev.CellID)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("the round yielded nothing to check")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	after := 0
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.CellID != "author" {
			t.Fatalf("post-close event carries author %q, want \"author\"", ev.CellID)
		}
		after++
	}
	if after == 0 {
		t.Fatal("a closed agent yielded nothing to check")
	}
}

// TestLiveStreamSurvivesWire: a real event stream is wire-ready end to end —
// every event comes back equal, with no field reported as dropped.
func TestLiveStreamSurvivesWire(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{ID: "wired"}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	var kinds []meowire.EventKind
	for ev := range a.Stimulate(context.Background(), "work") {
		kinds = append(kinds, ev.Kind)
		data, err := meowire.EncodeEvent(ev)
		if err != nil {
			t.Fatalf("encode %v: %v", ev.Kind, err)
		}
		got, err := meowire.DecodeEvent(data)
		if err != nil {
			t.Fatalf("decode %v: %v", ev.Kind, err)
		}
		if !reflect.DeepEqual(got, ev) {
			t.Errorf("%v came back as %+v, want %+v", ev.Kind, got, ev)
		}
		if len(got.Dropped) != 0 {
			t.Errorf("%v reported dropped fields %v, want none", ev.Kind, got.Dropped)
		}
	}
	// A round that produced only one event would not have exercised much.
	if len(kinds) < 3 {
		t.Fatalf("kinds = %v, want a stream of at least three events", kinds)
	}
}
