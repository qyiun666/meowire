# Changelog

All notable changes to meowire are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.2] - 2026-08-22

### Breaking changes

- **`Hooks.AfterAct` now receives the raw tool error** — signature changed from
  `func(ctx, a *Action, e *Effect)` to
  `func(ctx, a *Action, e *Effect, err error)`; `err` non-nil means the
  effector failed (this also covers `Effect.Err` / nil-effect cases with the
  original error). Migration: add the trailing `err error` parameter to any
  host `AfterAct` callback.

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

### Docs

- `docs/host-integration.md` / `docs/host-integration.en.md` — Hooks struct,
  trigger table, and multi-agent sample updated to the new signatures;
  `BeforeStimulate` / `AfterStimulate` usage patterns documented.
- README (en/zh-CN) and `internal/nerve/agent.md` — Hooks port list and
  module contract synced.

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
