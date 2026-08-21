# Changelog

All notable changes to meowire are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
