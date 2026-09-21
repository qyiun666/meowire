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

// MethodSpec describes one built-in capability the agent has. The framework
// never produces or interprets the list — the host composes it and the Thinker
// renders it, so it describes only.
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
	Methods  []MethodSpec // Built-in capability description (host composed, describes only)
	Tools    []ToolSpec   // Available tool list (host defined)
	Context  []string     // Context (host-injected base + sandbox denials; tool results live in ToolResults)
	Bounds   string       // Execution boundary description (Sandbox.Bounds snapshot, host defined)

	// Dynamic part (updated each round)
	Input string // Current stimulus text
	Plan  string // Task plan/progress (host injected, brain can update)
	// Reflection carries the host's reflexion note (e.g. a failure
	// post-mortem from the previous attempt). Injected via the
	// BeforeStimulate prototype, passed through verbatim every round — the
	// Reflexion loop's standard slot, so hosts never invent private prompt
	// channels.
	Reflection string

	// Structured tool feedback accumulated within this cycle (see ToolResult).
	ToolResults []ToolResult

	// Memories is this round's recall output (see Memory): framework-filled
	// before every Think and replaced wholesale each round, so it never
	// accumulates and never enters a suspension snapshot. BeforeThink may
	// overwrite it, as it may overwrite Context.
	Memories []Record
}

// buildPrompt assembles this round's data package from the loop context. The
// Thinker reads it as a snapshot; only the framework writes it.
func (lc *LoopContext) buildPrompt() *Prompt {
	return &Prompt{
		System:      lc.System,
		Identity:    lc.Identity,
		Methods:     lc.Methods,
		Tools:       lc.Tools,
		Context:     lc.Context,
		Bounds:      lc.Bounds,
		Input:       lc.Input,
		Plan:        lc.Plan,
		Reflection:  lc.Reflection,
		ToolResults: lc.ToolResults,
		Memories:    lc.Memories,
	}
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
// (tool-role messages, [tool_call_id=xxx] markers, plain text) is the
// bundled brain's, not the kernel's. Sandbox denials are verdicts, not tool
// results, and never appear here.
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
// the host collects the input and hands it back as Resume's Response. A
// WaitInput declared while err is non-nil wins over the error (it is an
// explicit intent); a nil Effect is never treated as a suspension.
type Effect struct {
	Result string
	Err    string
	// WaitInput non-empty = suspend and wait for external input.
	WaitInput string
}

// WaitInput is the EventWaitInput payload: why the loop stopped for input and
// the resume handle. The host saves Session and passes it back to Resume once
// the answer arrives. Call and Question describe what is being asked: a tool
// wait names the tool and its question, a pre-execution membrane ask names the
// pending call and the confirmation prompt, an utterance membrane ask carries no
// call at all, and a pause carries neither. The two look-alike cases (a tool's
// own question and a pre-execution confirmation) are told apart by
// Session.Kind, which is also what says which Response field the answer needs.
type WaitInput struct {
	CellID   string
	Call     ToolCall // the tool that requested input (zero for an utterance ask or a pause)
	Question string   // what the loop is waiting on an answer to
	Session  Session  // resume handle — host saves and returns it
}

// Session is an opaque value object snapshotting the loop state at the
// suspension point (round, accumulated context, remaining tool calls,
// accumulated output, plan, input). It is produced by the framework inside
// EventWaitInput and EventPaused and consumed by Resume; hosts only save it
// and pass it back — its fields are unexported, read only through the
// accessors below, and never mutated (persistence round-trips through
// Marshal/UnmarshalSession).
// A Session belongs to the cell that suspended it: Resume rejects a handle
// that another cell produced, because replaying it would run the first cell's
// round on the second cell's organs. It is single-use too — resuming twice
// re-executes the remaining tool calls with duplicate side effects (host
// responsibility).
type Session struct {
	round       int          // round at suspension; Resume continues from it (no extra round)
	input       string       // stimulus text at suspension
	plan        string       // plan at suspension
	context     []string     // accumulated context at suspension (host base + sandbox denials)
	output      string       // accumulated text output at suspension (EventDone prefix)
	pending     ToolCall     // the tool that requested input (zero value for pause suspensions)
	remaining   []ToolCall   // tool calls after the suspending one
	toolResults []ToolResult // accumulated structured tool feedback at suspension
	cell        string       // owning cell: Resume refuses a handle from another cell
	kind        WaitKind     // why the loop stopped for input
	utterance   string       // withheld text, kind == WaitUtterance
}

// WaitKind classifies one suspension — what the resumed Response answers.
type WaitKind int

const (
	WaitPause     WaitKind = iota // gap pause: nothing is being asked, the loop just waits
	WaitTool                      // a tool requested external input (Response.Answer is its result)
	WaitCallAsk                   // the membrane asked before executing pending (approve or deny)
	WaitUtterance                 // the membrane asked before saying utterance (approve or deny)
)

// waitKindNames is the wire name of each suspension flavour, indexed by value:
// encoding kinds by name rather than by number means a reordering of the iota
// cannot silently reinterpret a saved handle.
var waitKindNames = []string{"pause", "tool", "call-ask", "utterance-ask"}

// Response is the answer a suspension asked for, or the reason it is refused.
// Which field is read depends on what the handle suspended for (Session.Kind),
// and the two are mutually exclusive: stating a refusal refuses the suspension
// however the answer is worded.
//
//   - WaitTool: Answer becomes the pending tool's structured result; a non-empty
//     Deny records the call as failed with that reason instead.
//   - WaitCallAsk / WaitUtterance: Deny refuses — the pending call is not
//     executed, the withheld draft is replaced by the denial — and an empty Deny
//     with a non-empty Answer grants it, the Answer kept as the closing audit
//     record's reason.
//   - WaitPause: nothing is being asked, so neither field is read.
//
// The zero value is fail-closed wherever a refusal is possible: it declines both
// membrane asks. A tool wait resumed with the zero value gets an empty answer,
// which is what the host asked for by resuming it.
type Response struct {
	Answer string // what the question was answered with
	Deny   string // non-empty = refuse it, for this reason
}

// wireName is the enum's JSON identity. "" means the value is not a flavour this
// build knows, which Marshal refuses rather than writing out as a pause.
func (k WaitKind) wireName() string { return nameOf(waitKindNames, k) }

// waitKindOf decodes a wire name; an unknown name is reported as not-a-kind
// (the caller rejects the handle rather than guessing a flavour).
func waitKindOf(name string) (WaitKind, bool) {
	return valueOfName[WaitKind](waitKindNames, name)
}

// sessionVersion is the Session serialization format version. Bump it on
// any incompatible change to the marshaled shape; UnmarshalSession rejects
// mismatched versions so a stale or future handle is never replayed.
const sessionVersion = 4

// sessionJSON is the wire shape of a Session. Session fields stay
// unexported (hosts only save the handle and pass it back — no inspection,
// no mutation), so persistence round-trips through Marshal/UnmarshalSession.
type sessionJSON struct {
	Version     int          `json:"version"`
	Cell        string       `json:"cell"` // owning cell, checked by Resume
	Round       int          `json:"round"`
	Input       string       `json:"input"`
	Plan        string       `json:"plan"`
	Context     []string     `json:"context"`
	Output      string       `json:"output"`
	Pending     ToolCall     `json:"pending"`
	Remaining   []ToolCall   `json:"remaining"`
	ToolResults []ToolResult `json:"toolResults"`
	Ask         string       `json:"ask"`       // WaitKind wire name
	Utterance   string       `json:"utterance"` // withheld text (ask == "utterance-ask")
}

// Marshal serializes the session to its wire shape (JSON) — the persistence
// primitive behind every suspension flavour (tool wait, pause, and both
// membrane asks): hosts save the bytes, restore via UnmarshalSession, and pass
// the restored Session to Resume. A zero-value Session (round == 0) is not a
// valid handle and returns an error.
func (s Session) Marshal() ([]byte, error) {
	if !s.valid() {
		return nil, fmt.Errorf("nerve.Session.Marshal: invalid session (zero value)")
	}
	ask := s.kind.wireName()
	if ask == "" {
		return nil, fmt.Errorf("nerve.Session.Marshal: unknown suspension kind %d", s.kind)
	}
	b, err := json.Marshal(sessionJSON{
		Version:     sessionVersion,
		Cell:        s.cell,
		Round:       s.round,
		Input:       s.input,
		Plan:        s.plan,
		Context:     s.context,
		Output:      s.output,
		Pending:     s.pending,
		Remaining:   s.remaining,
		ToolResults: s.toolResults,
		Ask:         ask,
		Utterance:   s.utterance,
	})
	if err != nil {
		return nil, fmt.Errorf("nerve.Session.Marshal: %w", err)
	}
	return b, nil
}

// UnmarshalSession restores a Session from Marshal output. A version
// mismatch returns an error: the wire format has evolved and the saved
// handle must not be replayed against a different contract. An unknown ask
// name is rejected too — resuming it as some other flavour would replay the
// wrong side effects.
func UnmarshalSession(data []byte) (Session, error) {
	var sj sessionJSON
	if err := json.Unmarshal(data, &sj); err != nil {
		return Session{}, fmt.Errorf("nerve.UnmarshalSession: %w", err)
	}
	if sj.Version != sessionVersion {
		return Session{}, fmt.Errorf("nerve.UnmarshalSession: version %d != %d (wire format changed)", sj.Version, sessionVersion)
	}
	kind, ok := waitKindOf(sj.Ask)
	if !ok {
		return Session{}, fmt.Errorf("nerve.UnmarshalSession: unknown ask %q", sj.Ask)
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
		cell:        sj.Cell,
		kind:        kind,
		utterance:   sj.Utterance,
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

// Kind reports what this handle suspended for, which is what tells a host the
// shape of the Response Resume will read: an ask wants a ruling (its Question is
// a yes/no), a tool wait wants content, a pause wants neither. The handle is the
// authority on this — the same fact a host could infer from WaitInput.Call is
// ambiguous between a tool's own question and a pre-execution confirmation, and
// a handle restored from a journal has no wait event beside it at all.
func (s Session) Kind() WaitKind { return s.kind }

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
