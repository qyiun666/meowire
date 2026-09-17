// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// event.go — typed event produced by each iteration of the DecisionLoop.
package nerve

// Event is a typed event produced by each iteration of the DecisionLoop.
// The host consumes events via a yield function.
//
// Each EventKind uses specific fields; unused fields are zero values.
// This is intentional — the flat struct is passed by value on the stack
// via yield, avoiding heap allocation that an interface-based design would incur.
type Event struct {
	// CellID names the cell that produced this event. The cell stamps it on
	// every event as it leaves, so an event keeps its author once it is
	// written to a log, replayed in another process, or merged with another
	// agent's events.
	CellID   string
	Kind     EventKind
	Text     string          // KindText: LLM text output
	ToolCall *ToolCall       // KindToolCall: the call; KindToolResult: echoes the call for ID association
	Effect   *Effect         // KindToolResult: tool execution result
	State    LoopState       // KindState: loop state change
	Err      error           // KindError: unrecoverable error
	Output   string          // KindDone: final accumulated output
	Usage    *Usage          // KindUsage: token usage of the last Think
	Verdict  *SandboxVerdict // EventSandbox: sandbox decision (audit record)
	Wait     *WaitInput      // EventWaitInput / EventPaused: suspension snapshot + resume handle
	Replace  *ReplaceAudit   // EventReplace: runtime port swap record (audit)
	Config   *ConfigAudit    // EventConfig: runtime config swap record (audit)
	// Dropped names the values a wire round trip could not carry (see
	// DecodeEvent). The loop never writes it: a live event is complete by
	// construction, so a non-empty Dropped means "this event was restored".
	Dropped []string
}

// SandboxVerdict is the audit record of one membrane ruling. The guard covers
// both sides of the loop and each ruling is audited exactly once: a tool
// attempt records its Call before execution, a round's text records a zero
// Call. Either side's Ask resolution adds a second terminal record (ask →
// allow/deny), closing the chain. Hosts persist these to build the
// who/what/why-was-permitted trail required by the Authority model.
type SandboxVerdict struct {
	CellID   string   // owning agent id
	Call     ToolCall // the gated tool action; zero for an utterance-side ruling
	Ruling   Verdict  // tri-state decision: Deny / Allow / Ask
	Reason   string   // policy reason ("" when allowed; the denial text when resolved-as-denied)
	Question string   // confirmation prompt carried by an Ask ruling ("" otherwise)
	Err      error    // sandbox evaluation error (nil = clean decision)
}

// ReplaceAudit is the audit record of one runtime port swap: every
// successful Replace produces exactly one record, emitted as EventReplace at
// the start of the next Stimulate/Resume (the moment the swap takes effect).
// Hosts persist these to build the wiring audit trail alongside EventSandbox.
// The swapped ports are recorded by type name, not by value: an audit record
// outlives the swap and is written to a log, so a live port inside it would
// keep a retired organ alive and could not be serialized. `Replace` returns
// the previous port to the host, which is where a value can still be acted on.
type ReplaceAudit struct {
	CellID  string // owning agent id
	Slot    string // slot name this swap went through (one of the blueprint's WirePoint.Slot values)
	OldType string // type name of the displaced port ("" when none was set)
	NewType string // type name of the swapped-in port
}

// ConfigAudit is the audit record of one runtime config swap: every
// successful UpdateConfig produces exactly one record, emitted as
// EventConfig at the start of the next Stimulate/Resume (the moment the
// swap takes effect). Hosts persist these to build the wiring audit trail
// alongside EventSandbox and EventReplace — every "unique update" of the
// loop is traceable.
type ConfigAudit struct {
	CellID string     // owning agent id
	Old    LoopConfig // previous config (zero value if none was set)
	New    LoopConfig // the swapped-in config
}

// EventKind categorizes the type of event.
type EventKind int

const (
	EventText       EventKind = iota // LLM text output
	EventToolCall                    // LLM decided to call a tool
	EventToolResult                  // Tool execution result
	EventState                       // Loop state change
	EventDone                        // Loop completed normally
	EventError                       // Loop encountered unrecoverable error
	EventUsage                       // Token usage of the last Think
	EventSandbox                     // Sandbox decision (audit record)
	EventWaitInput                   // Loop suspended waiting for external input
	EventPaused                      // Loop suspended by a pause request (Session + resume handle)
	EventReplace                     // Runtime port swap (audit record)
	EventConfig                      // Runtime config swap (audit record)
)

// eventKindNames is the wire name of each kind, indexed by value. One table
// rather than a switch per direction: a kind added to the iota without a name
// here is reported as "unknown" on the way out and rejected on the way in, and
// a guard test pins the table's length against the last constant.
var eventKindNames = []string{
	"text", "tool-call", "tool-result", "state", "done", "error",
	"usage", "sandbox", "wait-input", "paused", "replace", "config",
}

// String returns the kind's wire name. Kinds travel by name, not by number, so
// inserting or reordering the iota cannot silently reinterpret a stored stream
// (same discipline as the Session's waitKind names).
func (k EventKind) String() string {
	if name := nameOf(eventKindNames, k); name != "" {
		return name
	}
	return "unknown"
}

// eventKindOf resolves a wire name; an unknown name is reported as not-a-kind
// so the caller rejects the record instead of guessing what it was.
func eventKindOf(name string) (EventKind, bool) {
	return valueOfName[EventKind](eventKindNames, name)
}
