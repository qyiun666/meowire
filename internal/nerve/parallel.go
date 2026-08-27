// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// parallel.go — ParallelActs batch execution path (v1.3.3, opt-in): one
// round's multiple tool calls run concurrently in three phases — serial
// gating, parallel Act, serial feedback. Events and hooks stay serial in
// call order throughout; only actWithRetry enters the concurrent phase.
package nerve

import (
	"context"
	"strings"
	"sync"
)

// runToolCallsParallel executes one round's tool call batch in three phases:
//
//  1. Serial gating — announce every call (EventToolCall in call order),
//     one pause gap point for the whole batch, then per-call gateTool
//     (sandbox membrane audit + BeforeAct, shared with the serial path).
//     A denied call gets its [denied: reason] feedback in place and is
//     skipped — it never affects its siblings.
//  2. Parallel execution — one goroutine per admitted call runs
//     actWithRetry (timeout/retry included); results land by call index.
//     Hooks, events, and loop state never enter this phase (the goroutines
//     read lc's config/ports read-only).
//  3. Serial feedback — AfterAct + ToolResult + EventToolResult in call
//     order regardless of completion order.
//
// Suspension semantics mirror the serial path with two batch-specific
// differences: a WaitInput result suspends with remaining empty (the batch
// has fully executed — replaying any call would duplicate side effects) and
// every sibling's feedback is appended before the snapshot, so Resume sees
// the complete picture. A pause at the batch gap point snapshots the whole
// batch unexecuted, so Resume replays it. ok=false ends the iterator (an
// error was emitted or the consumer stopped).
func runToolCallsParallel(ctx context.Context, lc *LoopContext, calls []ToolCall, round int, out *strings.Builder, yield func(Event) bool) (waiting *WaitInput, ok bool) {
	// Announce every call first (event order = call order).
	for i := range calls {
		if !yield(Event{Kind: EventToolCall, ToolCall: &calls[i]}) {
			return nil, false
		}
	}

	// One pause gap point before the batch: a paused run snapshots the whole
	// batch (nothing has executed yet) so Resume replays it — isomorphic with
	// the serial path pausing at the first call.
	if !waitIfPaused(ctx, lc, round, out, calls, yield) {
		return nil, false
	}

	// Phase 1: serial gating — the shared membrane gate per call (sandbox
	// audit + BeforeAct). Each batch entry keeps both identities: the
	// original call (feedback/snapshot identity, same as the serial path)
	// and the gated Action (BeforeAct mutations included — what executes).
	type batchEntry struct {
		call ToolCall // announced/original call — feedback identity
		act  Action   // gated action — execution payload
	}
	batch := make([]batchEntry, 0, len(calls))
	for _, tc := range calls {
		act, admitted, gOk := gateTool(ctx, lc, tc, yield)
		if !gOk {
			return nil, false
		}
		if admitted {
			batch = append(batch, batchEntry{call: tc, act: act})
		}
	}

	// Phase 2: parallel execution — only actWithRetry runs concurrently;
	// results land by call index so phase 3 stays deterministic. A ctx
	// cancellation aborts each goroutine naturally; the error flows through
	// feedback like any other tool failure.
	type outcome struct {
		eff *Effect
		err error
	}
	results := make([]outcome, len(batch))
	var wg sync.WaitGroup
	for i, e := range batch {
		wg.Go(func() {
			eff, err := actWithRetry(ctx, lc, e.act)
			results[i] = outcome{eff: eff, err: err}
		})
	}
	wg.Wait()

	// Phase 3: serial feedback in call order (completion order is discarded).
	// WaitInput wins over err (explicit intent, serial-path parity): the
	// first such result in call order suspends the loop. The suspending call
	// itself gets no AfterAct/EventToolResult — its result arrives via
	// Resume — but every sibling (before AND after it) is fed back first,
	// because phase 2 already ran the whole batch.
	suspendAt := -1
	for j := range batch {
		if suspendAt < 0 && results[j].eff != nil && results[j].eff.WaitInput != "" {
			suspendAt = j
			continue
		}
		if !toolFeedback(ctx, lc, batch[j].call, results[j].eff, results[j].err, yield) {
			return nil, false
		}
	}
	if suspendAt >= 0 {
		// remaining is nil: the batch fully executed, Resume must not replay.
		w, yOk := emitWait(lc, round, out, batch[suspendAt].call, nil, results[suspendAt].eff.WaitInput, yield)
		if !yOk {
			return nil, false
		}
		return w, true
	}
	return nil, true
}
