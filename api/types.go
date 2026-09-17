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
	"github.com/qyiun666/meowire/internal/nerve"
)

// Ports — host-provided capabilities (all required, no stubs).
type (
	Thinker       = nerve.Thinker
	Effector      = nerve.Effector
	Closer        = nerve.Closer
	Hooks         = nerve.Hooks
	Sandbox       = nerve.Sandbox
	ContextBudget = nerve.ContextBudget
	Memory        = nerve.Memory
	PauseGate     = nerve.PauseGate
)

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
	Verdict    = nerve.Verdict // tri-state sandbox ruling (Deny / Allow / Ask)

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
// is rejected rather than reinterpreted.
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
