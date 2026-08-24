// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

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
//   - Wiring blueprint: internal/nerve/wire.go (wiring graph — ConnectomeNodes
//     are the data-object nodes, Connectome is the slot edge list; each slot
//     carries TargetID, Semantics, Phase, Category, Parallel, Required),
//     wiring.go (WiringDiagram / BuildGraph / SlotsByTarget / Validate /
//     RenderDiagram / RenderJSON: blueprint × assembly comparison as a graph)
//   - Dynamic wiring: Agent.Replace(slot, port) swaps runtime ports between
//     Stimulates (plasticity); AgentCard(Organs) renders the A2A-style
//     capability card; Signal.Status carries A2A task lifecycle states
//   - Synapse: internal/synapse/ (plastic synapse graph: Link/Unlink/Reinforce/
//     Fire/Edges + reference learning rules Hebbian/STDP/Prune)
//   - Memory: internal/memory/ (standalone contract, host reference)
//   - Composition: assemble.go (Blueprint{Organs, Config} + New(Blueprint)
//     single assembly point; every wiring point is required — error-level
//     findings always block, no warn level; FullHooks fills declared no-ops)
//   - Facade: meow.go (Agent: Stimulate returns iter.Seq[Event]; Pause/Resume; Close)
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
// All six ports (Think/Act/Closer/Hooks/Sandbox/Budget) and all eight hook
// callbacks (H1–H8) are required — New rejects a missing port or callback;
// there are no stubs, no default implementations, no optional wiring.
package meowire
