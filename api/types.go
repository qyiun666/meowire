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
	"github.com/qyiun666/meowire/internal/memory"
	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/synapse"
)

// Ports — host-provided capabilities (all required, no stubs).
type (
	Thinker       = nerve.Thinker
	Effector      = nerve.Effector
	Closer        = nerve.Closer
	Hooks         = nerve.Hooks
	Sandbox       = nerve.Sandbox
	ContextBudget = nerve.ContextBudget
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

// Memory contract for host-implemented memory backends.
type (
	Memory = memory.Memory
	Record = memory.Record
	Query  = memory.Query
)

// Inter-agent messaging (host reference).
type (
	Synapse    = synapse.Synapse
	Edge       = synapse.Edge
	Resolver   = synapse.Resolver
	Signal     = nerve.Signal
	SignalKind = nerve.SignalKind
	TaskStatus = nerve.TaskStatus
)

// NewDirect creates the reference Direct synapse (connection-table with
// Resolver-based delivery). r may be nil until SetResolver is called;
// initial restores a previously exported graph (host persistence round-trip).
func NewDirect(r Resolver, initial ...Edge) Synapse { return synapse.NewDirect(r, initial...) }

// STDPParams tunes the reference STDP learning rule (host-side learning;
// the framework never applies learning rules itself).
type STDPParams = synapse.STDPParams

// Reference learning rules (host-callable plasticity loops; the framework
// stores state and never decides when to learn).
var (
	Hebbian     = synapse.Hebbian
	STDP        = synapse.STDP
	Prune       = synapse.Prune
	HebbianFire = synapse.HebbianFire
)

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

// SignalKind constants.
const (
	KindStimulus SignalKind = nerve.KindStimulus
	KindResponse SignalKind = nerve.KindResponse
	KindNotice   SignalKind = nerve.KindNotice
)

// TaskStatus constants (A2A-style task lifecycle states).
const (
	TaskSubmitted  TaskStatus = nerve.TaskSubmitted
	TaskWorking    TaskStatus = nerve.TaskWorking
	TaskNeedsInput TaskStatus = nerve.TaskNeedsInput
	TaskCompleted  TaskStatus = nerve.TaskCompleted
	TaskFailed     TaskStatus = nerve.TaskFailed
	TaskCancelled  TaskStatus = nerve.TaskCancelled
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
