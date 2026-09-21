// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_lifecycle_test.go — lifecycle tests: Close behavior.
package meowire_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestCloseBehavior verifies Close calls the host closer and subsequent Stimulate fails.
func TestCloseBehavior(t *testing.T) {
	cs := &testutil.Closer{}
	a, err := testNew(testOrgans(t, meowire.Organs{Closer: cs}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Stimulate should work before close
	if !hasKind(collect(a.Stimulate(context.Background(), "work")), meowire.EventDone) {
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
	events := collect(a.Stimulate(context.Background(), "work"))
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
	a, err := testNew(testOrgans(t, meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	events := collect(a.Stimulate(context.Background(), "work"))

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
	a, err := testNew(testOrgans(t, meowire.Organs{ID: "author"}), meowire.Config{})
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
	a, err := testNew(testOrgans(t, meowire.Organs{ID: "wired"}), meowire.Config{})
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

// journal is the coverage ledger for the wire guards: every event is encoded and
// decoded as it is seen, and the kind is counted. A refusal on the encode side is
// a lost line in a host's audit log, so the framework's own output — not only a
// hand-built fixture — has to survive the shape it publishes.
type journal struct {
	t     *testing.T
	kinds map[meowire.EventKind]int
}

func (j *journal) watch(seq iter.Seq[meowire.Event]) []meowire.Event {
	j.t.Helper()
	var out []meowire.Event
	for ev := range seq {
		data, err := meowire.EncodeEvent(ev)
		if err != nil {
			j.t.Fatalf("encode %v: %v", ev.Kind, err)
		}
		got, err := meowire.DecodeEvent(data)
		if err != nil {
			j.t.Fatalf("decode %v: %v", ev.Kind, err)
		}
		if got.Kind != ev.Kind || got.CellID != ev.CellID {
			j.t.Fatalf("%v came back as kind %v of cell %q", ev.Kind, got.Kind, got.CellID)
		}
		if j.kinds[ev.Kind] == 0 && ev.Kind != meowire.EventError && len(got.Dropped) != 0 {
			j.t.Errorf("%v reported dropped fields %v, want none", ev.Kind, got.Dropped)
		}
		j.kinds[ev.Kind]++
		out = append(out, ev)
	}
	return out
}

// wireRule denies one tool and asks on another — the two rulings a
// pass-through membrane never produces.
func wireRule(_ context.Context, a meowire.Action) (meowire.Verdict, string, error) {
	switch a.Call.Name {
	case "danger":
		return meowire.VerdictDeny, "not on this box", nil
	case "asker":
		return meowire.VerdictAsk, "confirm?", nil
	}
	return meowire.VerdictAllow, "", nil
}

// TestEveryProducedKindIsJournalable drives one agent through every suspension
// the facade owns and asserts the ledger afterwards: the event wire is only real
// if the framework's own stream crosses it.
func TestEveryProducedKindIsJournalable(t *testing.T) {
	ctx := context.Background()
	j := &journal{t: t, kinds: map[meowire.EventKind]int{}}

	plan := testutil.FakeCompletion{
		Text:  "planning",
		Usage: testutil.FakeUsage{Prompt: 1, Completion: 2, Total: 3},
		ToolCalls: []testutil.FakeCall{
			{ID: "d1", Name: "danger"}, // denied by the membrane
			{ID: "a1", Name: "asker"},  // asks, and the round suspends there
		},
	}

	a, err := testNew(testOrgans(t, meowire.Organs{
		ID:      "wire-all",
		Sandbox: testutil.Sandbox{Fn: wireRule, Bound: "wire test bounds"},
		Act: testutil.Effector{Fn: func(_ context.Context, act meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "asked-" + act.Call.Name}, nil
		}},
	}, plan, testutil.FakeCompletion{Text: "finished"}), meowire.Config{MaxRounds: 4})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	events := j.watch(a.Stimulate(ctx, "work"))
	sess, ok := waitedSession(events)
	if !ok {
		t.Fatalf("no suspension in %v", j.kinds)
	}
	j.watch(a.Resume(ctx, sess, meowire.Response{Answer: "confirmed"}))

	// The two runtime audits take effect on the next run, and the pause is
	// honored at its first gap point — Replace, Config, State, Paused.
	if _, err := a.Replace(meowire.SlotMem, testutil.Memory{}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	a.UpdateConfig(meowire.Config{MaxRounds: 4})
	a.Pause()
	paused, ok := waitedSession(j.watch(a.Stimulate(ctx, "again")))
	if !ok {
		t.Fatalf("no pause in %v", j.kinds)
	}
	j.watch(a.Resume(ctx, paused, meowire.Response{}))

	// The error arm: a loop that outlives its rounds while tools are pending.
	b, err := testNew(testOrgans(t, meowire.Organs{
		ID: "wire-err", Sandbox: testutil.Sandbox{},
	}, plan), meowire.Config{MaxRounds: 1})
	if err != nil {
		t.Fatalf("new error case: %v", err)
	}
	defer b.Close()
	j.watch(b.Stimulate(ctx, "work"))

	var missing []string
	for _, k := range allKinds() {
		if j.kinds[k] == 0 {
			missing = append(missing, k.String())
		}
	}
	if len(missing) > 0 {
		t.Fatalf("kinds never produced by the framework's own runs: %v", missing)
	}
}

// allKinds is the complete enumeration the ledger is checked against; a kind
// added to the loop without a producer here is a gap in this guard, not in the
// wire.
func allKinds() []meowire.EventKind {
	return []meowire.EventKind{
		meowire.EventText, meowire.EventToolCall, meowire.EventToolResult, meowire.EventState,
		meowire.EventDone, meowire.EventError, meowire.EventUsage, meowire.EventSandbox,
		meowire.EventWaitInput, meowire.EventPaused, meowire.EventReplace, meowire.EventConfig,
	}
}

// waitedSession picks out the suspension handle in a collected stream.
func waitedSession(events []meowire.Event) (meowire.Session, bool) {
	for _, ev := range events {
		if ev.Kind == meowire.EventWaitInput || ev.Kind == meowire.EventPaused {
			return ev.Wait.Session, true
		}
	}
	return meowire.Session{}, false
}

// TestAbandonedStreamRequeuesItsAudits: a consumer that stops mid-list abandons
// the round, but the audits the prelude never delivered belong to the next run —
// exactly-once is a promise about the stream, not about a run that finished. The
// facade is what stands between a host's early stop and the cell that requeues,
// so this is the level that has to prove it.
func TestAbandonedStreamRequeuesItsAudits(t *testing.T) {
	ctx := context.Background()
	a, err := testNew(testOrgans(t, meowire.Organs{ID: "abandon"}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	// Three audits, all taking effect on the next run: two swaps, then a config.
	if _, err := a.Replace(meowire.SlotMem, testutil.Memory{}); err != nil {
		t.Fatalf("replace mem: %v", err)
	}
	if _, err := a.Replace(meowire.SlotSandbox, testutil.Sandbox{}); err != nil {
		t.Fatalf("replace sandbox: %v", err)
	}
	a.UpdateConfig(meowire.Config{MaxRounds: 4})

	// Consume the first audit and stop there — the cooperative path, where the
	// consumer's yield returns false.
	var got []meowire.EventKind
	a.Stimulate(ctx, "work")(func(ev meowire.Event) bool {
		got = append(got, ev.Kind)
		return false
	})
	if len(got) != 1 || got[0] != meowire.EventReplace {
		t.Fatalf("abandoned stream delivered %v, want just the first EventReplace", got)
	}

	var rest []meowire.EventKind
	for ev := range a.Stimulate(ctx, "again") {
		rest = append(rest, ev.Kind)
	}
	if len(rest) < 2 || rest[0] != meowire.EventReplace || rest[1] != meowire.EventConfig {
		t.Fatalf("next run leads with %v, want the undelivered Replace then Config audit", rest)
	}
}
