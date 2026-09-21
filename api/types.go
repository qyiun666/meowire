// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// types.go — public contract types: aliases of the internal definitions.
//
// The api package is the primary importable surface of meowire. All
// implementation packages live under internal/ and are sealed by the Go
// compiler; hosts interact with the framework through New/Stimulate/Close
// plus the contract types below (ports, data packets, events, memory).
package meowire

import (
	"context"

	"github.com/qyiun666/meowire/internal/brain"
	"github.com/qyiun666/meowire/internal/nerve"
)

// Ports — host-provided capabilities (all required, no stubs). The brain is
// not among them: it is the one organ the framework ships, parameterized by
// Organs.Brain instead of implemented by the host. The pause gate is not here
// either: it is framework wiring behind Agent.Pause, not something a host
// supplies.
type (
	Effector      = nerve.Effector
	Closer        = nerve.Closer
	Hooks         = nerve.Hooks
	Sandbox       = nerve.Sandbox
	ContextBudget = nerve.ContextBudget
	Memory        = nerve.Memory
)

// The bundled brain's assembly parameters and streaming channel.
type (
	// BrainConfig parameterizes the bundled brain (the public face of
	// brain.Config): endpoint, credential, model, the streaming switch and
	// the wire selector.
	BrainConfig = brain.Config
	// BrainMode selects the wire the brain speaks (the public face of
	// brain.Mode): chat completions or the Responses API. A transport
	// choice, not a contract fork — both render the same Prompt statelessly
	// and fold back into the same Decision.
	BrainMode = brain.Mode
	// Sink receives the brain's text deltas during a streaming round.
	Sink = brain.Sink
)

// BrainMode constants. The zero value lands on chat: an assembly that never
// sets a Mode still gets the default wire.
const (
	BrainModeChat      BrainMode = brain.ModeChat      // chat completions (default)
	BrainModeResponses BrainMode = brain.ModeResponses // the Responses API wire
)

// WithSink mounts the streaming channel on the context handed to
// Stimulate/Resume: the brain's text deltas reach the sink before the output
// membrane rules, so a host showing them live replaces the whole segment when
// EventText (whole, already ruled) arrives.
func WithSink(ctx context.Context, sink Sink) context.Context {
	return brain.WithSink(ctx, sink)
}

// Data packets exchanged with the host ports.
type (
	MethodSpec = nerve.MethodSpec
	Prompt     = nerve.Prompt
	ToolSpec   = nerve.ToolSpec
	ToolResult = nerve.ToolResult
	Decision   = nerve.Decision
	ToolCall   = nerve.ToolCall
	Action     = nerve.Action
	Effect     = nerve.Effect
	Usage      = nerve.Usage
	Verdict    = nerve.Verdict  // tri-state sandbox ruling (Deny / Allow / Ask)
	Response   = nerve.Response // what a suspended loop is answered with (see Session.Kind)

	// Utterance is what the output half of the membrane (Sandbox.Emit) is
	// asked to rule on: one round's generated text.
	Utterance = nerve.Utterance

	// Memory port payload (P7): what recall returns and what a finished
	// invocation hands back for persistence.
	Record      = nerve.Record
	MemoryQuery = nerve.MemoryQuery
	CycleFacts  = nerve.CycleFacts
)

// Events yielded by the Stimulate iterator.
type (
	Event          = nerve.Event
	EventKind      = nerve.EventKind
	LoopState      = nerve.LoopState
	SandboxVerdict = nerve.SandboxVerdict
	WaitInput      = nerve.WaitInput // EventWaitInput/EventPaused payload: tool (zero for pause) + question + resume Session
	Session        = nerve.Session   // opaque resume handle — save from EventWaitInput/EventPaused, pass to Resume
	WaitKind       = nerve.WaitKind  // what a Session suspended for, which is what a Response answers
	ReplaceAudit   = nerve.ReplaceAudit
	ConfigAudit    = nerve.ConfigAudit
)

// UnmarshalSession restores a Session from Marshal output — the persistence
// round-trip (save the bytes, restore the handle, pass it to Resume). A
// version mismatch returns an error: the wire format has evolved and the
// saved handle must not be replayed against a different contract.
func UnmarshalSession(data []byte) (Session, error) {
	return nerve.UnmarshalSession(data)
}

// Event wire format — the portable shape of the event stream, for a host that
// journals events or replays them in another process. Kinds, states and
// rulings travel by name and every record names its version, so a stale stream
// is rejected rather than reinterpreted. Version 2 records carry the cell's
// emission order (Seq) and moment (TS).
type (
	WireEvent   = nerve.WireEvent
	WireErr     = nerve.WireErr
	WireVerdict = nerve.WireVerdict
	WireWait    = nerve.WireWait
)

// EncodeEvent serializes one event; DecodeEvent restores it. A value the wire
// cannot carry (an error the framework does not own) comes back as an equal
// text and the event names the loss in its Dropped field — nothing is dropped
// without saying so. The encode side refuses what the decode side could never
// read back: an event kind, loop state or membrane ruling outside its name table.
func EncodeEvent(e Event) ([]byte, error) { return nerve.EncodeEvent(e) }

// DecodeEvent rejects malformed JSON, a foreign wire version, an unknown kind,
// state or ruling name, and a suspension handle whose own version guard fails.
func DecodeEvent(data []byte) (Event, error) { return nerve.DecodeEvent(data) }

// Wiring blueprint types (Connectome / WiringDiagram / Validate).
type (
	WirePoint     = nerve.WirePoint
	WireNode      = nerve.WireNode
	WireCategory  = nerve.WireCategory
	WireSemantics = nerve.WireSemantics
)

// LoopState constants.
const (
	StateIdle     LoopState = nerve.StateIdle
	StateThinking LoopState = nerve.StateThinking
	StateActing   LoopState = nerve.StateActing
	StatePaused   LoopState = nerve.StatePaused
	StateWaiting  LoopState = nerve.StateWaiting
	StateDone     LoopState = nerve.StateDone
	StateError    LoopState = nerve.StateError
)

// EventKind constants.
const (
	EventText       EventKind = nerve.EventText
	EventToolCall   EventKind = nerve.EventToolCall
	EventToolResult EventKind = nerve.EventToolResult
	EventState      EventKind = nerve.EventState
	EventDone       EventKind = nerve.EventDone
	EventError      EventKind = nerve.EventError
	EventUsage      EventKind = nerve.EventUsage
	EventSandbox    EventKind = nerve.EventSandbox
	EventWaitInput  EventKind = nerve.EventWaitInput
	EventPaused     EventKind = nerve.EventPaused
	EventReplace    EventKind = nerve.EventReplace
	EventConfig     EventKind = nerve.EventConfig
)

// Suspension flavours (Session.Kind): what the handle stopped for, which is what
// decides how Resume reads the Response.
const (
	WaitPause     WaitKind = nerve.WaitPause     // gap pause: nothing is being asked
	WaitTool      WaitKind = nerve.WaitTool      // a tool asked the outside world a question
	WaitCallAsk   WaitKind = nerve.WaitCallAsk   // the membrane asked before executing a call
	WaitUtterance WaitKind = nerve.WaitUtterance // the membrane asked before saying a draft
)

// Sandbox ruling constants (tri-state; the zero value is Deny — fail-closed).
const (
	VerdictDeny  Verdict = nerve.VerdictDeny  // execution refused
	VerdictAllow Verdict = nerve.VerdictAllow // permitted: proceed to execution
	VerdictAsk   Verdict = nerve.VerdictAsk   // suspend and confirm externally
)

// CycleOutcome classifies how the Stimulate/Resume cycle ended, delivered by
// Hooks.OnCycleEnd. Zero is deliberately not a valid outcome.
type CycleOutcome = nerve.CycleOutcome

// CycleOutcome constants (zero value reserved — unmarked means the consumer
// abandoned the iterator early).
const (
	OutcomeDone      CycleOutcome = nerve.OutcomeDone      // cycle completed normally
	OutcomeSuspended CycleOutcome = nerve.OutcomeSuspended // yielded Session via EventWaitInput/EventPaused
	OutcomeMaxRounds CycleOutcome = nerve.OutcomeMaxRounds // round budget exhausted with pending tool calls
	OutcomeError     CycleOutcome = nerve.OutcomeError     // ended through EventError
	OutcomeAborted   CycleOutcome = nerve.OutcomeAborted   // consumer stopped consuming mid-cycle
)

// WireCategory constants.
const (
	CategorySense  WireCategory = nerve.CategorySense
	CategoryDecide WireCategory = nerve.CategoryDecide
	CategoryAct    WireCategory = nerve.CategoryAct
)

// WireSemantics constants.
const (
	SemReplace   WireSemantics = nerve.SemReplace
	SemAppend    WireSemantics = nerve.SemAppend
	SemTrim      WireSemantics = nerve.SemTrim
	SemGate      WireSemantics = nerve.SemGate
	SemRead      WireSemantics = nerve.SemRead
	SemAct       WireSemantics = nerve.SemAct
	SemContainer WireSemantics = nerve.SemContainer
)

// DefaultMaxRounds is the default round limit when Config.MaxRounds <= 0.
const DefaultMaxRounds = nerve.DefaultMaxRounds
