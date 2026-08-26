// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// port.go — host ports: LLM/tool contracts.
package nerve

import (
	"context"
	"encoding/json"
	"fmt"
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
// EventWaitInput and EventPaused and consumed by Resume; hosts only save it
// and pass it back — its fields are unexported and must not be inspected or
// mutated (persistence round-trips through Marshal/UnmarshalSession).
// A Session is single-use: resuming it twice re-executes the remaining tool
// calls with duplicate side effects (host responsibility).
type Session struct {
	round       int          // round at suspension; Resume continues from it (no extra round)
	input       string       // stimulus text at suspension
	plan        string       // plan at suspension
	context     []string     // accumulated context at suspension (host base + sandbox denials)
	output      string       // accumulated text output at suspension (EventDone prefix)
	pending     ToolCall     // the tool that requested input (zero value for pause suspensions)
	remaining   []ToolCall   // tool calls after the suspending one
	toolResults []ToolResult // accumulated structured tool feedback at suspension
}

// sessionVersion is the Session serialization format version. Bump it on
// any incompatible change to the marshaled shape; UnmarshalSession rejects
// mismatched versions so a stale or future handle is never replayed.
const sessionVersion = 1

// sessionJSON is the wire shape of a Session. Session fields stay
// unexported (hosts only save the handle and pass it back — no inspection,
// no mutation), so persistence round-trips through Marshal/UnmarshalSession.
type sessionJSON struct {
	Version     int          `json:"version"`
	Round       int          `json:"round"`
	Input       string       `json:"input"`
	Plan        string       `json:"plan"`
	Context     []string     `json:"context"`
	Output      string       `json:"output"`
	Pending     ToolCall     `json:"pending"`
	Remaining   []ToolCall   `json:"remaining"`
	ToolResults []ToolResult `json:"toolResults"`
}

// Marshal serializes the session to its wire shape (JSON) — the persistence
// primitive for both suspension kinds (ask_user and pause, v1.3.2): hosts
// save the bytes, restore via UnmarshalSession, and pass the restored
// Session to Resume. A zero-value Session (round == 0) is not a valid
// handle and returns an error.
func (s Session) Marshal() ([]byte, error) {
	if !s.valid() {
		return nil, fmt.Errorf("nerve.Session.Marshal: invalid session (zero value)")
	}
	b, err := json.Marshal(sessionJSON{
		Version:     sessionVersion,
		Round:       s.round,
		Input:       s.input,
		Plan:        s.plan,
		Context:     s.context,
		Output:      s.output,
		Pending:     s.pending,
		Remaining:   s.remaining,
		ToolResults: s.toolResults,
	})
	if err != nil {
		return nil, fmt.Errorf("nerve.Session.Marshal: %w", err)
	}
	return b, nil
}

// UnmarshalSession restores a Session from Marshal output. A version
// mismatch returns an error: the wire format has evolved and the saved
// handle must not be replayed against a different contract.
func UnmarshalSession(data []byte) (Session, error) {
	var sj sessionJSON
	if err := json.Unmarshal(data, &sj); err != nil {
		return Session{}, fmt.Errorf("nerve.UnmarshalSession: %w", err)
	}
	if sj.Version != sessionVersion {
		return Session{}, fmt.Errorf("nerve.UnmarshalSession: version %d != %d (wire format changed)", sj.Version, sessionVersion)
	}
	s := Session{
		round:       sj.Round,
		input:       sj.Input,
		plan:        sj.Plan,
		context:     sj.Context,
		output:      sj.Output,
		pending:     sj.Pending,
		remaining:   sj.Remaining,
		toolResults: sj.ToolResults,
	}
	if !s.valid() {
		return Session{}, fmt.Errorf("nerve.UnmarshalSession: invalid session payload (round < 1)")
	}
	return s, nil
}

// valid reports whether s is a usable session (zero value is rejected).
// round is the discriminator: the framework always snapshots round >= 1,
// and a zero-value Session has round 0. context may be nil (a host that
// injects no base context) — Resume appends onto nil slices fine.
func (s Session) valid() bool { return s.round >= 1 }

// RemainingCalls returns a clone of the tool calls still pending at the
// suspension point (empty when nothing is left to run). Hosts use it in the
// suspension-resume protocol to tell whether Resume will replay tool calls:
// a pause before or amid a round keeps the unexecuted calls, while a tool
// suspension inside a parallel batch is empty (the batch fully executed —
// replaying it would duplicate side effects). The returned clone is safe to
// inspect; the Session itself stays opaque.
func (s Session) RemainingCalls() []ToolCall {
	return slices.Clone(s.remaining)
}

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
