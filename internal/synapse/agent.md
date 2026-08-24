# MeowAgent synapse module context (capability layer: synapse)

> Package lives at internal/synapse — sealed from external import; hosts use only the api package API.

## Purpose

- Capability layer: plastic synapse graph — inter-agent connections (Link/Unlink/Reinforce) and signal delivery (Fire), plus Direct implementation
- Depends on nerve; does not depend on cell (uses Resolver closure to resolve targets, avoiding cell import)
- Signal types are host-level: the framework does not consume them — hosts route/consume via their own inboxes
- Learning rules (Hebbian/STDP) are host-side: the framework stores state (weights, delivery counts), never decides when to change it

## Dependencies

- nerve (Signal type)
- Go standard library only (context/fmt/sync)

## Interface Contract

- `Edge{From, To string, Weight float64, Fired int64}`: one synaptic edge (strength + cumulative deliveries)
- `Synapse` interface (five methods, plastic graph since 1.1.1):
  - `Link(ctx, from, to, weight) error` — synaptogenesis; idempotent overwrite; negative weight clamps to 0
  - `Unlink(ctx, from, to) error` — synapse elimination; missing connection → `ErrNotLinked`; empty rows dropped
  - `Reinforce(ctx, from, to, delta) error` — LTP/LTD; result floor 0; missing → `ErrNotLinked`
  - `Fire(ctx, sig) error` — connection check (ErrNotLinked) → Resolver lookup (ErrNoTarget) → select three-way delivery `{inbox<-sig; <-ctx.Done(); default}` (full buffer → ErrTargetBusy); success increments the edge's Fired
  - `Edges(ctx, from) ([]Edge, error)` — deep-copied snapshot; from="" = whole graph (persistence export); unknown from = empty, not an error
- `Direct` implementation:
  - Edge table `map[string]map[string]Edge` (from→to→Edge, RWMutex protected)
  - `Resolver func(id) (chan<- nerve.Signal, bool)`: resolves target inbox by ID (host-injected closure)
  - `NewDirect(r Resolver, initial ...Edge)`: initial restores a persisted graph (omitted = empty); negative weights clamp
  - `SetResolver`/`Connected`: assembly and read-only query
- Error variables: `ErrNoTarget`, `ErrNotLinked`, `ErrTargetBusy`
- Reference learning rules (1.1.2, host-callable; framework never auto-applies):
  - `Hebbian(ctx, s, from, to, rate) error` — fire-together-wire-together step (Reinforce +rate)
  - `STDP(ctx, s, from, to, dt, STDPParams{APlus, AMinus, Tau}) error` — spike-timing window: dt>0 LTP (Δw=APlus·exp(−dt/τ)), dt<0 LTD (−AMinus·exp(dt/τ)), dt=0 no change; Tau≤0 or negative magnitudes error
  - `Prune(ctx, s, weightFloor, minFired) (int, error)` — removes edges with weight<floor AND Fired<minFired; returns removed count
  - `HebbianFire(ctx, s, sig, rate) error` — end-to-end pattern: Fire then learn on success; delivery failure returns unchanged (no learning)

## Key Decisions

- Resolver closure decouples synapse from cell (dependency direction: nerve ← synapse)
- Fire enforces connection check: synapse only delivers between linked agents
- Non-blocking delivery: buffer full → ErrTargetBusy immediately (never waits for target consumption)
- Plastic graph since 1.1.1: Link carries initial weight; Unlink/Reinforce/Edges added; single-path policy — no two-interface compatibility shim
- Persistence round-trip: export `Edges(ctx, "")` → host serializes; restore via `NewDirect(resolver, restored...)` — Agent.New stays unaware (host-domain assembly)
- Multi-agent orchestration is host-side: hosts wrap synapse.Fire inside a `send_message` tool; Fire errors become `Effect{Err}` feedback (EventToolResult) so the loop continues — resistance is yield-ed, not fatal
- Asynchronous task pattern: hosts combine `submit_task` (immediate ack) + `query_task` (status poll) tools for long-running sub-agents; the loop stays continuous

## Pitfalls

- Fire is non-blocking: inbox full returns ErrTargetBusy; host tool should retry or report status
- Fired counter bumps only on successful delivery; an edge unlinked concurrently is skipped (counts lost with the connection)
- Edges returns a deep copy: mutating the snapshot never mutates internal state
- Weights clamp at 0 (both Link and Reinforce); negative delta can only approach 0, never go negative
- Target closed (Cell.Close but still in table): Resolver returns false → ErrNoTarget; host Retire path should handle connection cleanup
- Resolver-returned inbox may be closed: host must ensure lifecycle ordering (stop sending before Retire)
- Link ctx is only for cancellation check, does not block
- The event stream is a synchronous pull model (iter.Seq): external events cannot be injected mid-iteration; cross-cycle waiting is done via poll tools
