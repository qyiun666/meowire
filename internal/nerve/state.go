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

// loopStateNames is the wire name of each state, indexed by value: a state
// travels by name, so reordering the iota cannot silently reinterpret a stored
// stream. A guard test pins the table against the last constant.
var loopStateNames = []string{
	"idle", "thinking", "acting", "paused", "waiting", "done", "error",
}

// String returns the human-readable name of the loop state.
func (s LoopState) String() string {
	if name := nameOf(loopStateNames, s); name != "" {
		return name
	}
	return "unknown"
}

// loopStateOf resolves a state name; an unknown name is reported as not-a-state
// so a stored stream is rejected instead of read as some other state.
func loopStateOf(name string) (LoopState, bool) {
	return valueOfName[LoopState](loopStateNames, name)
}

// DefaultMaxRounds is the default round limit when MaxRounds <= 0.
const DefaultMaxRounds = 8

// LoopConfig is the scalar runtime configuration of the loop (host
// provided). Zero-value semantics: MaxRounds<=0 uses DefaultMaxRounds(8);
// MaxToolOutput<=0 disables truncation; MaxRetries<=0 disables Think retry;
// ToolTimeout<=0 disables per-tool timeouts; ToolMaxRetries<=0 disables tool
// retry; ParallelActs=false keeps strict serial tool execution, and
// MaxParallelActs<=0 places no ceiling on a parallel batch
// (every admitted call runs at once). UpdateConfig swaps it wholesale; the next
// Stimulate/Resume snapshots the new values (an in-flight loop keeps the values
// it started with).
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
	// MaxParallelActs caps how many calls of one batch execute at the same
	// time (<=0 = the whole batch at once). It only narrows ParallelActs:
	// a host that may run tools concurrently but not all at once (a rate
	// limit, a connection pool) sets the ceiling it owes someone else.
	MaxParallelActs int
}
