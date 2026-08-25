// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// port.go — host ports: LLM/tool contracts.
package nerve

import (
	"context"
	"slices"
)

// MethodSpec is a method specification (gene Method capability projection, describes only).
type MethodSpec struct {
	Name   string
	Desc   string
	Input  string
	Output string
}

// Prompt is the sole data package delivered to the brain (Thinker).
// The framework assembles it; the Thinker (LLM) makes decisions.
type Prompt struct {
	// Fixed part (set at construction, unchanged per cycle)
	System   string       // System instructions (host injected)
	Identity string       // Identity description text (host composed)
	Methods  []MethodSpec // Built-in capability description (gene projection, describes only)
	Tools    []ToolSpec   // Available tool list (host defined)
	Context  []string     // Context (host-injected base + sandbox denials; tool results live in ToolResults)
	Bounds   string       // Execution boundary description (Sandbox.Bounds snapshot, host defined)

	// Dynamic part (updated each round)
	Input string // Current stimulus text
	State string // Current loop state (framework auto-updated)
	Plan  string // Task plan/progress (host injected, brain can update)

	// Structured tool feedback accumulated within this cycle (single track:
	// tool results no longer enter Context; rendering is the host's call).
	ToolResults []ToolResult
}

// ToolSpec is a tool specification (host defined, framework passthrough).
type ToolSpec struct {
	Name   string // Tool name
	Desc   string // Description
	Input  string // Input parameter description (JSON Schema)
	Output string // Output description
}

// ToolResult is a structured tool feedback entry (framework populated
// within a cycle; the sole feedback track — tool results never enter the
// Context text track, which keeps host base + sandbox denials).
// ID/Name echo the originating ToolCall (ID is the LLM-provided call id,
// e.g. call_xxx). Result carries the successful output and Err the failure
// text — Err non-empty means the call failed, and exactly one of the two is
// set; both are post-truncation when MaxToolOutput applies. Rendering
// (tool-role messages, [tool_call_id=xxx] markers, plain text) is the host
// Thinker's decision. Sandbox denials are verdicts, not tool results, and
// never appear here.
type ToolResult struct {
	ID     string // Tool call ID (LLM-provided, e.g. call_xxx)
	Name   string // Tool name (echo of ToolCall.Name)
	Result string // Successful tool output (truncated per MaxToolOutput)
	Err    string // Failure text (truncated per MaxToolOutput; non-empty = failed)
}

// Usage is the token usage (carried by Decision, host accumulates; nil = skip accounting).
type Usage struct {
	Prompt     int
	Completion int
	Total      int
}

// Decision is the Thinker output.
type Decision struct {
	Text      string
	ToolCalls []ToolCall
	Usage     *Usage
}

// ToolCall is a tool invocation declaration.
type ToolCall struct {
	ID   string
	Name string
	Args string
}

// Action is an execution action (wraps a tool call).
type Action struct {
	CellID string
	Call   ToolCall
}

// Effect is an execution result.
// WaitInput, when non-empty, suspends the loop: the tool requests external
// input (the field carries the question text). The loop yields
// EventWaitInput with a Session snapshot and ends the iterator normally;
// the host collects the input and resumes via Resume(sess, response). A
// WaitInput declared while err is non-nil wins over the error (it is an
// explicit intent); a nil Effect is never treated as a suspension.
type Effect struct {
	Result string
	Err    string
	// WaitInput non-empty = suspend and wait for external input.
	WaitInput string
}

// WaitInput is the EventWaitInput payload: which tool suspended the loop,
// what it asked, and the resume handle. The host saves Session and passes
// it back to Resume once the external input arrives.
type WaitInput struct {
	CellID   string
	Call     ToolCall // the tool that requested input
	Question string   // the question text (Effect.WaitInput)
	Session  Session  // resume handle — host saves and returns it
}

// Session is an opaque value object snapshotting the loop state at the
// suspension point (round, accumulated context, remaining tool calls,
// accumulated output, plan, input). It is produced by the framework inside
// EventWaitInput and consumed by Resume; hosts only save it and pass it
// back — its fields are unexported and must not be inspected or mutated.
// A Session is single-use: resuming it twice re-executes the remaining tool
// calls with duplicate side effects (host responsibility).
type Session struct {
	round       int          // round at suspension; Resume continues from it (no extra round)
	input       string       // stimulus text at suspension
	plan        string       // plan at suspension
	context     []string     // accumulated context at suspension (host base + sandbox denials)
	output      string       // accumulated text output at suspension (EventDone prefix)
	pending     ToolCall     // the tool that requested input (resume response attaches to its result)
	remaining   []ToolCall   // tool calls after the suspending one
	toolResults []ToolResult // accumulated structured tool feedback at suspension
}

// valid reports whether s is a usable session (zero value is rejected).
// round is the discriminator: the framework always snapshots round >= 1,
// and a zero-value Session has round 0. context may be nil (a host that
// injects no base context) — Resume appends onto nil slices fine.
func (s Session) valid() bool { return s.round >= 1 }

// snapshot returns a deep-enough copy of the session for later resumption.
func (s Session) snapshot(round int, input, plan string, context []string, output string, pending ToolCall, remaining []ToolCall, toolResults []ToolResult) Session {
	return Session{
		round:       round,
		input:       input,
		plan:        plan,
		context:     slices.Clone(context),
		output:      output,
		pending:     pending,
		remaining:   slices.Clone(remaining),
		toolResults: slices.Clone(toolResults),
	}
}

// Thinker is the LLM host port (the brain).
type Thinker interface {
	Think(ctx context.Context, p *Prompt) (*Decision, error)
}

// Effector is the tool host port (execution).
type Effector interface {
	Act(ctx context.Context, a Action) (*Effect, error)
}

// Closer is the cleanup host port (shutdown).
type Closer interface {
	Close() error
}
