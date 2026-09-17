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
	"time"
)

// ErrMaxRounds is returned when the loop exhausts all rounds with pending tool calls.
var ErrMaxRounds = errors.New("nerve: max rounds exceeded")

// LoopContext carries all data needed for a single Cycle invocation.
type LoopContext struct {
	// Identity
	CellID   string
	Identity string

	// Built-in capability description (gene projection, describes only)
	Methods []MethodSpec

	// Required ports
	Think   Thinker
	Act     Effector
	Hooks   *Hooks
	Sandbox Sandbox
	Budget  *ContextBudget
	Mem     Memory
	// Pause is framework-injected wiring (api layer), not a host port.
	Pause *PauseGate

	// Config
	MaxRounds      int           // Hard round limit (<=0 uses DefaultMaxRounds)
	MaxToolOutput  int           // Tool output truncation length (<=0 = no truncation)
	MaxRetries     int           // Think retry count (<=0 = no retry)
	ToolTimeout    time.Duration // Per-tool execution timeout (<=0 = no timeout)
	ToolMaxRetries int           // Tool retry count on effector error (<=0 = no retry)
	ParallelActs   bool          // Parallel batch execution (requires a concurrency-safe Effector)

	// Dynamic state
	State   LoopState
	Input   string
	Plan    string
	Context []string // Host-injected base + sandbox denials (tool results live in ToolResults)
	Bounds  string   // Sandbox.Bounds() snapshot, taken once per Stimulate

	// Structured tool feedback accumulated within this cycle (see ToolResult).
	ToolResults []ToolResult

	// PendingReplace: port swap audits recorded since the last Stimulate/
	// Resume, emitted as EventReplace before any other event (the swap takes
	// effect now — each Stimulate/Resume snapshots the ports it starts with).
	PendingReplace []ReplaceAudit

	// PendingConfig: config swap audits recorded since the last Stimulate/
	// Resume, emitted as EventConfig after EventReplace (the swap takes
	// effect now — each Stimulate/Resume snapshots the config it starts
	// with). Every "unique update" of the loop is traceable.
	PendingConfig []ConfigAudit

	// Host injected fixed parts
	System string
	Tools  []ToolSpec

	// Reflection slot (host injected via BeforeStimulate write-back /
	// base assembly): the Reflexion note passed through to every Think.
	Reflection string

	// Memories is this round's recall output: replaced wholesale before every
	// Think, never accumulated and never carried into a Session — a resumed
	// loop recalls against the round it resumes into.
	Memories []Record

	// outcome records how this invocation ended (first mark wins; zero =
	// nothing terminal reached = consumer abort). Delivered by OnCycleEnd.
	outcome CycleOutcome
}

// endWith records how the invocation ends; the first mark wins so a later
// generic error can never overwrite a more specific terminal (e.g. the
// MaxRounds classification of an ErrMaxRounds emission).
func (lc *LoopContext) endWith(o CycleOutcome) {
	if lc.outcome == 0 {
		lc.outcome = o
	}
}

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
	// Load the session: stimulus, plan, accumulated context, plus the
	// accumulated output.
	lc.Input = sess.input
	lc.Plan = sess.plan
	lc.Context = slices.Clone(sess.context)
	lc.ToolResults = slices.Clone(sess.toolResults)
	// The response attaches to the pending tool — the meaning depends on the
	// suspension flavor. ask_user: the response IS the tool's structured
	// result (single track — rendering is the host's call). A
	// pause-suspended session has no pending tool (zero value) and resumes
	// with an empty response: nothing is injected, the loop just continues.
	// Sandbox ask: the response resolves the confirmation below.
	if sess.pending.ID != "" && !sess.sandboxAsk {
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

	// Sandbox-ask flavor: resolve the tri-state confirmation. An empty
	// response denies as "declined", a "[denied: ...]" payload denies with
	// that text, anything else approves the pending call through the standard
	// admitted pipeline without re-gating (a stateless membrane would re-ask
	// forever). Either arm emits the terminal EventSandbox that closes the ask
	// chain; neither consumes a round.
	if sess.pending.ID != "" && sess.sandboxAsk {
		resp := strings.TrimSpace(response)
		ruling, fb := VerdictDeny, "[sandbox-denied: declined]"
		switch {
		case resp == "":
		case strings.HasPrefix(resp, "[denied:"):
			fb = "[sandbox-denied" + strings.TrimPrefix(resp, "[denied")
		default:
			ruling, fb = VerdictAllow, resp
		}
		if !yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
			CellID: lc.CellID, Call: sess.pending, Ruling: ruling, Reason: fb,
		}}) {
			return
		}
		if ruling == VerdictAllow {
			lc.State = StateActing
			if !yield(Event{Kind: EventState, State: StateActing}) {
				return
			}
			waiting, ok := b.admitted(sess.pending, slices.Clone(sess.remaining))
			if !ok || waiting != nil {
				return
			}
		} else if !b.deny(sess.pending, fb) {
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
// Remember, OnCycleEnd and AfterStimulate are guaranteed exactly once per
// invocation — on normal completion, error path, suspension, or early consumer
// stop (yield=false). Remember runs first so the organ sees the same terminal
// the hooks do and its own failure cannot rewrite it. AfterStimulate is
// protected from an OnCycleEnd panic via a nested defer. finalOutput is
// dereferenced at cleanup time.
func cycleGuarantees(ctx context.Context, lc *LoopContext, finalOutput *string) func() {
	return func() {
		defer func() { lc.Hooks.AfterStimulate(ctx, *finalOutput) }()
		lc.endWith(OutcomeAborted) // no terminal reached: consumer stopped early
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
// the StateThinking event, prompt assembly, Think with retry, AfterThink, then
// the text and usage events. It returns the decision, or nil when the loop must
// end (an error was emitted or the consumer stopped).
func (b *actBatch) think() (*Decision, bool) {
	lc := b.lc

	// Apply the regulator to both accumulating tracks before each Think
	lc.metabolize()

	// Fill this round's memory track before the prompt is assembled, so the
	// BeforeThink hook sees what was recalled and may overwrite it.
	if err := lc.recall(b.ctx); err != nil {
		emitError(b.ctx, lc, b.yield, err)
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

	b.out.WriteString(dec.Text)
	if !b.yield(Event{Kind: EventText, Text: dec.Text}) {
		return nil, false
	}

	// Report token usage of this Think (nil = skip accounting)
	if dec.Usage != nil {
		if !b.yield(Event{Kind: EventUsage, Usage: dec.Usage}) {
			return nil, false
		}
	}
	return dec, true
}
