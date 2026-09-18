// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// feedback.go — one round's execution handle: the tool-call batch, its
// structured feedback, and the suspension any step may end with.
package nerve

import (
	"context"
	"fmt"
	"strings"
)

// actBatch is one round's execution handle: the calls under way, the round
// they belong to, the output accumulator and the consumer's yield. Every Act
// phase step is a method on it, so a suspension always carries the same tail
// its batch started from and no step drags a parameter convoy.
// An empty calls slice is legitimate: a Think-gap pause has no calls yet.
type actBatch struct {
	ctx   context.Context
	lc    *LoopContext
	calls []ToolCall
	round int
	out   *strings.Builder
	yield func(Event) bool
}

// newActBatch binds an execution pass to one loop context. round is set by
// the caller before each pass (roundLoop per round, Resume at the suspended
// round).
func newActBatch(ctx context.Context, lc *LoopContext, out *strings.Builder, yield func(Event) bool) *actBatch {
	return &actBatch{ctx: ctx, lc: lc, out: out, yield: yield}
}

// run executes b.calls: pause gate, sandbox gate, Act, feedback, per call.
// It returns waiting non-nil when a tool suspended the loop — the wait events
// were already yielded and the caller must end the iterator normally (no Done,
// no error). ok=false means an error was emitted or the consumer stopped.
func (b *actBatch) run() (waiting *WaitInput, ok bool) {
	// Batch parallelism is opt-in and never applies to a single call, which
	// keeps the serial path byte-identical.
	if b.lc.ParallelActs && len(b.calls) > 1 {
		return b.runParallel()
	}
	for i, tc := range b.calls {
		if !b.yield(Event{Kind: EventToolCall, ToolCall: &tc}) {
			return nil, false
		}

		// Gap point before each execution: a paused run snapshots the calls
		// from this one on (the current tool has not run), so Resume runs
		// them first.
		if !b.pause(b.calls[i:]) {
			return nil, false
		}

		g := b.gate(tc)
		if !g.ok {
			return nil, false
		}
		switch g.ruling {
		case VerdictAsk:
			w, yOk := b.suspend(suspension{pending: tc, remaining: b.calls[i+1:], question: g.question, kind: WaitCallAsk})
			if !yOk {
				return nil, false
			}
			return w, true
		case VerdictDeny:
			if !b.refuse(tc, deniedText(g.reason)) {
				return nil, false
			}
			continue // refused by the membrane: feedback landed in place
		}
		waiting, ok := b.admitted(tc, b.calls[i+1:])
		if !ok {
			return nil, false
		}
		if waiting != nil {
			return waiting, true
		}
	}
	return nil, true
}

// reportOver records why the framework stopped a call, ahead of any failure text
// the organ already wrote: the framework's reason is what explains the round, and
// a truncated tool error must not crowd it out.
func reportOver(eff *Effect, reason string) {
	if eff.Err == "" {
		eff.Err = reason
		return
	}
	eff.Err = reason + "; " + eff.Err
}

// refuseLaterWait drops a wait that arrived after the round spent its single
// suspension on an earlier call of the batch. The snapshot replays no completed
// calls, so this one is refused before it is delivered — as tool feedback, the
// same shape as every other resistance — rather than coming back as a silent
// empty success that nothing will ever answer.
func refuseLaterWait(eff *Effect, pending ToolCall) {
	if eff == nil || eff.WaitInput == "" {
		return
	}
	eff.WaitInput = ""
	reportOver(eff, fmt.Sprintf("one wait per round: %s already waits", pending.Name))
}

// admitted runs one call that cleared the membrane: BeforeAct (a mutated
// Action is what executes), actWithRetry, then either a suspension — the tool
// asked for external input, and the tail rides the Session — or structured
// feedback. Shared by the serial path and the Resume approval arm.
func (b *actBatch) admitted(tc ToolCall, tail []ToolCall) (*WaitInput, bool) {
	act := Action{CellID: b.lc.CellID, Call: tc}
	if err := hookBeforeAct(b.ctx, b.lc, &act); err != nil {
		emitError(b.ctx, b.lc, b.yield, err)
		return nil, false
	}
	eff, err := actWithRetry(b.ctx, b.lc, act)

	// A tool-requested wait wins over err (explicit intent) and a nil effect
	// never suspends. The suspending call gets no AfterAct/EventToolResult —
	// its result arrives through Resume.
	if eff != nil && eff.WaitInput != "" {
		w, yOk := b.suspend(suspension{pending: tc, remaining: tail, question: eff.WaitInput, kind: WaitTool})
		if !yOk {
			return nil, false
		}
		return w, true
	}

	if !b.feedback(tc, eff, err) {
		return nil, false
	}
	return nil, true
}

// feedback finalizes one executed call: AfterAct, the truncated entry on the
// structured track, and the EventToolResult yield. Shared by the serial and
// the parallel path. Returns false when the consumer stopped.
func (b *actBatch) feedback(tc ToolCall, eff *Effect, err error) bool {
	lc := b.lc
	lc.Hooks.AfterAct(b.ctx, &Action{CellID: lc.CellID, Call: tc}, eff, err)
	tr := ToolResult{ID: tc.ID, Name: tc.Name}
	switch {
	case err != nil:
		tr.Err = truncateText(err.Error(), lc.MaxToolOutput)
	case eff.Err != "":
		tr.Err = truncateText(eff.Err, lc.MaxToolOutput)
	default:
		tr.Result = truncateText(eff.Result, lc.MaxToolOutput)
	}
	lc.ToolResults = append(lc.ToolResults, tr)
	return b.yield(Event{Kind: EventToolResult, Effect: eff, ToolCall: &tc})
}
