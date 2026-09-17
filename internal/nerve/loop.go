// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// loop.go — decision loop: pure orchestration, no default implementation.
package nerve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrMaxRounds is returned when the loop exhausts all rounds with pending tool calls.
var ErrMaxRounds = errors.New("nerve: max rounds exceeded")

// ErrForeignSession is returned when Resume is handed a Session produced by a
// different cell: the handle carries that cell's context, output and pending
// calls, and replaying it against another cell's organs would run one agent's
// half-finished round with another agent's brain and tools.
var ErrForeignSession = errors.New("nerve: session belongs to another cell")

// DecisionLoop is the pure orchestration engine.
type DecisionLoop struct{}

// Cycle runs the decision loop, yielding events to the caller.
// The yield function returns false to stop the loop early.
func (DecisionLoop) Cycle(ctx context.Context, lc *LoopContext, yield func(Event) bool) {
	var finalOutput string
	defer cycleGuarantees(ctx, lc, &finalOutput)()
	if !cyclePrelude(ctx, lc, yield) {
		return
	}
	var out strings.Builder
	out.Grow(256)
	b := newActBatch(ctx, lc, &out, yield)
	b.round = 1
	finalOutput = roundLoop(lc, 1, b)
}

// Resume continues a suspended loop from the Session captured in
// EventWaitInput: the external response is injected as the pending tool's
// structured result (a ToolResults entry), the remaining tool calls of the
// suspended round run first, then the round loop resumes
// from the suspended round — the Think that digests the response uses the
// suspended round's quota, so the suspension consumes no extra round. The
// event stream is isomorphic with Stimulate (same hooks, same guarantees). A
// zero-value Session is rejected with an error event.
func (DecisionLoop) Resume(ctx context.Context, lc *LoopContext, sess Session, response string, yield func(Event) bool) {
	var finalOutput string
	defer cycleGuarantees(ctx, lc, &finalOutput)()
	if !sess.valid() {
		emitError(ctx, lc, yield, fmt.Errorf("nerve: resume: invalid session"))
		return
	}
	if sess.cell != lc.CellID {
		emitError(ctx, lc, yield, fmt.Errorf("nerve: resume: %w (session from %q, resumed by %q)",
			ErrForeignSession, sess.cell, lc.CellID))
		return
	}
	// Load the session: stimulus, plan, accumulated context, plus the
	// accumulated output.
	lc.Input = sess.input
	lc.Plan = sess.plan
	lc.Context = slices.Clone(sess.context)
	lc.ToolResults = slices.Clone(sess.toolResults)
	lc.Requests = slices.Clone(sess.requests)
	// A tool wait consumes the response as the pending tool's structured result
	// (rendering is the host's call). A pause injects nothing: the loop just
	// continues. Both membrane asks resolve through the tri-state grammar below.
	if sess.kind == waitTool && sess.pending.ID != "" {
		lc.ToolResults = append(lc.ToolResults, ToolResult{
			ID:     sess.pending.ID,
			Name:   sess.pending.Name,
			Result: truncateText(response, lc.MaxToolOutput),
		})
	}
	var out strings.Builder
	out.Grow(256)
	out.WriteString(sess.output)
	b := newActBatch(ctx, lc, &out, yield)
	b.round = sess.round
	if !cyclePrelude(ctx, lc, yield) {
		return
	}

	switch sess.kind {
	case waitCallAsk:
		if !b.resolveCallAsk(sess, response) {
			return
		}
	case waitUtterance:
		done, ok := b.resolveUtteranceAsk(sess, response)
		if !ok {
			return
		}
		if done {
			finalOutput = announceDone(lc, b)
			return
		}
	}

	// Finish the suspended round's remaining tool calls (same round, no new
	// Think) before re-entering the round loop. A tool that suspends again
	// yields a fresh EventWaitInput and ends the iterator normally.
	if len(sess.remaining) > 0 {
		if lc.State != StateActing { // the approval arm already announced Acting
			lc.State = StateActing
			if !yield(Event{Kind: EventState, State: StateActing}) {
				return
			}
		}
		b.calls = slices.Clone(sess.remaining)
		waiting, ok := b.run()
		if !ok || waiting != nil {
			return
		}
	}
	finalOutput = roundLoop(lc, sess.round, b)
}

// cycleGuarantees returns the deferred cleanup shared by Cycle and Resume:
// answers to served peer requests, Remember, OnCycleEnd and AfterStimulate are
// guaranteed exactly once per invocation — on normal completion, error path,
// suspension, or early consumer stop (yield=false). Answers go first so the
// memory organ and the hooks observe the same terminal the requester does.
// Remember runs next so the organ sees the same terminal the hooks do and its
// own failure cannot rewrite it. AfterStimulate is
// protected from an OnCycleEnd panic via a nested defer. finalOutput is
// dereferenced at cleanup time.
func cycleGuarantees(ctx context.Context, lc *LoopContext, finalOutput *string) func() {
	return func() {
		defer func() { lc.Hooks.AfterStimulate(ctx, *finalOutput) }()
		lc.endWith(OutcomeAborted) // no terminal reached: consumer stopped early
		lc.answer(ctx, *finalOutput)
		lc.remember(ctx, *finalOutput)
		lc.Hooks.OnCycleEnd(ctx, *finalOutput, lc.outcome)
	}
}

// cyclePrelude runs the shared prologue of Cycle and Resume: snapshot the
// execution boundary once (before the BeforeStimulate hook so the prototype
// carries it read-only), fire BeforeStimulate exactly once before any event,
// check context cancellation, then emit pending EventReplace audits (the
// moment the swaps take effect — before any other event). Returns false when
// the caller must return immediately (an error was emitted or the consumer
// stopped).
func cyclePrelude(ctx context.Context, lc *LoopContext, yield func(Event) bool) bool {
	lc.Bounds = lc.Sandbox.Bounds()
	if err := hookBeforeStimulate(ctx, lc); err != nil {
		emitError(ctx, lc, yield, err)
		return false
	}
	if cerr := ctx.Err(); cerr != nil {
		emitError(ctx, lc, yield, fmt.Errorf("nerve: cycle: %w", cerr))
		return false
	}
	for _, ra := range lc.PendingReplace {
		if !yield(Event{Kind: EventReplace, Replace: &ra}) {
			return false
		}
	}
	for _, ca := range lc.PendingConfig {
		if !yield(Event{Kind: EventConfig, Config: &ca}) {
			return false
		}
	}
	return true
}

// roundLoop runs the round loop from startRound up to the effective cap:
// pause gate, Think, then Act. A round without tool calls completes the loop
// with StateDone + EventDone and returns the accumulated output. A tool
// suspension ends the loop normally (its wait events were already yielded)
// without Done, returning "" — callers must not read that as an error.
// Exhausting the cap with calls still pending emits ErrMaxRounds.
func roundLoop(lc *LoopContext, startRound int, b *actBatch) (finalOutput string) {
	maxRounds := effectiveMaxRounds(lc.MaxRounds)
	var round int
	for round = startRound; round <= maxRounds; round++ {
		b.round = round

		// Cancellation is checked between rounds.
		if cerr := b.ctx.Err(); cerr != nil {
			emitError(b.ctx, lc, b.yield, fmt.Errorf("nerve: cycle: %w", cerr))
			return ""
		}

		// Gap point: honor a pending pause before each Think (no calls at
		// stake yet, so the snapshot carries nothing to replay).
		if !b.pause(nil) {
			return ""
		}

		dec, ok := b.think()
		if !ok {
			return ""
		}

		// No tool calls → end of cycle
		if len(dec.ToolCalls) == 0 {
			break
		}

		lc.State = StateActing
		if !b.yield(Event{Kind: EventState, State: StateActing}) {
			return ""
		}
		b.calls = dec.ToolCalls
		waiting, ok := b.run()
		if !ok || waiting != nil {
			// Suspended for external input: the iterator ends normally and the
			// host resumes via Resume(sess, response).
			return ""
		}
	}
	return settleRound(lc, b, round)
}

// settleRound closes a finished round cycle: reaching the cap with calls still
// pending is ErrMaxRounds, anything else finalizes the output and announces
// Done. The output is read before StateDone so OnCycleEnd still receives it
// when the consumer stops at the done event.
func settleRound(lc *LoopContext, b *actBatch, round int) string {
	if round > effectiveMaxRounds(lc.MaxRounds) {
		lc.endWith(OutcomeMaxRounds)
		emitError(b.ctx, lc, b.yield, ErrMaxRounds)
		return ""
	}
	return announceDone(lc, b)
}

// announceDone stamps the Done terminal and yields the two closing events,
// returning the finalized output. The output is read before StateDone so
// OnCycleEnd still receives it when the consumer stops at the done event.
func announceDone(lc *LoopContext, b *actBatch) string {
	lc.endWith(OutcomeDone)
	final := b.out.String()
	lc.State = StateDone
	if !b.yield(Event{Kind: EventState, State: StateDone}) {
		return final
	}
	b.yield(Event{Kind: EventDone, Output: final})
	return final
}

// think runs one round's Think phase: context budget trimming, memory recall,
// the inbound drain, the StateThinking event, prompt assembly, Think with
// retry, AfterThink, the usage event, then the output membrane and the round's
// text. It returns the decision, or nil when the loop must end (an error was
// emitted, the membrane suspended for confirmation, or the consumer stopped).
func (b *actBatch) think() (*Decision, bool) {
	lc := b.lc

	if !b.prepare() {
		return nil, false
	}

	lc.State = StateThinking
	if !b.yield(Event{Kind: EventState, State: StateThinking}) {
		return nil, false
	}

	p := lc.buildPrompt()

	if err := hookBeforeThink(b.ctx, lc, p); err != nil {
		emitError(b.ctx, lc, b.yield, err)
		return nil, false
	}

	dec, err := thinkWithRetry(b.ctx, lc, p)
	if err != nil {
		emitError(b.ctx, lc, b.yield, err)
		return nil, false
	}

	if err := hookAfterThink(b.ctx, lc, dec); err != nil {
		emitError(b.ctx, lc, b.yield, err)
		return nil, false
	}

	// Report token usage of this Think (nil = skip accounting) before the
	// membrane gets the text: usage belongs to the Think, and an Ask ruling
	// ends the iterator here.
	if dec.Usage != nil {
		if !b.yield(Event{Kind: EventUsage, Usage: dec.Usage}) {
			return nil, false
		}
	}

	// Output membrane: nothing reaches the consumer (or the accumulated
	// output) before the ruling lands.
	if !b.utter(dec) {
		return nil, false
	}
	return dec, true
}

// prepare reads the three inbound tracks a round starts from, before its prompt
// is assembled: the regulator trims what accumulated, memory fills this round's
// recall, and the inbox yields what the colony sent since the last Think. Doing
// all three here is what lets the BeforeThink hook see (and rewrite) the round
// as the Thinker will. It returns false when a failing recall ends the cycle.
func (b *actBatch) prepare() bool {
	lc := b.lc
	lc.metabolize()
	if err := lc.recall(b.ctx); err != nil {
		emitError(b.ctx, lc, b.yield, err)
		return false
	}
	lc.ingest()
	return true
}
