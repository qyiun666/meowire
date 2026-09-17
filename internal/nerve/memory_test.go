// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// memory_test.go — the two timepoints the framework owns on the experience port.
package nerve

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recMemory records every call it receives; either function may be nil to
// behave inertly.
type recMemory struct {
	recallFn   func(context.Context, MemoryQuery) ([]Record, error)
	rememberFn func(context.Context, CycleFacts) error
	queries    []MemoryQuery
	facts      []CycleFacts
}

func (m *recMemory) Recall(ctx context.Context, q MemoryQuery) ([]Record, error) {
	m.queries = append(m.queries, q)
	if m.recallFn == nil {
		return nil, nil
	}
	return m.recallFn(ctx, q)
}

func (m *recMemory) Remember(ctx context.Context, f CycleFacts) error {
	m.facts = append(m.facts, f)
	if m.rememberFn == nil {
		return nil
	}
	return m.rememberFn(ctx, f)
}

func doneThinker() mockThinker {
	return mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
		return &Decision{Text: "ok"}, nil
	}}
}

// TestRecallFillsPromptBeforeBeforeThink pins the recall checkpoint: the hook
// that owns the text track sees (and may overwrite) what the organ returned.
func TestRecallFillsPromptBeforeBeforeThink(t *testing.T) {
	ctx := context.Background()
	var hookSaw int
	hooks := testHooks()
	hooks.BeforeThink = func(_ context.Context, p *Prompt) error {
		hookSaw = len(p.Memories)
		return nil
	}
	mem := &recMemory{recallFn: func(context.Context, MemoryQuery) ([]Record, error) {
		return []Record{{Key: "k1", Content: []byte("note")}}, nil
	}}
	lc := &LoopContext{CellID: "c1", Input: "hello", Hooks: hooks, Think: doneThinker(), Mem: mem}

	events := collectEvents(ctx, lc)
	if mem.queries[0].CellID != "c1" || mem.queries[0].Cue != "hello" {
		t.Fatalf("recall query = %+v, want CellID c1 and the stimulus as cue", mem.queries[0])
	}
	if hookSaw != 1 {
		t.Fatalf("BeforeThink saw %d memories, want 1 (recall runs before the hook)", hookSaw)
	}
	var thinked bool
	for _, e := range events {
		if e.Kind == EventText {
			thinked = true
		}
	}
	if !thinked {
		t.Fatal("cycle produced no text event")
	}
}

// TestMemoriesNeverAccumulate pins the volatile-track invariant: a multi-round
// cycle hands the brain exactly one recall's worth every round, and a
// suspension snapshot carries none of it.
func TestMemoriesNeverAccumulate(t *testing.T) {
	ctx := context.Background()
	rounds := 0
	var sawPerRound []int
	mem := &recMemory{recallFn: func(context.Context, MemoryQuery) ([]Record, error) {
		return []Record{{Key: "k"}, {Key: "kk"}}, nil
	}}
	lc := &LoopContext{CellID: "c1", Input: "hello", Think: mockThinker{
		fn: func(_ context.Context, p *Prompt) (*Decision, error) {
			rounds++
			sawPerRound = append(sawPerRound, len(p.Memories))
			if rounds == 1 {
				return &Decision{Text: "work", ToolCalls: []ToolCall{{ID: "t1", Name: "tool"}}}, nil
			}
			return &Decision{Text: "done"}, nil
		},
	}, Mem: mem}
	lc.Act = mockEffector{fn: func(context.Context, Action) (*Effect, error) { return &Effect{Result: "r"}, nil }}

	collectEvents(ctx, lc)
	if rounds != 2 {
		t.Fatalf("rounds = %d, want 2 (a tool round then a final round)", rounds)
	}
	for i, n := range sawPerRound {
		if n != 2 {
			t.Fatalf("round %d saw %d memories, want the 2 recalled for that round only", i+1, n)
		}
	}
	if len(mem.queries) != 2 {
		t.Fatalf("recall calls = %d, want one per Think", len(mem.queries))
	}
}

// TestRememberRunsOnEveryTerminal: one write per invocation on all four exit
// arms, each carrying that arm's outcome.
func TestRememberRunsOnEveryTerminal(t *testing.T) {
	tests := []struct {
		name      string
		lc        func(*recMemory) *LoopContext
		stopEarly bool // stop the iterator at the first event instead of draining it
		want      CycleOutcome
	}{
		{
			name: "done",
			lc: func(m *recMemory) *LoopContext {
				return &LoopContext{CellID: "c1", Input: "hi", Think: doneThinker(), Mem: m}
			},
			want: OutcomeDone,
		},
		{
			name: "error",
			lc: func(m *recMemory) *LoopContext {
				return &LoopContext{CellID: "c1", Input: "hi", Mem: m, Think: mockThinker{
					fn: func(context.Context, *Prompt) (*Decision, error) { return nil, errors.New("boom") },
				}}
			},
			want: OutcomeError,
		},
		{
			name: "suspended",
			lc: func(m *recMemory) *LoopContext {
				return &LoopContext{CellID: "c1", Input: "hi", Mem: m, Think: mockThinker{
					fn: func(context.Context, *Prompt) (*Decision, error) {
						return &Decision{Text: "ask", ToolCalls: []ToolCall{{ID: "t1", Name: "ask_user"}}}, nil
					},
				}, Act: mockEffector{fn: func(context.Context, Action) (*Effect, error) {
					return &Effect{WaitInput: "question"}, nil
				}}}
			},
			want: OutcomeSuspended,
		},
		{
			name: "aborted",
			lc: func(m *recMemory) *LoopContext {
				return &LoopContext{CellID: "c1", Input: "hi", Think: doneThinker(), Mem: m}
			},
			stopEarly: true,
			want:      OutcomeAborted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mem := &recMemory{}
			lc := tt.lc(mem)
			if tt.stopEarly {
				fillRequired(lc)
				DecisionLoop{}.Cycle(ctx, lc, func(Event) bool { return false })
			} else {
				collectEvents(ctx, lc)
			}
			if len(mem.facts) != 1 {
				t.Fatalf("Remember calls = %d, want exactly 1 per invocation", len(mem.facts))
			}
			if got := mem.facts[0].Outcome; got != tt.want {
				t.Fatalf("facts outcome = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRememberErrorNeverRewritesCycle: the invocation already decided its
// outcome at this point, so a failing write is reported to the host and the
// done event still lands.
func TestRememberErrorNeverRewritesCycle(t *testing.T) {
	ctx := context.Background()
	writeErr := errors.New("store down")
	var hookErr error
	hooks := testHooks()
	hooks.OnError = func(_ context.Context, err error) { hookErr = err }
	lc := &LoopContext{CellID: "c1", Input: "hi", Hooks: hooks, Think: doneThinker(),
		Mem: &recMemory{rememberFn: func(context.Context, CycleFacts) error { return writeErr }}}

	events := collectEvents(ctx, lc)
	var done, failed bool
	for _, e := range events {
		switch e.Kind {
		case EventDone:
			done = true
		case EventError:
			failed = true
		}
	}
	if !done || failed {
		t.Fatalf("events: done=%v error=%v, want the cycle to finish normally", done, failed)
	}
	if hookErr == nil || !strings.Contains(hookErr.Error(), writeErr.Error()) {
		t.Fatalf("OnError = %v, want it to carry %v", hookErr, writeErr)
	}
}

// TestRecallFailureEndsTheCycle: a broken organ is reported, not papered over.
func TestRecallFailureEndsTheCycle(t *testing.T) {
	ctx := context.Background()
	recallErr := errors.New("index missing")
	lc := &LoopContext{CellID: "c1", Input: "hi", Think: doneThinker(),
		Mem: &recMemory{recallFn: func(context.Context, MemoryQuery) ([]Record, error) {
			return nil, recallErr
		}}}

	events := collectEvents(ctx, lc)
	var failed bool
	for _, e := range events {
		if e.Kind == EventError && errors.Is(e.Err, recallErr) {
			failed = true
		}
	}
	if !failed {
		t.Fatal("a failing recall must end the cycle with that error")
	}
}
