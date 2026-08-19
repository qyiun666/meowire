// Package meowire is a bionic harness base — pure wiring, no default implementations.
//
// Architecture:
//
//   - Single agent kernel: Cell = ID + ports + DecisionLoop (nerve.DecisionLoop{}.Cycle)
//   - DecisionLoop: Think → Act → yield events (iter.Seq[Event] return model)
//   - Flat model: the host manages multiple Agent instances for multi-agent scenarios
//   - Sub-agent = host tool (spawn_agent tool pattern, not framework-level nesting)
//   - History managed by host (MemHop pattern: host controls context accumulation)
//
// Concept map:
//
//   - DecisionLoop: internal/nerve/loop.go (pure orchestration: Think→Act→yield events)
//   - Cell: internal/cell/cell.go (ID + ports + DecisionLoop → iter.Seq[Event])
//   - Ports: internal/nerve/port.go (Thinker/Effector/Closer), internal/nerve/hook.go (Hooks)
//   - Guard ports: internal/nerve/sandbox.go (Sandbox), internal/nerve/context.go (ContextBudget)
//   - Events: internal/nerve/event.go (EventText/EventToolCall/EventToolResult/EventState/EventDone/EventError/EventUsage)
//   - Synapse: internal/synapse/ (inter-agent connections: Link/Fire only)
//   - Memory: internal/memory/ (standalone contract: Memory interface + Record/Query, host reference)
//   - Composition: assemble.go (New(Organs, Config) single assembly point)
//   - Facade: meow.go (Agent: Stimulate returns iter.Seq[Event]; Close)
//
// Sealed internals: all implementation packages live under internal/ and are
// not importable outside this module — the root package is the only public
// surface (New/Stimulate/Close plus the contract types re-exported in types.go).
//
// Dependency graph (strictly unidirectional):
//
//	meowire (root, facade + composition root)
//	  ├── internal/cell → internal/nerve
//	  └── internal/synapse → internal/nerve
//	internal/memory: standalone contract, not consumed by the framework
//
// All six ports (Think/Act/Closer/Hooks/Sandbox/Budget) are required —
// New rejects a missing port; there are no stubs or default implementations.
// Multi-agent orchestration is host-side: sub-agents and inter-agent messaging
// are host tools (spawn_agent / send_message) whose results flow back into the
// loop as EventToolResult feedback; resistance (tool errors, denied calls,
// busy targets) is yield-ed to the host as feedback, never a hard stop.
//
// Event stream & host-driven resume model:
//
//   - The event stream is an observation mirror of the loop, not a
//     request-response channel. Tool execution happens inside the loop via the
//     host-injected Effector port; results are appended to the next round's
//     Prompt.Context automatically. The host observes EventToolCall/
//     EventToolResult but never feeds data back into an open iterator
//     (iter.Seq yields one-way).
//   - Abort semantics: stopping consumption (yield returns false) abandons the
//     current round. If stopped at an EventToolCall, that tool does NOT
//     execute. All state accumulated in this round is discarded.
//   - Host-driven multi-round resume (Step-Resume): stop the current iterator,
//     execute the tool host-side (human approval, async work, external
//     service), append the result to host-managed history, then call
//     Stimulate again. Each Stimulate is one stateless step (a fresh
//     LoopContext is created per call).
//   - OnCycleEnd is guaranteed to run exactly once per Cycle (normal, error,
//     or aborted). Close only affects subsequent Stimulate calls; an in-flight
//     iterator is not interrupted.
package meowire
