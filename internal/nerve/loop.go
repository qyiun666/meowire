// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// loop.go — decision loop: pure orchestration, no default implementation.
package nerve

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	StatePaused                    // Reserved for future pause/resume support
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

// LoopContext carries all data needed for a single Cycle invocation.
type LoopContext struct {
	// Identity
	CellID   string
	Identity Identity

	// Required ports
	Think Thinker
	Act   Effector

	// Optional ports (nil = skip)
	Hooks   *Hooks
	Sandbox Sandbox
	Budget  *ContextBudget

	// Config
	MaxRounds     int // Hard round limit (<=0 uses DefaultMaxRounds)
	MaxToolOutput int // Tool output truncation length (<=0 = no truncation)
	MaxRetries    int // Think retry count (<=0 = no retry)

	// Dynamic state
	State   LoopState
	Input   string
	Plan    string
	Context []string // Host injected constant context + tool result accumulation within cycle

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

	// OnCycleEnd is guaranteed exactly once per Cycle — on normal completion,
	// error path, or early consumer stop (yield=false).
	defer func() { hookOnCycleEnd(ctx, lc, finalOutput) }()

	hadToolCalls := false

	var round int
	for round = 1; round <= maxRounds; round++ {
		// Check ctx cancellation between rounds
		if cerr := ctx.Err(); cerr != nil {
			emitError(ctx, lc, yield, fmt.Errorf("nerve: cycle: %w", cerr))
			return
		}

		// Apply context budget trimming before each Think
		if lc.Budget != nil && lc.Budget.Trimmer != nil && lc.Budget.MaxTokens > 0 {
			lc.Context = lc.Budget.Trimmer(lc.Context, lc.Budget.MaxTokens)
		}

		// Thinking phase
		lc.State = StateThinking
		if !yield(Event{Kind: EventState, State: StateThinking}) {
			return
		}

		// Build prompt
		p := &Prompt{
			System:   lc.System,
			Identity: lc.Identity,
			Tools:    lc.Tools,
			Context:  lc.Context,
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
			hadToolCalls = false
			break
		}
		hadToolCalls = true

		// Acting phase
		lc.State = StateActing
		if !yield(Event{Kind: EventState, State: StateActing}) {
			return
		}

		for _, tc := range dec.ToolCalls {
			if !yield(Event{Kind: EventToolCall, ToolCall: &tc}) {
				return
			}

			// Sandbox check
			if reason, denied := checkSandbox(ctx, lc, tc); denied {
				fb := fmt.Sprintf("[denied: %s]", reason)
				lc.Context = append(lc.Context, fb)
				if !yield(Event{Kind: EventToolResult, Effect: &Effect{Err: fb}}) {
					return
				}
				continue
			}

			act := Action{CellID: lc.CellID, Call: tc}

			// BeforeAct hook
			if err := hookBeforeAct(ctx, lc, &act); err != nil {
				emitError(ctx, lc, yield, err)
				return
			}

			// Execute tool
			eff, err := lc.Act.Act(ctx, act)
			if eff == nil && err == nil {
				eff = &Effect{Err: "nil effect from effector"}
			}

			// Compute feedback
			fb := toolFeedback(tc, eff, err, lc.MaxToolOutput)

			// AfterAct hook
			hookAfterAct(ctx, lc, &act, eff)

			// Append feedback to context
			lc.Context = append(lc.Context, fb)

			if !yield(Event{Kind: EventToolResult, Effect: eff}) {
				return
			}
		}
	}

	// If the last round had tool calls and we exhausted rounds, it's an error
	if round > maxRounds && hadToolCalls {
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
	case eff == nil:
		fb = "[" + tc.Name + "] error: nil effect"
	case eff.Err != "":
		fb = "[" + tc.Name + "] error: " + eff.Err
	default:
		fb = "[" + tc.Name + "] " + eff.Result
	}
	if maxLen > 0 && len(fb) > maxLen {
		truncAt := maxLen
		for truncAt > 0 && (fb[truncAt]&0xC0) == 0x80 {
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
	var err error
	for attempt := 0; attempt <= lc.MaxRetries; attempt++ {
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

// hookBeforeThink calls Hooks.BeforeThink if set.
func hookBeforeThink(ctx context.Context, lc *LoopContext, p *Prompt) error {
	if lc.Hooks == nil || lc.Hooks.BeforeThink == nil {
		return nil
	}
	if err := lc.Hooks.BeforeThink(ctx, p); err != nil {
		return fmt.Errorf("nerve.hookBeforeThink: %w", err)
	}
	return nil
}

// hookAfterThink calls Hooks.AfterThink if set.
func hookAfterThink(ctx context.Context, lc *LoopContext, d *Decision) error {
	if lc.Hooks == nil || lc.Hooks.AfterThink == nil {
		return nil
	}
	if err := lc.Hooks.AfterThink(ctx, d); err != nil {
		return fmt.Errorf("nerve.hookAfterThink: %w", err)
	}
	return nil
}

// hookBeforeAct calls Hooks.BeforeAct if set.
func hookBeforeAct(ctx context.Context, lc *LoopContext, a *Action) error {
	if lc.Hooks == nil || lc.Hooks.BeforeAct == nil {
		return nil
	}
	if err := lc.Hooks.BeforeAct(ctx, a); err != nil {
		return fmt.Errorf("nerve.hookBeforeAct: %w", err)
	}
	return nil
}

// hookAfterAct calls Hooks.AfterAct if set.
func hookAfterAct(ctx context.Context, lc *LoopContext, a *Action, e *Effect) {
	if lc.Hooks == nil || lc.Hooks.AfterAct == nil {
		return
	}
	lc.Hooks.AfterAct(ctx, a, e)
}

// hookOnCycleEnd calls Hooks.OnCycleEnd if set.
func hookOnCycleEnd(ctx context.Context, lc *LoopContext, output string) {
	if lc.Hooks == nil || lc.Hooks.OnCycleEnd == nil {
		return
	}
	lc.Hooks.OnCycleEnd(ctx, output)
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
	if lc.Hooks != nil && lc.Hooks.OnError != nil {
		lc.Hooks.OnError(ctx, err)
	}
}

// checkSandbox checks Sandbox if non-nil; returns reason and whether denied.
func checkSandbox(ctx context.Context, lc *LoopContext, tc ToolCall) (reason string, denied bool) {
	if lc.Sandbox == nil {
		return "", false
	}
	act := Action{CellID: lc.CellID, Call: tc}
	allowed, reason, err := lc.Sandbox.Allow(ctx, act)
	if err != nil {
		return fmt.Sprintf("sandbox error: %v", err), true
	}
	if !allowed {
		return reason, true
	}
	return "", false
}
