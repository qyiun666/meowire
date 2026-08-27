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
}

// SandboxVerdict is the audit record of one sandbox decision: every tool
// execution attempt produces exactly one verdict before the tool runs,
// and an Ask resolution adds a second terminal record (ask → allow/deny),
// closing the chain. Hosts persist these to build the action-level audit
// trail (who/what/why was permitted) required by the Authority model.
type SandboxVerdict struct {
	CellID   string   // owning agent id
	Call     ToolCall // the tool action being gated
	Ruling   Verdict  // tri-state decision: Deny / Allow / Ask
	Reason   string   // policy reason ("" when allowed; the denial text when resolved-as-denied)
	Question string   // confirmation prompt carried by an Ask ruling ("" otherwise)
	Err      error    // sandbox evaluation error (nil = clean decision)
}

// ReplaceAudit is the audit record of one runtime port swap: every
// successful Replace produces exactly one record, emitted as EventReplace at
// the start of the next Stimulate/Resume (the moment the swap takes effect).
// Hosts persist these to build the wiring audit trail alongside EventSandbox.
type ReplaceAudit struct {
	CellID string // owning agent id
	Slot   string // swappable slot name ("think"/"act"/"sandbox"/"budget"/"hooks")
	Old    any    // previous port value (nil if none was set)
	New    any    // the swapped-in port value
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
