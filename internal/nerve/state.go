// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// state.go — the loop's observable vocabulary: states, the round cap and its config.
package nerve

import (
	"time"
)

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
