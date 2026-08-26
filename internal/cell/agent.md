# MeowAgent cell module context (capability layer: agent kernel)

> Package lives at internal/cell — sealed from external import; hosts use only the api package API.

## Purpose

- Capability layer: minimal agent kernel (ID + ports + DecisionLoop → iter.Seq[Event])
- Depends on nerve; does not depend on synapse/memory/root

## Dependencies

- nerve (DecisionLoop, Event, Hooks, Thinker, Effector, Sandbox, ContextBudget, MethodSpec)
- Go standard library only

## Interface Contract

- `Cell`: minimal agent kernel
  - `ID string`: unique identifier
  - `Identity string`: identity description text (host composed, injected into Prompt)
  - Required ports: `Think nerve.Thinker`, `Act nerve.Effector`
  - Optional ports: `Hooks *nerve.Hooks`, `Sandbox nerve.Sandbox`, `Budget *nerve.ContextBudget`, `PauseGate func() *nerve.PauseGate` (nil = pause unsupported; factory called per Stimulate for a fresh gate; since v1.3.2 PauseGate carries only IsPaused — an honored pause yields EventPaused + Session and ends the iterator, resumed via Resume(sess, ""))
  - Config: `MaxRounds`, `MaxToolOutput`, `MaxRetries`, `ToolTimeout`, `ToolMaxRetries`, `ParallelActs` (zero values use defaults / disabled; ParallelActs=false = strict serial); `UpdateConfig(cfg)` swaps wholesale at the next Stimulate/Resume and — since v1.3.2 — records a `ConfigAudit{CellID, Old, New}` drained into the next LoopContext and emitted as EventConfig after EventReplace (the config-update counterpart of the Replace audit)
  - Host-injected fixed parts: `System string`, `Methods []nerve.MethodSpec`, `Tools []nerve.ToolSpec`, `Context []string`
  - `Stimulate(ctx, text) iter.Seq[nerve.Event]`: runs DecisionLoop, yields events; snapshots ports under the wire lock, so a concurrent Replace takes effect at the next Stimulate
  - `Replace(slot string, port any) (any, error)`: dynamic wiring — swaps think/act/sandbox/budget/hooks at runtime; returns the previous port; takes effect at the next Stimulate; concurrency-safe (wireMu); no-op after Close; unknown slot / wrong port type errors; **rejects nil/incomplete ports** (Budget needs Trimmer+MaxTokens via completeHooks for Hooks); Closer/PauseGate not swappable
  - `Close() error`: idempotent close (marks cell as closed)
  - `IsClosed() bool`: query close status

## Key Decisions

- Cell is the minimal kernel: no Colony, no Divide, no tree management
- Multi-agent is achieved via flat model: host creates multiple Cell instances (via root New)
- Sub-agent = host tool pattern (spawn_agent tool), not framework-level nesting
- Stimulate returns `iter.Seq[nerve.Event]` — host consumes events directly
- Close is idempotent (mutex-protected boolean flag)
- Memory is host-managed (MemHop): no Mem port on Cell; host injects context via `Context` field
- All errors wrapped with `fmt.Errorf("...: %w", err)`

## Pitfalls

- Cell does not manage pumps, inboxes, or signal routing (those are host responsibilities in flat model)
- Host ports must respect ctx (long operations must monitor ctx.Done)
- Stimulate creates a fresh LoopContext per invocation (no state carried between calls)
- Replace is the plasticity counterpart of Blueprint assembly: it mutates the cell's port fields under wireMu; in-flight Stimulates keep the ports they snapshotted; since 1.2.0 it rejects nil/incomplete ports (a missing organ cannot be swapped in)
- Stimulate iterator abort = abandon the round; the tool at the abort point does not execute
- Context and Tools slices are copied on each Stimulate to prevent mutation across calls
