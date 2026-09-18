// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// invocation.go — the one data package a single decision-loop invocation runs on.
package nerve

import "time"

// LoopContext carries all data needed for a single Cycle invocation.
type LoopContext struct {
	// Identity
	CellID   string
	Identity string

	// Built-in capability description (host composed, describes only)
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
	MaxRounds       int           // Hard round limit (<=0 uses DefaultMaxRounds)
	MaxToolOutput   int           // Tool output truncation length (<=0 = no truncation)
	MaxRetries      int           // Think retry count (<=0 = no retry)
	ToolTimeout     time.Duration // Per-tool execution timeout (<=0 = no timeout)
	ToolMaxRetries  int           // Tool retry count on effector error (<=0 = no retry)
	ParallelActs    bool          // Parallel batch execution (requires a concurrency-safe Effector)
	MaxParallelActs int           // Ceiling on concurrently executing batch calls (<=0 = none)

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

	// Reflection slot (host injected via the BeforeStimulate write-back):
	// the Reflexion note passed through to every Think.
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
