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
	"unicode/utf8"
)

// ErrMaxRounds is returned when the loop exhausts all rounds with pending tool calls.
var ErrMaxRounds = errors.New("nerve: max rounds exceeded")

// LoopState represents the current state of the decision loop.
type LoopState int

const (
	StateIdle     LoopState = iota // Idle (waiting for stimulus)
	StateThinking                  // Thinking (Thinker invoked)
	StateActing                    // Acting (Effector invoked)
	StatePaused                    // Paused (yielded by pause gate at gap points)
	StateWaiting                   // Waiting for external input (loop suspended, EventWaitInput)
	StateDone                      // Done (cycle completed)
	StateError                     // Error
)

// String returns the human-readable name of the loop state.
func (s LoopState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateThinking:
		return "thinking"
	case StateActing:
		return "acting"
	case StatePaused:
		return "paused"
	case StateWaiting:
		return "waiting"
	case StateDone:
		return "done"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// DefaultMaxRounds is the default round limit when MaxRounds <= 0.
const DefaultMaxRounds = 8

// LoopConfig is the scalar runtime configuration of the loop (host
// provided). Zero-value semantics: MaxRounds<=0 uses DefaultMaxRounds(8);
// MaxToolOutput<=0 disables truncation; MaxRetries<=0 disables Think retry;
// ToolTimeout<=0 disables per-tool timeouts; ToolMaxRetries<=0 disables tool
// retry; ParallelActs=false keeps strict serial tool execution (v1.3.2
// behavior). UpdateConfig swaps it wholesale; the next Stimulate/Resume snapshots
// the new values (an in-flight loop keeps the values it started with).
type LoopConfig struct {
	MaxRounds      int
	MaxToolOutput  int
	MaxRetries     int
	ToolTimeout    time.Duration // Per-tool execution timeout (<=0 = none)
	ToolMaxRetries int           // Tool retry count on effector error (<=0 = no retry)
	// ParallelActs executes a round's multiple tool calls concurrently
	// (serial gating → parallel Act → serial feedback in call order); a
	// single call always keeps the serial path. Opt-in prerequisite: the
	// Effector implementation must be safe for concurrent Act calls.
	ParallelActs bool
}

// PauseGate is the pause gate (optional; nil = pause unsupported).
// The loop checks it at gap points (before each Think and before each tool
// execution); a pending pause yields EventState(StatePaused) + EventPaused
// with a Session snapshot and ends the iterator normally — the host resumes
// via Resume(sess, "") (the unified suspension-resume path since v1.3.2;
// the old in-iterator blocking wait is gone).
type PauseGate struct {
	// IsPaused reports whether a pause has been requested.
	IsPaused func() bool
}

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

	// Structured tool feedback accumulated within this cycle (single track:
	// tool results no longer enter Context; rendering is the host's call).
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
	finalOutput = roundLoop(ctx, lc, 1, &out, yield)
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
	// Load the session: stimulus, plan, accumulated context and the
	// suspension response as the pending tool's structured result (single
	// track — rendering is the host's call), plus the accumulated output.
	lc.Input = sess.input
	lc.Plan = sess.plan
	lc.Context = slices.Clone(sess.context)
	lc.ToolResults = slices.Clone(sess.toolResults)
	// The response attaches to the pending tool's structured result — but
	// only for a tool suspension (ask_user). A pause-suspended session has
	// no pending tool (zero value) and resumes with an empty response:
	// nothing is injected, the loop just continues from the suspended round.
	if sess.pending.ID != "" {
		lc.ToolResults = append(lc.ToolResults, ToolResult{
			ID:     sess.pending.ID,
			Name:   sess.pending.Name,
			Result: truncateText(response, lc.MaxToolOutput),
		})
	}
	var out strings.Builder
	out.Grow(256)
	out.WriteString(sess.output)
	if !cyclePrelude(ctx, lc, yield) {
		return
	}
	// Finish the suspended round's remaining tool calls (same round, no new
	// Think) before re-entering the round loop. A tool that suspends again
	// yields a fresh EventWaitInput and ends the iterator normally.
	if len(sess.remaining) > 0 {
		lc.State = StateActing
		if !yield(Event{Kind: EventState, State: StateActing}) {
			return
		}
		waiting, ok := runToolCalls(ctx, lc, sess.remaining, sess.round, &out, yield)
		if !ok || waiting != nil {
			return
		}
	}
	finalOutput = roundLoop(ctx, lc, sess.round, &out, yield)
}

// cycleGuarantees returns the deferred cleanup shared by Cycle and Resume:
// OnCycleEnd and AfterStimulate are guaranteed exactly once per invocation —
// on normal completion, error path, suspension, or early consumer stop
// (yield=false). AfterStimulate is protected from an OnCycleEnd panic via a
// nested defer. finalOutput is dereferenced at cleanup time.
func cycleGuarantees(ctx context.Context, lc *LoopContext, finalOutput *string) func() {
	return func() {
		defer func() { lc.Hooks.AfterStimulate(ctx, *finalOutput) }()
		lc.Hooks.OnCycleEnd(ctx, *finalOutput)
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

// roundLoop runs the round loop from startRound up to the effective max
// rounds: pause gate, budget trim, Think, then Act. A round without tool
// calls completes the loop with StateDone + EventDone and returns the final
// accumulated output. A tool suspension ends the loop normally (the wait
// events were already yielded) without Done, returning "" — callers must not
// treat it as an error. Exhausting the cap with a pending tool call emits
// ErrMaxRounds. Errors are emitted inside; a consumer stop aborts silently.
func roundLoop(ctx context.Context, lc *LoopContext, startRound int, out *strings.Builder, yield func(Event) bool) (finalOutput string) {
	maxRounds := effectiveMaxRounds(lc.MaxRounds)
	var round int
	for round = startRound; round <= maxRounds; round++ {
		// Check ctx cancellation between rounds
		if cerr := ctx.Err(); cerr != nil {
			emitError(ctx, lc, yield, fmt.Errorf("nerve: cycle: %w", cerr))
			return ""
		}

		// Gap point: honor a pending pause before each Think
		if !waitIfPaused(ctx, lc, round, out, nil, yield) {
			return ""
		}

		dec, ok := thinkRound(ctx, lc, out, yield)
		if !ok {
			return ""
		}

		// No tool calls → end of cycle
		if len(dec.ToolCalls) == 0 {
			break
		}

		// Acting phase
		lc.State = StateActing
		if !yield(Event{Kind: EventState, State: StateActing}) {
			return ""
		}
		waiting, ok := runToolCalls(ctx, lc, dec.ToolCalls, round, out, yield)
		if !ok {
			return ""
		}
		if waiting != nil {
			// Loop suspended waiting for external input: the iterator ends
			// normally here; the host resumes via Resume(sess, response).
			return ""
		}
	}

	// Exhausted all rounds with a tool call pending in the last round — the
	// loop ended only because the hard cap was hit (a round without tool
	// calls breaks out below the cap).
	if round > maxRounds {
		emitError(ctx, lc, yield, ErrMaxRounds)
		return ""
	}

	// Done — the output is finalized before StateDone so OnCycleEnd still
	// receives it when the consumer stops at the done event.
	final := out.String()
	lc.State = StateDone
	if !yield(Event{Kind: EventState, State: StateDone}) {
		return final
	}
	if !yield(Event{Kind: EventDone, Output: final}) {
		return final
	}
	return final
}

// thinkRound runs one round's Think phase: context budget trimming, the
// StateThinking event, prompt assembly, Think with retry, AfterThink, and
// the text/usage events. Returns the decision (nil = the loop must end; an
// error was emitted or the consumer stopped).
func thinkRound(ctx context.Context, lc *LoopContext, out *strings.Builder, yield func(Event) bool) (*Decision, bool) {
	// Apply context budget trimming before each Think
	lc.Context = lc.Budget.Trimmer(lc.Context, lc.Budget.MaxTokens)

	lc.State = StateThinking
	if !yield(Event{Kind: EventState, State: StateThinking}) {
		return nil, false
	}

	// Build prompt
	p := &Prompt{
		System:      lc.System,
		Identity:    lc.Identity,
		Methods:     lc.Methods,
		Tools:       lc.Tools,
		Context:     lc.Context,
		Bounds:      lc.Bounds,
		Input:       lc.Input,
		State:       lc.State.String(),
		Plan:        lc.Plan,
		ToolResults: lc.ToolResults,
	}

	// BeforeThink hook
	if err := hookBeforeThink(ctx, lc, p); err != nil {
		emitError(ctx, lc, yield, err)
		return nil, false
	}

	// Think with retry
	dec, err := thinkWithRetry(ctx, lc, p)
	if err != nil {
		emitError(ctx, lc, yield, err)
		return nil, false
	}

	// AfterThink hook
	if err := hookAfterThink(ctx, lc, dec); err != nil {
		emitError(ctx, lc, yield, err)
		return nil, false
	}

	// Accumulate text output
	out.WriteString(dec.Text)
	if !yield(Event{Kind: EventText, Text: dec.Text}) {
		return nil, false
	}

	// Report token usage of this Think (nil = skip accounting)
	if dec.Usage != nil {
		if !yield(Event{Kind: EventUsage, Usage: dec.Usage}) {
			return nil, false
		}
	}
	return dec, true
}

// runToolCalls executes one tool call list (a round's calls or the calls
// remaining after a suspension): pause gate, sandbox gate, Act, feedback.
// Returns waiting non-nil when a tool suspended the loop — the framework
// already yielded StateWaiting + EventWaitInput, the caller must end the
// iterator normally (no Done, no error). ok=false means the loop must end
// (an error was emitted or the consumer stopped).
func runToolCalls(ctx context.Context, lc *LoopContext, calls []ToolCall, round int, out *strings.Builder, yield func(Event) bool) (waiting *WaitInput, ok bool) {
	// Opt-in batch parallelism (v1.3.3): a round's multiple calls execute
	// concurrently while events and hooks stay serial. A single call keeps
	// the serial path — zero behavior difference.
	if lc.ParallelActs && len(calls) > 1 {
		return runToolCallsParallel(ctx, lc, calls, round, out, yield)
	}
	for i, tc := range calls {
		if !yield(Event{Kind: EventToolCall, ToolCall: &tc}) {
			return nil, false
		}

		// Gap point: honor a pending pause before each tool execution; a
		// paused run snapshots the calls from this one on (the current tool
		// has not run yet) into the Session, so Resume runs them first.
		if !waitIfPaused(ctx, lc, round, out, calls[i:], yield) {
			return nil, false
		}

		eff, err, handled, ok := runOneTool(ctx, lc, tc, yield)
		if !ok {
			return nil, false
		}
		if handled {
			continue // denied: feedback already appended and yielded
		}

		// Suspension: the tool requests external input. Snapshot the loop
		// state into a Session, yield the wait events, and end the iterator
		// normally — the host resumes later via Resume(sess, response).
		// WaitInput wins over err (explicit intent); a nil effect never
		// suspends.
		if eff != nil && eff.WaitInput != "" {
			w := snapshotWait(lc, round, out, tc, calls[i+1:])
			w.Question = eff.WaitInput
			lc.State = StateWaiting
			if !yield(Event{Kind: EventState, State: StateWaiting}) {
				return nil, false
			}
			if !yield(Event{Kind: EventWaitInput, Wait: w}) {
				return nil, false
			}
			return w, true
		}

		// Compute feedback — structured track only: tool results no longer
		// enter the Context text track (rendering is the host's decision;
		// Context keeps host-injected base + sandbox denials).
		if !toolFeedback(ctx, lc, tc, eff, err, yield) {
			return nil, false
		}
	}
	return nil, true
}

// toolFeedback finalizes one executed tool call: the AfterAct hook, the
// truncated ToolResult appended to the structured track, and the
// EventToolResult yield. Shared by the serial and the parallel path. Returns
// false when the consumer stopped.
func toolFeedback(ctx context.Context, lc *LoopContext, tc ToolCall, eff *Effect, err error, yield func(Event) bool) bool {
	lc.Hooks.AfterAct(ctx, &Action{CellID: lc.CellID, Call: tc}, eff, err)
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
	return yield(Event{Kind: EventToolResult, Effect: eff, ToolCall: &tc})
}

// runOneTool gates and executes one tool call: the sandbox membrane (every
// decision yields an EventSandbox audit record), the BeforeAct hook, then
// Act with timeout/retry. handled=true means the call was denied — the
// denial was already appended to the Context text track and yielded as
// EventToolResult, the caller must skip its own feedback. ok=false means the
// loop must end (an error was emitted or the consumer stopped).
func runOneTool(ctx context.Context, lc *LoopContext, tc ToolCall, yield func(Event) bool) (eff *Effect, err error, handled bool, ok bool) {
	// Sandbox check: every decision of the membrane produces an EventSandbox
	// audit record (allowed or denied) before the tool runs.
	if reason, denied, sbErr := checkSandbox(ctx, lc, tc); denied {
		if !yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
			CellID: lc.CellID, Call: tc, Allowed: false, Reason: reason, Err: sbErr,
		}}) {
			return nil, nil, false, false
		}
		fb := fmt.Sprintf("[denied: %s]", reason)
		lc.Context = append(lc.Context, fb)
		if !yield(Event{Kind: EventToolResult, Effect: &Effect{Err: fb}, ToolCall: &tc}) {
			return nil, nil, false, false
		}
		return nil, nil, true, true
	}
	if !yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
		CellID: lc.CellID, Call: tc, Allowed: true,
	}}) {
		return nil, nil, false, false
	}

	act := Action{CellID: lc.CellID, Call: tc}

	// BeforeAct hook
	if err := hookBeforeAct(ctx, lc, &act); err != nil {
		emitError(ctx, lc, yield, err)
		return nil, nil, false, false
	}

	// Execute tool (with optional per-tool timeout and retry)
	eff, err = actWithRetry(ctx, lc, act)
	if eff == nil && err == nil {
		eff = &Effect{Err: "nil effect from effector"}
	}
	return eff, err, false, true
}

// effectiveMaxRounds returns DefaultMaxRounds if maxRounds <= 0.
func effectiveMaxRounds(maxRounds int) int {
	if maxRounds <= 0 {
		return DefaultMaxRounds
	}
	return maxRounds
}

// truncateText truncates a feedback payload to maxLen bytes, keeping UTF-8
// rune boundaries; appends a truncation marker. maxLen <= 0 = no truncation.
func truncateText(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	truncAt := maxLen
	for truncAt > 0 && !utf8.RuneStart(s[truncAt]) {
		truncAt--
	}
	if truncAt == 0 {
		// maxLen falls inside the first character; cut after the first rune.
		// Actual output may slightly exceed maxLen to keep UTF-8 intact.
		_, size := utf8.DecodeRuneInString(s)
		truncAt = size
	}
	return s[:truncAt] + fmt.Sprintf("[truncated, %d bytes total]", len(s))
}

// thinkWithRetry retries Think up to MaxRetries times; on ctx cancellation returns immediately.
func thinkWithRetry(ctx context.Context, lc *LoopContext, p *Prompt) (*Decision, error) {
	maxAttempts := lc.MaxRetries
	if maxAttempts < 0 {
		maxAttempts = 0 // negative config means "no retry", still execute once
	}
	var err error
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("nerve.thinkWithRetry: %w", cerr)
		}
		var dec *Decision
		dec, err = lc.Think.Think(ctx, p)
		if err == nil {
			if dec == nil {
				err = fmt.Errorf("nerve: thinker returned nil decision without error")
				continue
			}
			return dec, nil
		}
	}
	return nil, fmt.Errorf("nerve.thinkWithRetry: %w", err)
}

// actWithRetry executes a tool with an optional per-attempt timeout and retry
// on effector errors only (Effect.Err is a business error and is never
// retried — retrying could duplicate side effects). A ctx cancellation or a
// timeout-derived error is not retried. The final error flows through
// ToolResults.Err like any other tool failure (resistance is feedback).
func actWithRetry(ctx context.Context, lc *LoopContext, act Action) (*Effect, error) {
	maxAttempts := lc.ToolMaxRetries
	if maxAttempts < 0 {
		maxAttempts = 0 // negative config means "no retry", still execute once
	}
	var eff *Effect
	var err error
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		actCtx, cancel := ctx, func() {}
		if lc.ToolTimeout > 0 {
			actCtx, cancel = context.WithTimeout(ctx, lc.ToolTimeout)
		}
		eff, err = lc.Act.Act(actCtx, act)
		actErr := actCtx.Err() // capture BEFORE cancel: after cancel it is always non-nil
		cancel()               // released immediately; never deferred inside a retry loop
		if err == nil {
			return eff, nil
		}
		if ctx.Err() != nil || (lc.ToolTimeout > 0 && actErr != nil) {
			return eff, err // parent canceled or per-attempt timeout actually fired
		}
	}
	return eff, err
}

// waitIfPaused checks the pause gate at gap points. When a pause is pending
// it yields EventState(StatePaused) + EventPaused with a Session snapshot
// and ends the iterator normally (no Done, no Error) — the host resumes via
// Resume(sess, "") (the unified suspension-resume path since v1.3.2; the
// old blocking wait is gone). remaining holds the tool calls after a
// tool-gap pause point (nil at a Think-gap); a paused run snapshots it into
// the Session so Resume finishes them first. Returns false when the caller
// must return immediately (paused or the consumer stopped).
func waitIfPaused(ctx context.Context, lc *LoopContext, round int, out *strings.Builder, remaining []ToolCall, yield func(Event) bool) bool {
	if lc.Pause == nil || lc.Pause.IsPaused == nil || !lc.Pause.IsPaused() {
		return true
	}
	lc.State = StatePaused
	if !yield(Event{Kind: EventState, State: StatePaused}) {
		return false
	}
	w := snapshotWait(lc, round, out, ToolCall{}, remaining)
	if !yield(Event{Kind: EventPaused, Wait: w}) {
		return false
	}
	return false // iterator ends normally; the host resumes via Resume(sess, "")
}

// snapshotWait snapshots the loop state into a WaitInput handle — the
// unified suspension primitive shared by tool suspension (ask_user) and
// pause requests (v1.3.2): same Session shape, same Resume path. pending is
// the suspending tool (zero value for a pause), remaining the tool calls
// after the suspension point.
func snapshotWait(lc *LoopContext, round int, out *strings.Builder, pending ToolCall, remaining []ToolCall) *WaitInput {
	sess := Session{}.snapshot(round, lc.Input, lc.Plan, lc.Context, out.String(), pending, remaining, lc.ToolResults)
	return &WaitInput{CellID: lc.CellID, Call: pending, Session: sess}
}

// hookBeforeThink calls Hooks.BeforeThink (required).
func hookBeforeThink(ctx context.Context, lc *LoopContext, p *Prompt) error {
	if err := lc.Hooks.BeforeThink(ctx, p); err != nil {
		return fmt.Errorf("nerve.hookBeforeThink: %w", err)
	}
	return nil
}

// hookAfterThink calls Hooks.AfterThink (required).
func hookAfterThink(ctx context.Context, lc *LoopContext, d *Decision) error {
	if err := lc.Hooks.AfterThink(ctx, d); err != nil {
		return fmt.Errorf("nerve.hookAfterThink: %w", err)
	}
	return nil
}

// hookBeforeAct calls Hooks.BeforeAct (required).
func hookBeforeAct(ctx context.Context, lc *LoopContext, a *Action) error {
	if err := lc.Hooks.BeforeAct(ctx, a); err != nil {
		return fmt.Errorf("nerve.hookBeforeAct: %w", err)
	}
	return nil
}

// hookBeforeStimulate calls Hooks.BeforeStimulate (required).
// The hook receives a Prompt prototype; its content fields are written back
// to the LoopContext after the call, so the modifications apply to every
// round of the Stimulate (State and Bounds are framework-managed and not
// written back — Bounds carries the Sandbox snapshot read-only).
func hookBeforeStimulate(ctx context.Context, lc *LoopContext) error {
	proto := &Prompt{
		System:   lc.System,
		Identity: lc.Identity,
		Methods:  lc.Methods,
		Tools:    lc.Tools,
		// Shallow copy: the prototype gets its own backing array so that
		// in-place mutations (or an early hook error) never leak into lc.
		// The write-back below then adopts the prototype's slice wholesale.
		// ToolResults is copied likewise but is read-only: hosts inject
		// history via Context, the structured track stays framework-managed.
		Context:     append([]string(nil), lc.Context...),
		ToolResults: append([]ToolResult(nil), lc.ToolResults...),
		Bounds:      lc.Bounds,
		Input:       lc.Input,
		Plan:        lc.Plan,
		State:       lc.State.String(),
	}
	if err := lc.Hooks.BeforeStimulate(ctx, proto); err != nil {
		return fmt.Errorf("nerve.hookBeforeStimulate: %w", err)
	}
	// Write back content fields (State is overwritten by the loop each round).
	lc.System = proto.System
	lc.Identity = proto.Identity
	lc.Methods = proto.Methods
	lc.Tools = proto.Tools
	lc.Context = proto.Context
	lc.Input = proto.Input
	lc.Plan = proto.Plan
	return nil
}

// emitError sets the error state, yields EventState(StateError) + EventError,
// then calls OnError hook if set (only when events are delivered normally;
// a consumer abort does not trigger OnError).
// OnCycleEnd is guaranteed by Cycle's defer.
// NOTE: callers always return immediately after calling emitError.
func emitError(ctx context.Context, lc *LoopContext, yield func(Event) bool, err error) {
	lc.State = StateError
	if !yield(Event{Kind: EventState, State: StateError}) {
		return
	}
	if !yield(Event{Kind: EventError, Err: err}) {
		return
	}
	if lc.Hooks.OnError != nil {
		lc.Hooks.OnError(ctx, err)
	}
}

// checkSandbox evaluates the membrane policy for one tool call; returns the
// policy reason, whether the action is denied, and the evaluation error
// (nil = clean decision). The membrane is required — every decision is
// audited via EventSandbox.
func checkSandbox(ctx context.Context, lc *LoopContext, tc ToolCall) (reason string, denied bool, sbErr error) {
	act := Action{CellID: lc.CellID, Call: tc}
	allowed, reason, err := lc.Sandbox.Allow(ctx, act)
	if err != nil {
		return fmt.Sprintf("sandbox error: %v", err), true, err
	}
	if !allowed {
		return reason, true, nil
	}
	return "", false, nil
}
