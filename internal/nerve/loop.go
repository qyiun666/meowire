// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// loop.go — decision loop: pure orchestration, no default implementation.
package nerve

import (
	"context"
	"errors"
	"fmt"
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

// PauseGate is the pause gate (optional; nil = pause unsupported).
// The loop checks it at gap points (before each Think and before each tool
// execution); a pending pause yields EventState(StatePaused) and blocks
// until ResumeCh is closed or ctx is canceled.
type PauseGate struct {
	// IsPaused reports whether a pause has been requested.
	IsPaused func() bool
	// ResumeCh returns the latest resume notification channel
	// (closed = resumed); fetched at each pause so a pause request issued
	// after the gate was built is still honored.
	ResumeCh func() <-chan struct{}
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

	// Dynamic state
	State   LoopState
	Input   string
	Plan    string
	Context []string // Host injected constant context + tool result accumulation within cycle
	Bounds  string   // Sandbox.Bounds() snapshot, taken once per Stimulate

	// Host injected fixed parts
	System string
	Tools  []ToolSpec
}

// DecisionLoop is the pure orchestration engine.
type DecisionLoop struct{}

// Cycle runs the decision loop, yielding events to the caller.
// The yield function returns false to stop the loop early.
func (DecisionLoop) Cycle(ctx context.Context, lc *LoopContext, yield func(Event) bool) {
	maxRounds := effectiveMaxRounds(lc.MaxRounds)
	var out strings.Builder
	out.Grow(256)
	var finalOutput string

	// OnCycleEnd and AfterStimulate are guaranteed exactly once per Cycle —
	// on normal completion, error path, or early consumer stop (yield=false).
	// AfterStimulate is protected from an OnCycleEnd panic via a nested defer.
	defer func() {
		defer func() { lc.Hooks.AfterStimulate(ctx, finalOutput) }()
		lc.Hooks.OnCycleEnd(ctx, finalOutput)
	}()

	// Snapshot the execution boundary once per Stimulate, before the
	// BeforeStimulate hook so the prototype carries it (read-only).
	lc.Bounds = lc.Sandbox.Bounds()

	// BeforeStimulate hook: once per Stimulate, before any event is yielded.
	// An error terminates the whole Stimulate without entering the loop.
	// It runs before the ctx check so it fires exactly once even on a
	// canceled context (AfterStimulate is already guaranteed by the defer).
	if err := hookBeforeStimulate(ctx, lc); err != nil {
		emitError(ctx, lc, yield, err)
		return
	}
	if cerr := ctx.Err(); cerr != nil {
		emitError(ctx, lc, yield, fmt.Errorf("nerve: cycle: %w", cerr))
		return
	}

	var round int
	for round = 1; round <= maxRounds; round++ {
		// Check ctx cancellation between rounds
		if cerr := ctx.Err(); cerr != nil {
			emitError(ctx, lc, yield, fmt.Errorf("nerve: cycle: %w", cerr))
			return
		}

		// Gap point: honor a pending pause before each Think
		if !waitIfPaused(ctx, lc, yield) {
			return
		}

		// Apply context budget trimming before each Think
		lc.Context = lc.Budget.Trimmer(lc.Context, lc.Budget.MaxTokens)

		// Thinking phase
		lc.State = StateThinking
		if !yield(Event{Kind: EventState, State: StateThinking}) {
			return
		}

		// Build prompt
		p := &Prompt{
			System:   lc.System,
			Identity: lc.Identity,
			Methods:  lc.Methods,
			Tools:    lc.Tools,
			Context:  lc.Context,
			Bounds:   lc.Bounds,
			Input:    lc.Input,
			State:    lc.State.String(),
			Plan:     lc.Plan,
		}

		// BeforeThink hook
		if err := hookBeforeThink(ctx, lc, p); err != nil {
			emitError(ctx, lc, yield, err)
			return
		}

		// Think with retry
		dec, err := thinkWithRetry(ctx, lc, p)
		if err != nil {
			emitError(ctx, lc, yield, err)
			return
		}

		// AfterThink hook
		if err := hookAfterThink(ctx, lc, dec); err != nil {
			emitError(ctx, lc, yield, err)
			return
		}

		// Accumulate text output
		out.WriteString(dec.Text)
		if !yield(Event{Kind: EventText, Text: dec.Text}) {
			return
		}

		// Report token usage of this Think (nil = skip accounting)
		if dec.Usage != nil {
			if !yield(Event{Kind: EventUsage, Usage: dec.Usage}) {
				return
			}
		}

		// No tool calls → end of cycle
		if len(dec.ToolCalls) == 0 {
			break
		}

		// Acting phase
		lc.State = StateActing
		if !yield(Event{Kind: EventState, State: StateActing}) {
			return
		}

		for _, tc := range dec.ToolCalls {
			if !yield(Event{Kind: EventToolCall, ToolCall: &tc}) {
				return
			}

			// Gap point: honor a pending pause before each tool execution
			if !waitIfPaused(ctx, lc, yield) {
				return
			}

			// Sandbox check: every decision of the membrane produces an
			// EventSandbox audit record (allowed or denied) before the tool runs.
			if reason, denied, sbErr := checkSandbox(ctx, lc, tc); denied {
				if !yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
					CellID: lc.CellID, Call: tc, Allowed: false, Reason: reason, Err: sbErr,
				}}) {
					return
				}
				fb := fmt.Sprintf("[denied: %s]", reason)
				lc.Context = append(lc.Context, fb)
				if !yield(Event{Kind: EventToolResult, Effect: &Effect{Err: fb}}) {
					return
				}
				continue
			}
			if !yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
				CellID: lc.CellID, Call: tc, Allowed: true,
			}}) {
				return
			}

			act := Action{CellID: lc.CellID, Call: tc}

			// BeforeAct hook
			if err := hookBeforeAct(ctx, lc, &act); err != nil {
				emitError(ctx, lc, yield, err)
				return
			}

			// Execute tool (with optional per-tool timeout and retry)
			eff, err := actWithRetry(ctx, lc, act)
			if eff == nil && err == nil {
				eff = &Effect{Err: "nil effect from effector"}
			}

			// Compute feedback
			fb := toolFeedback(tc, eff, err, lc.MaxToolOutput)

			// AfterAct hook
			lc.Hooks.AfterAct(ctx, &act, eff, err)

			// Append feedback to context
			lc.Context = append(lc.Context, fb)

			if !yield(Event{Kind: EventToolResult, Effect: eff}) {
				return
			}
		}
	}

	// Exhausted all rounds with a tool call pending in the last round — the
	// loop ended only because the hard cap was hit (a round without tool
	// calls breaks out below the cap).
	if round > maxRounds {
		emitError(ctx, lc, yield, ErrMaxRounds)
		return
	}

	// Done
	finalOutput = out.String()
	lc.State = StateDone
	if !yield(Event{Kind: EventState, State: StateDone}) {
		return
	}
	if !yield(Event{Kind: EventDone, Output: finalOutput}) {
		return
	}
	// OnCycleEnd hook: guaranteed by Cycle's defer above.
}

// effectiveMaxRounds returns DefaultMaxRounds if maxRounds <= 0.
func effectiveMaxRounds(maxRounds int) int {
	if maxRounds <= 0 {
		return DefaultMaxRounds
	}
	return maxRounds
}

// toolFeedback formats tool result/error as feedback string; truncates to maxLen if > 0.
func toolFeedback(tc ToolCall, eff *Effect, err error, maxLen int) string {
	var fb string
	switch {
	case err != nil:
		fb = "[" + tc.Name + "] error: " + err.Error()
	case eff.Err != "":
		fb = "[" + tc.Name + "] error: " + eff.Err
	default:
		fb = "[" + tc.Name + "] " + eff.Result
	}
	if maxLen > 0 && len(fb) > maxLen {
		truncAt := maxLen
		for truncAt > 0 && !utf8.RuneStart(fb[truncAt]) {
			truncAt--
		}
		if truncAt == 0 {
			// maxLen falls inside the first character; cut after the first rune.
			// Actual output may slightly exceed maxLen to keep UTF-8 intact.
			_, size := utf8.DecodeRuneInString(fb)
			truncAt = size
		}
		fb = fb[:truncAt] + fmt.Sprintf("[truncated, %d bytes total]", len(fb))
	}
	return fb
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
// toolFeedback like any other tool failure (resistance is feedback).
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

// waitIfPaused checks the pause gate at gap points. When a pause is pending it
// yields EventState(StatePaused) and blocks until ResumeCh is closed, ctx is
// canceled (error path), or the consumer stops the loop. Returns false when
// the caller must return immediately.
func waitIfPaused(ctx context.Context, lc *LoopContext, yield func(Event) bool) bool {
	if lc.Pause == nil || lc.Pause.IsPaused == nil || !lc.Pause.IsPaused() {
		return true
	}
	if !yield(Event{Kind: EventState, State: StatePaused}) {
		return false
	}
	if lc.Pause.ResumeCh == nil {
		return true
	}
	resume := lc.Pause.ResumeCh()
	if resume == nil {
		return true
	}
	select {
	case <-resume:
		return true
	case <-ctx.Done():
		emitError(ctx, lc, yield, fmt.Errorf("nerve: cycle: %w", ctx.Err()))
		return false
	}
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
		Context: append([]string(nil), lc.Context...),
		Bounds:  lc.Bounds,
		Input:   lc.Input,
		Plan:    lc.Plan,
		State:   lc.State.String(),
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
