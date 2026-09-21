// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// memory.go — blueprint port P7: experience in and out of the cycle.
package nerve

import (
	"context"
	"fmt"
)

// Record is one memory the framework carries to the brain. The framework never
// interprets Content; Key/Kind/Created exist for the organ's own bookkeeping.
type Record struct {
	Key     string // unique key within the store
	CellID  string // owning cell
	Kind    string // category, organ-defined
	Content []byte // payload
	Created int64  // Unix seconds, organ-written
}

// MemoryQuery is everything the framework can name at a recall point without
// guessing at a retrieval policy: which cell is asking and what it was asked.
// How (or whether) to use the cue is the organ's judgment.
type MemoryQuery struct {
	CellID string
	Cue    string // this invocation's stimulus text
}

// CycleFacts is what one invocation leaves behind for memory. The framework
// reports only what it knows by construction — the stimulus, the finalized
// output and how the invocation ended (Outcome, see CycleOutcome). Which facts
// are worth persisting, in what shape and for how long is the organ's call;
// deletion and forgetting never pass through this port.
type CycleFacts struct {
	CellID  string
	Input   string
	Output  string
	Outcome CycleOutcome
}

// Memory is the experience port (required): the framework owns the two
// timepoints, the organ owns the strategy.
//
// Recall runs before every Think, after the budget regulator has trimmed and
// before the BeforeThink hook fires — the hook may overwrite Prompt.Memories
// wholesale, exactly as it may overwrite Prompt.Context. Remember runs once per
// invocation at its terminal point, before OnCycleEnd, on every exit arm
// (done, error, suspension, consumer abort).
//
// A Recall error aborts the Think (the organ is part of the assembly; the loop
// does not decide to think without half of its context). A Remember error never
// rewrites the invocation: it reaches the host through OnError while the
// already-decided outcome stands.
type Memory interface {
	Recall(ctx context.Context, q MemoryQuery) ([]Record, error)
	Remember(ctx context.Context, facts CycleFacts) error
}

// recall fills this round's memory track (volatile: replaced wholesale per
// Think, never accumulated, never carried into a Session).
func (lc *LoopContext) recall(ctx context.Context) error {
	records, err := lc.Mem.Recall(ctx, MemoryQuery{CellID: lc.CellID, Cue: lc.Input})
	if err != nil {
		return fmt.Errorf("nerve: memory recall: %w", err)
	}
	lc.Memories = records
	return nil
}

// remember hands one invocation's facts to the memory organ. The error path is
// deliberately not the loop's: the outcome is already decided at this point, so
// a failing write is reported to the host, not merged into the cycle.
func (lc *LoopContext) remember(ctx context.Context, output string) {
	err := lc.Mem.Remember(ctx, CycleFacts{
		CellID:  lc.CellID,
		Input:   lc.Input,
		Output:  output,
		Outcome: lc.outcome,
	})
	if err != nil {
		lc.Hooks.OnError(ctx, fmt.Errorf("nerve: memory remember: %w", err))
	}
}
