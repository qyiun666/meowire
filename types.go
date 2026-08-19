// types.go — public contract types: aliases of the internal definitions.
//
// The root package is the only importable surface of meowire. All
// implementation packages live under internal/ and are sealed by the Go
// compiler; hosts interact with the framework through New/Stimulate/Close
// plus the contract types below (ports, data packets, events, memory).
package meowire

import (
	"github.com/qyiun666/meowire/internal/memory"
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
)

// Data packets exchanged with the host ports.
type (
	Identity   = nerve.Identity
	MethodSpec = nerve.MethodSpec
	Prompt     = nerve.Prompt
	ToolSpec   = nerve.ToolSpec
	Decision   = nerve.Decision
	ToolCall   = nerve.ToolCall
	Action     = nerve.Action
	Effect     = nerve.Effect
	Usage      = nerve.Usage
)

// Events yielded by the Stimulate iterator.
type (
	Event     = nerve.Event
	EventKind = nerve.EventKind
	LoopState = nerve.LoopState
)

// Memory contract for host-implemented memory backends.
type (
	Memory = memory.Memory
	Record = memory.Record
	Query  = memory.Query
)

// LoopState constants.
const (
	StateIdle     LoopState = nerve.StateIdle
	StateThinking LoopState = nerve.StateThinking
	StateActing   LoopState = nerve.StateActing
	StatePaused   LoopState = nerve.StatePaused
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
)

// DefaultMaxRounds is the default round limit when Config.MaxRounds <= 0.
const DefaultMaxRounds = nerve.DefaultMaxRounds
