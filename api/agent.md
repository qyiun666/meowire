# MeowAgent meowire api — facade + composition root

## Purpose

- Facade: Agent struct — Stimulate returns `iter.Seq[Event]`, Close shuts down
- Composition root: assemble.go is the single assembly point (port validation, cell wiring)
- Types: re-exports all internal contract types (ports, data packets, events, memory, synapse)

## Dependencies

- internal/cell (agent kernel)
- internal/nerve (Event, ports, Identity, Hooks, Signal/Message)
- internal/synapse (inter-agent connections, re-exported errors)
- internal/memory (Memory/Record/Query type aliases)
- Go standard library only

## Sealed Internals

- All implementation packages live under internal/ and are not importable
  outside this module (Go compiler enforced)
- This api package is the sole public surface: New/Stimulate/Close plus the
  contract types in types.go
- Hosts implement the six ports against api-package aliases
  (Thinker/Effector/Closer/Hooks/Sandbox/ContextBudget); they never touch
  internal packages directly

## Interface Contract

- `Organs{ID, Think, Act, Closer, Hooks, Sandbox, Budget, System, Tools, Context, Identity}`: host port container; `New(o Organs, cfg Config) (*Agent, error)` — **all six ports required**, a missing port returns an error (no stubs, no default implementations)
- `Config{MaxRounds, MaxToolOutput, MaxRetries}`: zero values fall back to defaults (8 rounds, no truncation, no retry)
- `ID` empty defaults to "agent" (flat model: host gives each instance a unique ID for synapse routing/logs)
- Facade methods are thin delegates: `Stimulate` → Cell.Stimulate (returns `iter.Seq[Event]`); Close → Cell.Close + host Closer; Close is idempotent
- After Close, Stimulate returns ErrCellClosed
- Errors: ErrCellClosed defined here; synapse errors (ErrNoTarget/ErrNotLinked/ErrTargetBusy) re-exported via errors.go

## Key Decisions

- Flat model: one Agent = one kernel; host manages multiple instances for multi-agent
- Sub-agent = host tool pattern (spawn_agent tool), not framework-level nesting
- Multi-agent messaging = host tool pattern (send_message tool over synapse.Fire); resistance yields as EventToolResult feedback, loop continues
- History managed by host (MemHop): host controls context accumulation via Organs.Context
- Host-driven multi-round resume (Step-Resume) = stop iterator + self-managed history + re-Stimulate; each Stimulate is one stateless step
- Streaming: host Thinker consumes LLM stream chunks and pushes them to its own channel (e.g. WebSocket); the event stream is an observation mirror of loop execution — EventText stays whole-segment, token deltas never enter it
- ask_user: Effector.Act is synchronous; host blocks inside Act (e.g. PauseAskUser) and must monitor ctx.Done so cancellation interrupts the wait
- Dynamic memory injection: Stimulate has no per-round context channel; per-round retrieval happens in Hooks.BeforeThink; host controls once-vs-every-round semantics with its own closure flag
- Guards/emotion/plan: ContextBudget trims context before each Think (trimmer, not hard stop); MaxRetries retries Think only — tool-failure guards are host-side (Effector or AfterAct); emotion rides Identity.Traits into Thinker LLM params; Plan is a host-serialized string injected via BeforeThink and updated via AfterThink
- Sandbox: host membrane (tool security gate: allowlist/denylist, ask-confirm) maps to Sandbox.Allow, invoked before each Act; denial yields EventToolResult(Err) and the loop continues
- Assembly only at composition root: components inside must not create dependencies
- No stubs/sentinels: all six ports must be injected; there is no "minimal runnable" path

## Pitfalls

- Host ports (Thinker/Effector) must be concurrency-safe if same Agent is stimulated concurrently
- Stopping the iterator mid-cycle abandons the round; tools at or after the stop point do not execute
- Host ports must respect ctx (long operations must monitor ctx.Done)
- Hooks.BeforeThink must replace p.Context as a whole slice — it shares the backing array with lc.Context; appending into it can corrupt the loop's accumulated context
- Streaming UX lives in the Thinker (host side); the event stream never carries token deltas (EventText is whole-segment by contract)
- Synapse errors (ErrNoTarget/ErrNotLinked/ErrTargetBusy) re-exported at api level
- Facade adds no business logic (delegation only); extend semantics in capability packages or host
