// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// inbox_test.go — the inbound colony track: what the loop takes from a cell's
// signal queue, when it becomes visible, and what a notice can withhold.
package nerve

import (
	"context"
	"slices"
	"testing"
)

// newInbox stands in for the cell: a queue with room for the whole capacity, a
// send helper, and the ingest closure a LoopContext is handed (take everything
// waiting, right now — what the cell's pairing route does).
func newInbox(sig ...Signal) (func() []Signal, func(Signal)) {
	ch := make(chan Signal, InboxCapacity)
	for _, s := range sig {
		ch <- s
	}
	ingest := func() []Signal {
		var out []Signal
		for {
			select {
			case s := <-ch:
				out = append(out, s)
			default:
				return out
			}
		}
	}
	return ingest, func(s Signal) { ch <- s }
}

// TestInboundVisibleBeforeNextThink: a signal that is in the queue before a
// round's Think must be in that Think's prompt.
func TestInboundVisibleBeforeNextThink(t *testing.T) {
	ingest, send := newInbox()
	var seen [][]Signal
	rounds := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Ingest: ingest,
		Think: mockThinker{fn: func(_ context.Context, p *Prompt) (*Decision, error) {
			seen = append(seen, slices.Clone(p.Stimuli))
			rounds++
			if rounds == 1 {
				return &Decision{Text: "a", ToolCalls: []ToolCall{{ID: "t1", Name: "work"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{Result: "ok"}, nil }},
	}
	send(Signal{ID: "a/1", From: "a", Kind: KindStimulus, Payload: []byte("do the thing")})
	collectEvents(context.Background(), lc)

	if len(seen) != 2 {
		t.Fatalf("thinks = %d, want 2", len(seen))
	}
	if len(seen[0]) != 1 || string(seen[0][0].Payload) != "do the thing" || seen[0][0].From != "a" {
		t.Fatalf("round 1 stimuli = %+v, want the queued signal", seen[0])
	}
	if len(seen[1]) != 0 {
		t.Fatalf("round 2 stimuli = %+v, want the track replaced wholesale", seen[1])
	}
}

// TestInhibitionWithholdsNamedTool: a notice naming a tool refuses that call
// before the membrane ever sees it; siblings run normally. Both act paths carry
// the check, so both are exercised.
func TestInhibitionWithholdsNamedTool(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		name := "serial"
		if parallel {
			name = "parallel"
		}
		t.Run(name, func(t *testing.T) {
			inhibitChecked(t, parallel)
		})
	}
}

func inhibitChecked(t *testing.T, parallel bool) {
	t.Helper()
	ingest, _ := newInbox(Signal{ID: "n/1", From: "watcher", Kind: KindNotice, Payload: []byte("dangerous")})
	var executed []string
	var inhibited []string
	var seenInhibit [][]string
	lc := &LoopContext{
		CellID:       "c1",
		Input:        "work",
		Ingest:       ingest,
		ParallelActs: parallel,
		Think: mockThinker{fn: func(_ context.Context, p *Prompt) (*Decision, error) {
			seenInhibit = append(seenInhibit, slices.Clone(p.Inhibit))
			if len(p.ToolResults)+len(p.Context) == 0 {
				return &Decision{Text: "go", ToolCalls: []ToolCall{
					{ID: "t1", Name: "dangerous"}, {ID: "t2", Name: "safe"},
				}}, nil
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(_ context.Context, a Action) (*Effect, error) {
			executed = append(executed, a.Call.Name)
			return &Effect{Result: "ok"}, nil
		}},
	}
	events := collectEvents(context.Background(), lc)

	if len(seenInhibit) != 2 || !slices.Equal(seenInhibit[0], []string{"dangerous"}) || len(seenInhibit[1]) != 0 {
		t.Fatalf("Inhibit per round = %v, want [dangerous] then none (the track is replaced)", seenInhibit)
	}
	for _, e := range events {
		if e.Kind == EventToolResult && e.ToolCall != nil && e.Effect != nil && e.Effect.Err != "" {
			inhibited = append(inhibited, e.Effect.Err)
		}
	}
	if !slices.Equal(inhibited, []string{"[inhibited: watcher]"}) {
		t.Fatalf("refusal feedback = %v, want the notice's sender named", inhibited)
	}
	if !slices.Equal(executed, []string{"safe"}) {
		t.Fatalf("executed = %v, want only the unwithheld call", executed)
	}
	// The withheld call never reached the membrane: one tool-side audit record
	// for the round, and the refusal joined the Context track.
	if n := len(toolVerdicts(events)); n != 1 {
		t.Fatalf("tool-side verdicts = %d, want 1 (an inhibition is not a membrane ruling)", n)
	}
	if !slices.Contains(lc.Context, "[inhibited: watcher]") {
		t.Fatalf("context = %v, want the refusal recorded", lc.Context)
	}
}

// TestInFlightActNotPreempted: a notice that arrives after the round's Think
// waits for the next one — it revokes nothing already decided, and it is not
// lost.
func TestInFlightActNotPreempted(t *testing.T) {
	ingest, send := newInbox()
	var executed []string
	rounds := 0
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Ingest: ingest,
		Think: mockThinker{fn: func(_ context.Context, p *Prompt) (*Decision, error) {
			rounds++
			if rounds == 1 {
				return &Decision{Text: "go", ToolCalls: []ToolCall{{ID: "t1", Name: "later"}}}, nil
			}
			if len(p.Inhibit) == 0 {
				t.Fatalf("round %d Inhibit = %v, want the notice that arrived during round 1", rounds, p.Inhibit)
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(_ context.Context, a Action) (*Effect, error) {
			// The veto arrives while the round is already acting.
			send(Signal{ID: "n/2", From: "watcher", Kind: KindNotice, Payload: []byte("later")})
			executed = append(executed, a.Call.Name)
			return &Effect{Result: "ok"}, nil
		}},
	}
	collectEvents(context.Background(), lc)

	if !slices.Equal(executed, []string{"later"}) {
		t.Fatalf("executed = %v, want the in-flight call untouched", executed)
	}
	if rounds != 2 {
		t.Fatalf("rounds = %d, want 2", rounds)
	}
}

// TestInboxNotConfiguredIsQuiet: a cell with no colony wiring runs exactly as
// before — no drain, no inhibition, no panic reading a nil channel.
func TestInboxNotConfiguredIsQuiet(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "work",
		Think: mockThinker{fn: func(_ context.Context, p *Prompt) (*Decision, error) {
			if p.Stimuli != nil || p.Inhibit != nil {
				t.Fatalf("prompt = %+v, want both inbound tracks absent", p)
			}
			return &Decision{Text: "done"}, nil
		}},
		Act: mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{}, nil }},
	}
	events := collectEvents(context.Background(), lc)
	if last := events[len(events)-1]; last.Kind != EventDone {
		t.Fatalf("last event = %v, want Done", last.Kind)
	}
}
