# MeowAgent meowire api — facade + composition root

## Purpose

- Facade: Agent struct — Stimulate returns `iter.Seq[Event]`, Close shuts down
- Composition root: assemble.go is the single assembly point (port validation, cell wiring)
- Types: re-exports all internal contract types (ports, data packets, events, memory, synapse)

## Dependencies

- internal/cell (agent kernel)
- internal/nerve (Event, ports, Hooks, MethodSpec, Signal/Message)
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

- `Organs{ID, Think, Act, Closer, Hooks, Sandbox, Budget, System, Identity, Methods, Tools, Context}`: host port container — **all six ports required**, a missing port returns an error (no stubs, no default implementations)
- `Blueprint{Organs, Config, Strict}`: host assembly blueprint instance; define once, `New` many times. `New(b Blueprint) (*Agent, error)` — error-level findings (missing ports) always block; `Strict: true` also blocks warn-level findings (half-wired hook pairs, incomplete memory/plan paths); info findings never block
- `Config{MaxRounds, MaxToolOutput, MaxRetries, ToolTimeout, ToolMaxRetries}`: zero values fall back to defaults (8 rounds, no truncation, no retry, no per-tool timeout/retry)
- `ID` empty defaults to "agent" (flat model: host gives each instance a unique ID for synapse routing/logs)
- Facade methods are thin delegates: `Stimulate` → Cell.Stimulate (returns `iter.Seq[Event]`); Close → Cell.Close + host Closer; Close is idempotent
- `Pause()` / `Resume()`: Agent-level pause control — idempotent, concurrency-safe, no-op after Close; takes effect at the next gap point (before each Think / tool execution), yields EventState(StatePaused), keeps in-cycle state until resumed
- `Replace(slot, port) (any, error)`: dynamic wiring — runtime port swap (SlotThink/SlotAct/SlotSandbox/SlotBudget/SlotHooks); takes effect at the next Stimulate (in-flight Stimulate keeps its ports); returns the previous port; concurrency-safe; no-op after Close; Closer/PauseGate never swappable
- `AgentCard(o Organs) ([]byte, error)`: A2A-style capability card (JSON name/description/skills) projected from ID/Identity/Methods; publish at /.well-known/agent-card.json for agent discovery
- After Close, Stimulate returns ErrCellClosed
- Errors: ErrCellClosed defined here; synapse errors (ErrNoTarget/ErrNotLinked/ErrTargetBusy) re-exported via errors.go

## Wiring Blueprint (graph-based assembly inspection)

- The blueprint is a **graph**: `ConnectomeNodes()` are the data-object nodes (prompt/context/plan/bounds/decision/action/effect/err/output/timing/resources/hooks); `Connectome()` is the slot edge list (ports P1–P6, hooks H1–H8, built-ins F1/G1). Each edge carries ID/Name/Phase(1 framework·2 host port·3 hook)/Category(sense-decide-act)/TargetID(graph node it reads or mutates)/Semantics(replace-append-trim-gate-read-only-act-container)/Parallel/Required/Desc
- `WiringDiagram(o Organs) []Slot`: extracts actual assembly state (filled/unwired) from Organs — no host registration needed; built-ins always filled
- `BuildGraph(o Organs) WiringGraph`: assembled graph — canonical nodes + slot edges with filled state
- `SlotsByTarget(o Organs, targetID) []Slot`: **find by function, not by port** — who touches a data object? e.g. Context → P6 (trim), H3 (replace), F1 (append)
- `Validate(o Organs, cfg Config) []Issue`: blueprint × assembly comparison. Levels: error (required slot missing), warn (half-wired hook pairs H1↔H2/H3↔H4/H5↔H6; memory path = H3 without configured ContextBudget trimmer; plan path = H4 without H3), info (empty Identity/Tools/Context, MaxRounds<=0 default)
- `RenderDiagram(o Organs) string`: ASCII wiring graph (nodes section, then one edge per line with [x]/[ ] mark and -> target node)
- `RenderJSON(o Organs) ([]byte, error)`: machine-readable counterpart — marshals the assembled graph (nodes + slots with filled state) as indented JSON
- Result types: `Slot{Wire WirePoint, Filled bool}` (blueprint entry + filled state), `Issue{ID, Level, Wire, Msg string}` (Validate finding; `Level` ∈ error/warn/info), `WiringGraph{Nodes []WireNode, Slots []Slot}` (assembled graph), `IssueLevel` constants `LevelError` / `LevelWarn` / `LevelInfo`
- `New` rejects error-level findings always (missing ports, joined via errors.Join); with `Blueprint.Strict` warn findings also reject; hosts surface warn/info findings by calling Validate at assembly/test time
- Single-sided hooks are legal (e.g. H3-only memory injection); the warn is advisory, not a hard error

## Key Decisions

- Flat model: one Agent = one kernel; host manages multiple instances for multi-agent
- Sub-agent = host tool pattern (spawn_agent tool), not framework-level nesting
- Multi-agent messaging = host tool pattern (send_message tool over synapse.Fire); resistance yields as EventToolResult feedback, loop continues
- Synapse is a plastic graph since 1.1.1: `Link(ctx, from, to, weight)` / `Unlink` / `Reinforce(ctx, from, to, delta)` / `Fire` / `Edges(ctx, from)`; `Edge{From, To, Weight, Fired}` re-exported; persistence loop: `Edges(ctx, "")` export → host stores → `NewDirect(resolver, restored...)` restore (host-domain assembly, Agent.New unaware)
- Signal carries `Status TaskStatus` (A2A-style: submitted/working/needs-input/completed/failed/cancelled) for end-to-end task lifecycle tracking
- History managed by host (MemHop): host controls context accumulation via Organs.Context
- Host-driven multi-round resume (Step-Resume) = stop iterator + self-managed history + re-Stimulate; each Stimulate is one stateless step
- Streaming: host Thinker consumes LLM stream chunks and pushes them to its own channel (e.g. WebSocket); the event stream is an observation mirror of loop execution — EventText stays whole-segment, token deltas never enter it
- ask_user: Effector.Act is synchronous; host blocks inside Act (e.g. PauseAskUser) and must monitor ctx.Done so cancellation interrupts the wait
- Dynamic memory injection: Stimulate has no per-round context channel; per-round retrieval happens in Hooks.BeforeThink; host controls once-vs-every-round semantics with its own closure flag
- Guards/emotion/plan: ContextBudget trims context before each Think (trimmer, not hard stop); MaxRetries retries Think only — tool-failure guards are host-side (Effector or AfterAct); ToolTimeout/ToolMaxRetries bound tool execution (per-attempt timeout; retry on effector err only, Effect.Err never retried); identity/emotion is host-composed into the `Identity` string and rendered by the host Thinker; Plan is a host-serialized string injected via BeforeThink and updated via AfterThink
- Sandbox: host membrane (tool security gate: allowlist/denylist, ask-confirm) maps to Sandbox.Allow, invoked before each Act; every decision of a configured sandbox first yields EventSandbox carrying a `SandboxVerdict{CellID, Call, Allowed, Reason, Err}` (action-level audit record; no sandbox = no verdict); denial then yields EventToolResult(Err) and the loop continues; Sandbox.Bounds() is snapshotted once per Stimulate into Prompt.Bounds (execution boundary description surfaced to the LLM)
- Assembly only at composition root: components inside must not create dependencies
- No stubs/sentinels: all six ports must be injected; there is no "minimal runnable" path

## Pitfalls

- Host ports (Thinker/Effector) must be concurrency-safe if same Agent is stimulated concurrently
- Pause is gap-effective: it never interrupts an in-flight Think/Act — interrupt a running tool with ctx cancellation instead
- Stopping the iterator mid-cycle abandons the round; tools at or after the stop point do not execute
- Host ports must respect ctx (long operations must monitor ctx.Done)
- Hooks.BeforeThink must replace p.Context as a whole slice — it shares the backing array with lc.Context; appending into it can corrupt the loop's accumulated context
- Streaming UX lives in the Thinker (host side); the event stream never carries token deltas (EventText is whole-segment by contract)
- Synapse errors (ErrNoTarget/ErrNotLinked/ErrTargetBusy) re-exported at api level
- Facade adds no business logic (delegation only); extend semantics in capability packages or host
