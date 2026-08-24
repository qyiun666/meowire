# Changelog

All notable changes to meowire are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.1] - 2026-08-24

### Breaking changes

- **`Synapse` extended to a plastic synapse graph** — `Link` gains a
  `weight float64` parameter (initial strength; idempotent overwrite;
  negative clamps to 0); three new methods: `Unlink` (synapse elimination,
  missing connection → `ErrNotLinked`, empty rows dropped), `Reinforce`
  (LTP/LTD delta, result floor 0), `Edges` (out-edge or whole-graph
  snapshot). Old two-method implementations must be updated; no
  compatibility shim (single-path policy).

### Added

- **`Edge{From, To, Weight, Fired}`** — synaptic edge type; re-exported at
  the api facade. `Fired` counts successful deliveries (incremented by
  `Fire`).
- **Persistence round-trip** — export the whole graph via `Edges(ctx, "")`
  (deep-copied snapshot), restore via `NewDirect(resolver, initial...)`;
  `Agent.New` stays unaware (host-domain assembly, composition root).
- `Direct` upgrades: weighted edge table, delivery counter, empty-row
  cleanup, constructor initial edges (negative weights clamp).

### Docs

- `docs/synapse-plasticity.md` — 1.1.1 design: bionic mapping
  (synaptogenesis / elimination / LTP-LTD / pruning / persistence),
  interface contract, migration guide, test plan.
- `internal/synapse/agent.md`, `api/agent.md`, `docs/host-integration.md` /
  `docs/host-integration.en.md` — Synapse contract synced (plastic graph,
  Edge, persistence loop).

## [1.1.0] - 2026-08-24

### Breaking changes

- **`New(o Organs, cfg Config)` replaced by `New(b Blueprint)`** — assembly now
  takes a single `Blueprint{Organs, Config, Strict}` value; define once, New
  many times. All existing host call sites must wrap `Organs`/`Config` into a
  `Blueprint`. No compatibility shim is kept (single-path policy).
- **`New` error aggregation** — with multiple missing required ports the
  returned error is `errors.Join`-aggregated (one `meow: required port X not
  injected` line per port, `\n`-separated). Single missing-port errors keep
  the exact previous format; hosts should match with `errors.Is` /
  `strings.Contains`, never exact string equality.

### Added

- **`Blueprint.Strict`** — when true, warn-level `Validate` findings
  (half-wired hook pairs, incomplete memory/plan paths) also abort `New`;
  error-level findings (missing required ports) always abort.
- **Wiring blueprint is now a graph** — `ConnectomeNodes()` are the
  data-object nodes (prompt/context/plan/bounds/decision/action/effect/err/
  output/timing/resources/hooks); each `WirePoint` gains `TargetID` linking
  the slot edge to its node; `Connectome()` / `ConnectomeNodes()`
  re-exported through the api facade.
- **`BuildGraph(o Organs) WiringGraph`** — assembled graph (nodes + slot
  edges with filled state); `SlotsByTarget(o, targetID)` is the
  find-by-function query (who touches Context → P6/H3/F1); `RenderDiagram`
  now renders nodes section + edges section; `RenderJSON` is the
  machine-readable counterpart for observability and diff-based review.
- **`EventSandbox` audit event** — every decision of a configured `Sandbox`
  (allowed or denied) now yields an event carrying a `SandboxVerdict`
  (CellID, tool call, Allowed, policy Reason, evaluation error). With no
  sandbox wired no verdict is emitted. Hosts persist the event stream to
  build the action-level audit trail required by the Authority model.
- **Dynamic wiring: `Agent.Replace(slot, port)`** — runtime port swapping
  (think/act/sandbox/budget/hooks), the plasticity counterpart of the
  static Blueprint assembly. Takes effect at the next `Stimulate` (each
  Stimulate snapshots ports into a fresh LoopContext; an in-flight
  Stimulate keeps the ports it started with); returns the previous port;
  safe for concurrent use; no-op after Close. Slot constants
  `SlotThink`/`SlotAct`/`SlotSandbox`/`SlotBudget`/`SlotHooks`. Closer and
  PauseGate are never swappable.
- **`AgentCard(o Organs) ([]byte, error)`** — A2A-style machine-readable
  capability card (JSON: name/description/skills), projected entirely from
  the assembly (`ID`/`Identity`/`Methods`); hosts publish it at
  `/.well-known/agent-card.json` for agent discovery.
- **`TaskStatus` task lifecycle states** — A2A-style six states
  (submitted/working/needs-input/completed/failed/cancelled) carried by
  `Signal.Status` for end-to-end inter-agent task tracking; re-exported at
  the api facade.

### Docs

- `docs/protocols.md` — new protocol mapping guide: how hosts map meowire
  ports/events/contracts onto 2026 industry standards (MCP, A2A, AGENTS.md,
  Authority, long-running task state externalization), how meowire differs
  from DeepSeek Harness (Cordis); dynamic wiring, Agent Card and task states
  now have first-class API counterparts.
- `docs/host-integration.md` / `docs/host-integration.en.md` — §1 flow table,
  §5 New assembly validation (Blueprint/Strict/graph check), sample code
  updated to `New(bp)`.
- README (en/zh-CN), `api/agent.md`, `internal/nerve/agent.md`,
  `internal/cell/agent.md`, `api/doc.go` — all contracts synced.

## [1.0.2] - 2026-08-22

### Breaking changes

- **`Hooks.AfterAct` now receives the raw tool error** — signature changed from
  `func(ctx, a *Action, e *Effect)` to
  `func(ctx, a *Action, e *Effect, err error)`; `err` non-nil means the
  effector failed (this also covers `Effect.Err` / nil-effect cases with the
  original error). Migration: add the trailing `err error` parameter to any
  host `AfterAct` callback.
- **`Sandbox` interface now requires `Bounds() string`** — hosts must add the
  method to their Sandbox implementations. `Bounds` returns the execution
  boundary description; the framework snapshots it once per `Stimulate` and
  surfaces it to the Thinker via the new `Prompt.Bounds` field.

### Added

- **`Hooks.BeforeStimulate` / `Hooks.AfterStimulate`** — Stimulate-level
  boundary hooks firing exactly once per `Stimulate` (normal, error, and
  early-consumer-stop paths): `BeforeStimulate` runs before any event is
  yielded and receives a Prompt prototype whose content fields
  (System/Identity/Methods/Tools/Context/Input/Plan) are written back to the
  loop, so one-shot injections (e.g. turn-level memory Recall) apply to every
  round; `AfterStimulate` runs after the cycle ends (e.g. turn-level memory
  Save). `State` is loop-managed and not written back.
- **`BeforeStimulate` mutation guarantees** — the prototype's `Context` is a
  shallow copy (in-place edits or an early hook error never leak into the
  loop); it fires before the ctx-cancellation check so it still runs exactly
  once on a canceled context; `AfterStimulate` is protected from an
  `OnCycleEnd` panic via a nested defer.
- **`Agent.Pause()` / `Agent.Resume()`** — process-level pause control.
  Pause is gap-effective (checked before each Think and before each tool
  execution); a pending pause yields `EventState(StatePaused)` and the loop
  keeps its in-cycle state until resumed. Idempotent, concurrency-safe,
  no-op after `Close`; pause state is Agent-level and survives across
  `Stimulate` calls.
- **`Prompt.Bounds`** — execution boundary description (Sandbox.Bounds
  snapshot), part of the fixed Prompt block.
- **`Config.ToolTimeout`** — per-tool execution timeout (per-attempt;
  timeout-derived errors are not retried and flow back as feedback).
- **`Config.ToolMaxRetries`** — tool retry count on effector errors only;
  `Effect.Err` (business errors) is never retried to avoid duplicate side
  effects. Zero-value semantics: disabled.

### Docs

- `docs/host-integration.md` / `docs/host-integration.en.md` — Hooks struct,
  trigger table, and multi-agent sample updated to the new signatures;
  `BeforeStimulate` / `AfterStimulate` usage patterns documented; §2.4
  Pause/Resume (gap semantics, StatePaused event), Sandbox.Bounds (§2.6),
  Config rows for ToolTimeout/ToolMaxRetries, pause event sequence,
  pitfall #9.
- README (en/zh-CN), `internal/nerve/agent.md`, `api/agent.md` — module
  contract synced (PauseGate, Bounds, ToolTimeout, ToolMaxRetries).

## [1.0.1] - 2026-08-21

### Breaking changes

- **`Identity` struct removed** — `Prompt.Identity` and `Organs.Identity` changed
  from the `Identity` struct (`{Name, Role, Traits, Methods}`) to a plain
  `string`. meowire only owns the Prompt module layout (fixed / mixed /
  dynamic); the identity description text is composed by the host.
  - Migration: pass a self-composed identity text at assembly, e.g.
    `Identity: "You are meow, role assistant, warm tone"`. Any host code that
    read `p.Identity.Name` / `p.Identity.Role` / `p.Identity.Traits` /
    `p.Identity.Methods` will not compile and must be updated.
  - The `meowire.Identity` type alias no longer exists.

### Added

- **`Methods []MethodSpec`** — new independent Prompt module (fixed part) for
  built-in capability description (gene projection, describes only). It is
  injected via `Organs.Methods` and mirrors `ToolSpec` (`{Name, Desc, Input,
  Output}`), ready for future built-in tool schema projection.
- `docs/host-integration.md` / `docs/host-integration.en.md` — host-side
  integration guide: the six ports, field-by-field Prompt/Organs semantics,
  event stream, and the pitfall checklist.
- README (en/zh-CN): Installation section and links to the Host Integration Guide.

### Fixed

- `Cell.Stimulate` now defensively clones the `Methods` slice into
  `LoopContext`, consistent with `Tools` and `Context` (no shared backing
  array between the cell and the Thinker).

## [0.1.0] - 2026-07-20

### Added

- Initial meowire agent harness base: facade + composition root (`api`),
  agent kernel (`internal/cell`), nerve decision loop (`internal/nerve`),
  standalone contracts for memory and synapse.
- Core lifecycle: `New` / `Stimulate` / `Close` with `iter.Seq[Event]` event
  stream (text, tool call/result, state, done, error, usage).
- Six required host ports: Thinker / Effector / Closer / Hooks / Sandbox /
  ContextBudget; no stubs, no default implementations.
- Zero external dependencies; Go standard library only.
