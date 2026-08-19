# MeowAgent synapse module context (capability layer: synapse)

> Package lives at internal/synapse — sealed from external import; hosts use only the api package API.

## Purpose

- Capability layer: inter-agent connections (Link) and signal delivery (Fire) contract, plus Direct implementation
- Depends on nerve; does not depend on cell (uses Resolver closure to resolve targets, avoiding cell import)
- Signal types are host-level: the framework does not consume them — hosts route/consume via their own inboxes

## Dependencies

- nerve (Signal type)
- Go standard library only (context/fmt/sync)

## Interface Contract

- `Synapse` interface: `Link(ctx, from, to) error` + `Fire(ctx, sig) error` — Link/Fire only, no Forget
- `Direct` implementation:
  - Connection table `map[string]map[string]struct{}` (from→to set, RWMutex protected)
  - `Resolver func(id) (chan<- nerve.Signal, bool)`: resolves target inbox by ID (host-injected closure)
  - Fire: connection check (ErrNotLinked if missing) → Resolver lookup (ErrNoTarget if not found) → select three-way delivery `{inbox<-sig; <-ctx.Done(); default}` (buffer full → ErrTargetBusy)
  - `SetResolver`/`Connected`: assembly and read-only query
- Error variables: `ErrNoTarget`, `ErrNotLinked`, `ErrTargetBusy`

## Key Decisions

- Resolver closure decouples synapse from cell (dependency direction: nerve ← synapse)
- Fire enforces connection check: synapse only delivers between linked agents
- Non-blocking delivery: buffer full → ErrTargetBusy immediately (never waits for target consumption)
- Simplified interface: Link/Fire only; connection cleanup is host responsibility
- Multi-agent orchestration is host-side: hosts wrap synapse.Fire inside a `send_message` tool; Fire errors become `Effect{Err}` feedback (EventToolResult) so the loop continues — resistance is yield-ed, not fatal
- Asynchronous task pattern: hosts combine `submit_task` (immediate ack) + `query_task` (status poll) tools for long-running sub-agents; the loop stays continuous

## Pitfalls

- Fire is non-blocking: inbox full returns ErrTargetBusy; host tool should retry or report status
- Target closed (Cell.Close but still in table): Resolver returns false → ErrNoTarget; host Retire path should handle connection cleanup
- Resolver-returned inbox may be closed: host must ensure lifecycle ordering (stop sending before Retire)
- Link ctx is only for cancellation check, does not block
- The event stream is a synchronous pull model (iter.Seq): external events cannot be injected mid-iteration; cross-cycle waiting is done via poll tools
