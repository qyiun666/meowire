# Changelog

All notable changes to meowire are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.3.5] - 2026-08-27

### Fixed

- **ParallelActs: BeforeAct mutations now apply on the parallel path** —
  phase 2 previously reconstructed the Action from the raw ToolCall,
  silently discarding any rewrite the `BeforeAct` hook made (the serial
  path executes the mutated Action). The batch now carries both identities:
  the gated Action is executed, the original ToolCall stays the
  feedback/snapshot identity — byte-identical hook semantics across paths
  (regression test `TestParallelActsBeforeActMutationApplies`).

### Changed

- **Shared execution primitives (dedup)** — the sandbox-membrane gate
  (EventSandbox audit + denial feedback + BeforeAct) and the wait
  suspension (Session snapshot + StateWaiting + EventWaitInput) are
  extracted into `gateTool`/`emitWait`, now used by both the serial and the
  parallel path (previously ~90%/~85% duplicated blocks); `runOneTool`'s
  four-value return maze is gone. The nil-effect port-contract guard moves
  into `actWithRetry` — the single Act chokepoint both paths run through.
- **Go 1.25+ idioms** — the parallel batch join uses `sync.WaitGroup.Go`
  with per-iteration loop-variable capture (Go 1.22+ semantics) instead of
  `Add`/`Done` + explicit parameter passing.

No public surface change; event ordering and wire formats are untouched.

## [1.3.4] - 2026-08-27

### Changed

- **Go 1.27 baseline** — `go.mod` now requires Go 1.27. The upgrade is a
  runtime/toolchain gain (faster small allocations, goroutine-leak profile,
  smarter `go fix`/`go doc`); the zero-dependency promise and the whole
  public surface are unchanged. `encoding/json` stays as-is for the Session
  wire shape (`sessionVersion` untouched) — json/v2 is deliberately not
  adopted (wire-format stability over novelty).
- **Modernized idioms** (`go fix` 1.27 modernizers, reviewed):
  integer-range `for range N` loops in tests, `slices.Contains` in
  loop_test.go, `strings.SplitSeq` in contract_sync_test.go. The
  retry-count clamp in `thinkWithRetry`/`actWithRetry` deliberately keeps
  the explicit if-form (the `max()` rewrite buries the "negative = no
  retry" contract comment).

### Docs

- `README.md`/`README.zh-CN.md` — requirement bumped to Go 1.27+.

## [1.3.3] - 2026-08-27

### Added

- **`Config.ParallelActs` — same-round tool batch parallelism (opt-in)** — when
  enabled and a round produces multiple tool calls, `runToolCalls` diverts to a
  three-phase batch path (`internal/nerve/parallel.go`):
  1. **Serial gating** — every call announced as `EventToolCall` in call order,
     one pause gap point for the whole batch (a hit snapshots the whole batch
     unexecuted; Resume replays it), per-call sandbox verdict (`EventSandbox`
     audit unchanged) and `BeforeAct`; a denied call gets its `[denied: reason]`
     feedback in place and is skipped without affecting its siblings.
  2. **Parallel execution** — one goroutine per admitted call runs `actWithRetry`
     (timeout/retry included); results land by call index. Hooks, events, and
     loop state never enter this phase.
  3. **Serial feedback** — `AfterAct` → `ToolResult` → `EventToolResult` in call
     order, never completion order.
  Zero value false keeps strict serial execution (v1.3.2 behavior; existing
  hosts are unaffected). A single call always keeps the serial path. A
  `WaitInput` result inside the batch suspends with an empty `remaining` — the
  batch has fully executed, so Resume injects the response without replaying
  anything (replaying would duplicate side effects); every sibling's feedback
  is preserved in the snapshot's `ToolResults`. Prerequisite: the Effector
  implementation must be safe for concurrent `Act` calls.
- **`Session.RemainingCalls()`** — the sole sanctioned read-only probe of a
  suspension handle: a clone of the tool calls not yet executed at the
  suspension point (empty when nothing is left to run). Hosts use it in the
  suspension-resume protocol to tell whether Resume will replay tool calls.
  The Session wire shape and `sessionVersion` are unchanged.
- **Validate info finding** — `Validate` reports a `LevelInfo` finding when
  `ParallelActs` is enabled (reminder of the concurrency-safety prerequisite);
  never an error, `New` is unaffected.

### Fixed

- Contract drift: repowiki 事件系统.md now enumerates the v1.3.x event kinds
  (`EventSandbox`/`EventWaitInput`/`EventPaused`/`EventReplace`/`EventConfig`),
  restoring `TestEventKindContractSynced` to green.

### Docs

- `README.md` — Features lists the parallel batch switch and
  `Session.RemainingCalls()`.
- `host-integration.md`/`.en.md` — §4 config table adds `ParallelActs`; §6.4
  suspension notes add the `RemainingCalls()` probe semantics.
- `internal/nerve/agent.md`, `internal/cell/agent.md`, `api/agent.md` —
  contracts synced (LoopConfig/LoopContext/Session enumerations, three-phase
  batch semantics, Validate info finding).

## [1.3.2] - 2026-08-26

### Added

- **Unified suspension-resume** — Pause and ask_user now share one
  snapshot + resume path (LangGraph-interrupt-style single suspension
  primitive): a pause honored at a gap point yields `EventState(StatePaused)`
  + `EventPaused` (opaque `Session` snapshot) and ends the iterator normally;
  `Agent.Resume(ctx, sess, "")` continues (nothing is injected — no pending
  tool). A pause before a tool keeps the current tool (and the calls after
  it) in `Session.remaining`, so Resume runs them first. The old blocking
  `PauseGate.ResumeCh` wait is gone — the loop never blocks on a pause.
  **Breaking**: Pause no longer suspends the same iterator in place; hosts
  resume via `Resume(sess, "")` (event content is equivalent, only the
  iterator boundary changes). `Unpause()` now only clears a pause request
  that has not taken effect yet; `Resume` clears a stale request
  automatically.
- **Session persistence** — `Session.Marshal() ([]byte, error)` /
  `meowire.UnmarshalSession([]byte) (Session, error)`: versioned JSON wire
  shape for both suspension kinds; a suspended or paused loop survives
  process restarts (alignment with mainstream checkpoint/resume). Version
  mismatch and zero-value handles are rejected (a stale or future handle
  must not be replayed). Fields stay unexported — hosts only save and pass
  the handle back (no inspection, no mutation).
- **EventConfig audit** — `Agent.UpdateConfig` records a
  `ConfigAudit{CellID, Old, New}` per call, emitted as `EventConfig` at the
  start of the next Stimulate/Resume after `EventReplace` (the moment the
  swap takes effect) — every "unique update" of the loop is traceable,
  mirroring the `EventReplace` port-swap audit.

### Breaking

- `Pause` semantics (see above): snapshot suspension instead of the
  in-iterator blocking wait; `PauseGate` drops the `ResumeCh` field
  (internal wiring; hosts building their own gate only provide `IsPaused`).
- `Unpause` semantics: clears a not-yet-effective pause request only.
- New event kinds `EventPaused` / `EventConfig` are appended to the
  `EventKind` enum (host switches must handle them).

### Docs

- `README.md` — Features and Core Concepts rewritten around the unified
  suspension-resume model; event table lists `EventPaused`/`EventConfig`;
  Session persistence usage.
- `host-integration.md`/`.en.md` — §2.4 Pause/Unpause rewritten (snapshot
  suspension, Resume-driven continuation, Unpause back-out); §6.4 adds the
  pause branch and Session persistence; event tables list the two new kinds.
- `internal/nerve/agent.md`, `api/agent.md`, `internal/cell/agent.md`,
  repowiki 事件系统.md — contracts synced (PauseGate, Resume conditional
  injection, ConfigAudit, persistence).

## [1.3.1] - 2026-08-25

### Added

- **Structured tool feedback (B1)** — `Prompt.ToolResults`
  (`[]ToolResult{ID, Name, Result, Err}`) is the single tool-feedback track:
  tool results no longer enter the `Context` text track (it keeps host
  base + sandbox denials). `ID` is the LLM-provided call id (`call_xxx`,
  previously dropped); `Result`/`Err` carry the truncated raw output
  (`Err` non-empty = failed, rendering is the host's decision). `Session`
  snapshots the accumulated track, `Resume` appends the external response
  as the pending tool's entry, and `EventToolResult` echoes the call for
  ID association. **Breaking**: hosts must consume `ToolResults` in their
  Thinker to surface tool feedback to the model.

### Docs

- `host-integration.md`/`.en.md` — §6.3 clarifies the boundary between
  Step-Resume (break + Stimulate, host-driven takeover) and the
  suspension-resume protocol (WaitInput + Resume, tool-requested input) in
  plain language: ask_user has exactly one form, and break is the standard
  Go iterator consumption semantics (tools after the stop point never run),
  not a second implementation.

## [1.3.0] - 2026-08-25

### Added

- **Suspension-resume protocol (ask_user)** — `Effect.WaitInput` suspends the
  loop: the framework snapshots an opaque `Session` (round/context/remaining
  tool calls/output), yields `EventState(StateWaiting)` + `EventWaitInput`
  (tool, question, Session) and ends the iterator normally — no blocking, no
  extra round (the digesting Think uses the suspended round's quota), no
  budget during the wait. `Agent.Resume(ctx, sess, response)` continues the
  loop: the response enters the context as `[tool] <response>`, the
  suspended round's remaining tools run first, then the loop resumes from
  the suspended round. Timeouts are host-controlled (resume with
  `[denied: timeout]`). Replaces the host-side synchronous block inside
  Effector. `Session` is an opaque value object (single-use, host-held).
- **Runtime config updates** — `Agent.UpdateConfig(cfg)` swaps the scalar
  loop config wholesale (takes effect at the next Stimulate/Resume),
  `Agent.GetConfig()` reads it back (read-modify-write); `Config` is now an
  alias of the internal `LoopConfig` (single source of truth).
- **Port-swap audit events** — every successful `Replace` records a
  `ReplaceAudit{CellID, Slot, Old, New}` emitted as `EventReplace` at the
  start of the next Stimulate/Resume (the moment the swap takes effect),
  same persistable level as `EventSandbox`; failed swaps record nothing.

### Changed

- **`Agent.Resume()` renamed to `Agent.Unpause()`** — clears a pending
  gap-point pause; `Resume(ctx, sess, response)` now means continuing a
  suspended loop. Hosts calling `Unpause()` must update their call sites.

### Internal

- `loop.go` refactored into reusable segments — `cyclePrelude` (shared
  prologue: Bounds snapshot / BeforeStimulate / ctx check / pending
  `EventReplace` emission), `roundLoop`, `thinkRound`, `runToolCalls`,
  `runOneTool`; `Cycle` and `Resume` share the same path, so the Resume
  event stream is isomorphic with Stimulate (same hooks, same guarantees).
- `cell.Cell` config fields converged into `Config nerve.LoopConfig`;
  `snapshot()` (ports + config + pending Replace audits under the wire
  lock) is shared by Stimulate and Resume.
- **Contract documentation drift guard** — new `test/contract_sync_test.go`
  extracts the `EventKind` enum from `internal/nerve/event.go` (single
  source of truth) and fails when the api aliases (`api/types.go`),
  `internal/nerve/agent.md`, `host-integration.md`/`.en.md` event tables,
  or the repowiki event-system doc drift from it. `reference-host.md`
  stays manual: its event switch is an example, not a contract.

### Docs

- `internal/nerve/agent.md` — added a Contract Change Checklist section
  listing every document a public-contract change must sync.
- repowiki event-system doc — event enumeration restored to the current
  contract (`EventSandbox` had been missing since 1.2.0).
- `host-integration.md`/`.en.md` — §2.4 Pause/Unpause, §4 runtime config
  updates, §5.1 Replace audit, §6.1 suspension sequence, §6.2 event table,
  new §6.4 suspension-resume protocol.
- **Single-path guarantee for ask_user** — README (en/zh-CN),
  `host-integration.md`/`.en.md`, and `reference-host.md` now declare
  `Effect.WaitInput` + `Resume` as the **only** form for a tool requesting
  external input; Step-Resume (break + `Stimulate`) is explicitly scoped to
  host-driven takeover (manual approval, async work, `ErrMaxRounds`
  continuation). The two paths are mutually exclusive — a break-based
  `ask_user` would lose the suspended context (the `Session` is opaque and
  cannot be rebuilt by hand), and the pitfall list now forbids blocking
  inside Effector.

## [1.2.1] - 2026-08-24

### Fixed

- **`Validate` findings are now deterministic** — previously iterated a
  map built only for traversal; issue order could vary between runs and
  break diff-based review. Now iterates the stable diagram slice.
- **`thinkWithRetry` clamps negative retry configs** — a negative
  `Config.ThinkMaxRetries` previously produced a malformed error
  (`%!w(<nil>)`); now aligned with `actWithRetry` (clamped to 0).

### Internal

- Dead code removed: `hadToolCalls` loop variable, `diagramByID`
  (map built only to iterate), three pure-forwarding hook helpers, and an
  unreachable `nil`-effect branch in tool feedback.
- New stdlib APIs where they simplify: `slices.Sorted` + `maps.Keys`
  (graph agent listing), `cmp.Or`/`cmp.Compare` (edge sort), built-in
  `max` (learning decay, no float round-trip).
- Test stubs consolidated: 10 duplicated local stubs across four test
  files replaced by `internal/testutil` (Thinker/Effector/Sandbox/Closer);
  white-box stubs kept where importing testutil would cycle.

### Docs

- `reference-host.md` — new step-by-step reference host: how to
  build a runnable AI host on meowire from scratch (OpenAI-compatible LLM,
  tool dispatch, permission gate, context trimming, memory, multi-agent,
  persistence). Indexed from README (en/zh-CN) and both integration
  guides.

## [1.2.0] - 2026-08-24

### Breaking changes

- **All wiring points are now required (no optional organs)** — hooks
  H1–H8 are mandatory callbacks; a missing callback fails assembly.
  Hosts that want no behavior at a point pass an explicit no-op — an
  explicit no-op is a declared decision, an absent callback is a missing
  organ. `Sandbox` and `ContextBudget` are required ports (as before) and
  the decision loop no longer tolerates nil: the membrane's `Bounds()`
  snapshot and every `EventSandbox` verdict are now unconditional, and
  `Budget.Trimmer` runs before every Think.
- **`Blueprint.Strict` removed** — with every hook required there are no
  warn-level findings left to promote; validation is strictly error/info
  (error blocks New, info never does). Hosts must drop the `Strict` field
  from `Blueprint` literals.
- **`IssueLevel.LevelWarn` removed** — validation has only `LevelError`
  and `LevelInfo`; hosts matching `LevelWarn` must update.
- **`Replace` rejects nil/incomplete ports** — a swapped port must be a
  non-nil, complete organ (ContextBudget needs Trimmer + MaxTokens, Hooks
  needs all eight callbacks); a missing organ cannot be swapped in.
- **`ContextBudget` completeness enforced** — a budget with a nil Trimmer
  or MaxTokens <= 0 fails assembly (a budget that does not trim is not a
  budget).

### Added

- **Learning rules re-exported at the facade** — `Hebbian`/`STDP`/
  `Prune`/`HebbianFire`/`STDPParams` are now callable from the api package
  (hosts can build plasticity loops without importing internals).
- `BuildComposite` initializes empty slices (JSON renders `[]` instead of
  `null` for agents/synapses).

## [1.1.2] - 2026-08-24

### Added

- **Reference learning rules** (`internal/synapse/learning.go`) — hosts
  build their plasticity loops on these or adapt them: `Hebbian(ctx, s,
  from, to, rate)` (fire-together-wire-together step), `STDP(ctx, s, from,
  to, dt, STDPParams{APlus, AMinus, Tau})` (spike-timing window: dt > 0
  LTP, dt < 0 LTD, exponential decay), `Prune(ctx, s, weightFloor,
  minFired)` (removes young-and-weak edges, returns the count),
  `HebbianFire(ctx, s, sig, rate)` (end-to-end fire-then-learn pattern;
  failed delivery does not learn). The framework still never decides when
  to learn — these are reference implementations the host may call.
- **Unified composite view** (`api/composite.go`) — `BuildComposite(ctx,
  o, syn)` merges the static assembly subgraph (internal nodes + slot
  edges) with the dynamic synapse graph (external agents + synaptic edges)
  into one `CompositeGraph`; `RenderComposite` renders it as ASCII (weak
  synapses flagged for pruning review) and `RenderCompositeJSON` as
  indented JSON. `syn == nil` renders the internal subgraph only. View is
  unified, data stays separate.
- **Facade completions** — `NewDirect(r Resolver, initial ...Edge)` and
  `Resolver` are now re-exported at the api facade: hosts can build the
  reference synapse and restore persisted graphs without importing
  internals.

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
- `internal/synapse/agent.md`, `api/agent.md`, `host-integration.md` /
  `host-integration.en.md` — Synapse contract synced (plastic graph,
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

- `protocols.md` — new protocol mapping guide: how hosts map meowire
  ports/events/contracts onto 2026 industry standards (MCP, A2A, AGENTS.md,
  Authority, long-running task state externalization), how meowire differs
  from DeepSeek Harness (Cordis); dynamic wiring, Agent Card and task states
  now have first-class API counterparts.
- `host-integration.md` / `host-integration.en.md` — §1 flow table,
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

- `host-integration.md` / `host-integration.en.md` — Hooks struct,
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
- `host-integration.md` / `host-integration.en.md` — host-side
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
