// Package meowire is the public facade of the meowire harness.
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
//   - Events: internal/nerve/event.go
//   - Synapse: internal/synapse/ (inter-agent connections: Link/Fire only)
//   - Memory: internal/memory/ (standalone contract, host reference)
//   - Composition: assemble.go (New(Organs, Config) single assembly point)
//   - Facade: meow.go (Agent: Stimulate returns iter.Seq[Event]; Close)
//
// Sealed internals: all implementation packages live under internal/ and are
// not importable outside this module. The api package is the sole public
// surface of the meowire module.
//
// Dependency graph (strictly unidirectional):
//
//	meowire/api (facade + composition root)
//	  ├── internal/cell → internal/nerve
//	  └── internal/synapse → internal/nerve
//	internal/memory: standalone contract, not consumed by the framework
//
// All six ports (Think/Act/Closer/Hooks/Sandbox/Budget) are required —
// New rejects a missing port; there are no stubs or default implementations.
package meowire
