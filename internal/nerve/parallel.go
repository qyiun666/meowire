// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// parallel.go — the opt-in batch path (LoopConfig.ParallelActs): one round's
// tool calls run concurrently in three phases — serial gating, parallel Act,
// serial feedback. Events and hooks stay serial in call order throughout;
// only the execution phase is concurrent.
package nerve

import (
	"sync"
)

// batchEntry keeps both identities of an admitted call: the call as announced
// (feedback and snapshot identity, identical to the serial path) and the gated
// Action (execution payload, carrying BeforeAct mutations).
type batchEntry struct {
	call ToolCall
	act  Action
}

// batchOutcome is one goroutine's result, indexed by call position so the
// feedback phase stays in call order regardless of completion order.
type batchOutcome struct {
	eff *Effect
	err error
}

// runParallel executes b.calls in the three phases described above. Suspension
// mirrors the serial path with two batch-specific differences documented on
// feedbackPhase.
func (b *actBatch) runParallel() (waiting *WaitInput, ok bool) {
	// Announce every call first (event order = call order).
	for i := range b.calls {
		if !b.yield(Event{Kind: EventToolCall, ToolCall: &b.calls[i]}) {
			return nil, false
		}
	}

	// One pause gap point before the batch: a paused run snapshots the whole
	// batch unexecuted, so Resume replays it.
	if !b.pause(b.calls) {
		return nil, false
	}

	batch, w, stop := b.gatePhase()
	if stop {
		return nil, false
	}
	if w != nil {
		return w, true
	}
	return b.feedbackPhase(batch, b.execPhase(batch))
}

// gatePhase runs the shared membrane gate per call. A denied call gets its
// [sandbox-denied: reason] feedback in place and is skipped — it never affects
// its siblings. The FIRST Ask stops everything: no further gating and no
// execution phase, so already-admitted siblings and the not-yet-gated tail all
// ride the snapshot (finalized denials stay out, their feedback already
// landed). suspended is non-nil when the batch suspends on an ask; stop means
// the iterator must end (an error was emitted or the consumer stopped).
func (b *actBatch) gatePhase() (batch []batchEntry, suspended *WaitInput, stop bool) {
	batch = make([]batchEntry, 0, len(b.calls))
	denied := make([]bool, len(b.calls))
	for i, tc := range b.calls {
		if notice, blocked := b.lc.inhibited(tc.Name); blocked {
			if !b.refuse(tc, inhibitedText(notice)) {
				return nil, nil, true
			}
			denied[i] = true
			continue // withheld by a notice: its feedback already landed
		}
		g := b.gate(tc)
		if !g.ok {
			return nil, nil, true
		}
		switch g.ruling {
		case VerdictAsk:
			tail := make([]ToolCall, 0, len(b.calls)-1)
			for j, other := range b.calls {
				if j != i && !denied[j] {
					tail = append(tail, other)
				}
			}
			w, yOk := b.suspend(suspension{pending: tc, remaining: tail, question: g.question, kind: waitCallAsk})
			return nil, w, !yOk
		case VerdictDeny:
			if !b.refuse(tc, deniedText(g.reason)) {
				return nil, nil, true
			}
			denied[i] = true
		default:
			// BeforeAct fires here (serial, outside the concurrent phase) so
			// its mutations are part of what the execution phase runs.
			if err := hookBeforeAct(b.ctx, b.lc, &g.act); err != nil {
				emitError(b.ctx, b.lc, b.yield, err)
				return nil, nil, true
			}
			batch = append(batch, batchEntry{call: tc, act: g.act})
		}
	}
	return batch, nil, false
}

// execPhase runs one goroutine per admitted call. A ctx cancellation aborts
// each goroutine naturally and the error flows through feedback like any other
// tool failure. Hooks, events and loop state never enter here: the goroutines
// read the batch's config and ports read-only.
func (b *actBatch) execPhase(batch []batchEntry) []batchOutcome {
	results := make([]batchOutcome, len(batch))
	var wg sync.WaitGroup
	for i, e := range batch {
		wg.Go(func() {
			eff, err := actWithRetry(b.ctx, b.lc, e.act)
			results[i] = batchOutcome{eff: eff, err: err}
		})
	}
	wg.Wait()
	return results
}

// feedbackPhase finalizes executed calls in call order, discarding completion
// order. A WaitInput wins over err (serial-path parity) and the first such
// result suspends the loop: the suspending call gets no AfterAct/EventToolResult
// because its result arrives via Resume, while every sibling — before AND after
// it — is fed back first, since the execution phase already ran the whole
// batch. That is also why the snapshot carries no remaining calls: replaying a
// completed batch would duplicate side effects.
func (b *actBatch) feedbackPhase(batch []batchEntry, results []batchOutcome) (*WaitInput, bool) {
	suspendAt := -1
	for j := range batch {
		b.delegate(results[j].eff)
		if suspendAt < 0 && results[j].eff != nil && results[j].eff.WaitInput != "" {
			suspendAt = j
			continue
		}
		if !b.feedback(batch[j].call, results[j].eff, results[j].err) {
			return nil, false
		}
	}
	if suspendAt < 0 {
		return nil, true
	}
	w, yOk := b.suspend(suspension{pending: batch[suspendAt].call, question: results[suspendAt].eff.WaitInput, kind: waitTool})
	if !yOk {
		return nil, false
	}
	return w, true
}
