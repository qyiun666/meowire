// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// parallel.go — ParallelActs batch execution path (v1.3.3, opt-in): one
// round's multiple tool calls run concurrently in three phases — serial
// gating, parallel Act, serial feedback. Events and hooks stay serial in
// call order throughout; only actWithRetry enters the concurrent phase.
package nerve

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// runToolCallsParallel executes one round's tool call batch in three phases:
//
//  1. Serial gating — announce every call (EventToolCall in call order),
//     one pause gap point for the whole batch, then per-call sandbox
//     verdict (EventSandbox audit) and BeforeAct. A denied call gets its
//     [denied: reason] feedback in place and is skipped — it never affects
//     its siblings.
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

	// Phase 1: serial gating — sandbox verdict + BeforeAct per call.
	admitted := make([]ToolCall, 0, len(calls))
	for i := range calls {
		tc := calls[i]
		if reason, denied, sbErr := checkSandbox(ctx, lc, tc); denied {
			if !yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
				CellID: lc.CellID, Call: tc, Allowed: false, Reason: reason, Err: sbErr,
			}}) {
				return nil, false
			}
			fb := fmt.Sprintf("[denied: %s]", reason)
			lc.Context = append(lc.Context, fb)
			if !yield(Event{Kind: EventToolResult, Effect: &Effect{Err: fb}, ToolCall: &tc}) {
				return nil, false
			}
			continue // denied in place; siblings proceed unaffected
		}
		if !yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
			CellID: lc.CellID, Call: tc, Allowed: true,
		}}) {
			return nil, false
		}
		act := Action{CellID: lc.CellID, Call: tc}
		if err := hookBeforeAct(ctx, lc, &act); err != nil {
			emitError(ctx, lc, yield, err)
			return nil, false
		}
		admitted = append(admitted, tc)
	}

	// Phase 2: parallel execution — only actWithRetry runs concurrently;
	// results land by call index so phase 3 stays deterministic. A ctx
	// cancellation aborts each goroutine naturally; the error flows through
	// feedback like any other tool failure.
	type outcome struct {
		eff *Effect
		err error
	}
	results := make([]outcome, len(admitted))
	var wg sync.WaitGroup
	for j := range admitted {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			eff, err := actWithRetry(ctx, lc, Action{CellID: lc.CellID, Call: admitted[j]})
			if eff == nil && err == nil {
				eff = &Effect{Err: "nil effect from effector"}
			}
			results[j] = outcome{eff: eff, err: err}
		}(j)
	}
	wg.Wait()

	// Phase 3: serial feedback in call order (completion order is discarded).
	// WaitInput wins over err (explicit intent, serial-path parity): the
	// first such result in call order suspends the loop. The suspending call
	// itself gets no AfterAct/EventToolResult — its result arrives via
	// Resume — but every sibling (before AND after it) is fed back first,
	// because phase 2 already ran the whole batch.
	suspendAt := -1
	for j := range admitted {
		if suspendAt < 0 && results[j].eff != nil && results[j].eff.WaitInput != "" {
			suspendAt = j
			continue
		}
		if !toolFeedback(ctx, lc, admitted[j], results[j].eff, results[j].err, yield) {
			return nil, false
		}
	}
	if suspendAt >= 0 {
		// remaining is nil: the batch fully executed, Resume must not replay.
		w := snapshotWait(lc, round, out, admitted[suspendAt], nil)
		w.Question = results[suspendAt].eff.WaitInput
		lc.State = StateWaiting
		if !yield(Event{Kind: EventState, State: StateWaiting}) {
			return nil, false
		}
		if !yield(Event{Kind: EventWaitInput, Wait: w}) {
			return nil, false
		}
		return w, true
	}
	return nil, true
}
