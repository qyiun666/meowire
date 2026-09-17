# MeowAgent synapse module context (capability layer: synapse)

> Package lives at internal/synapse — sealed from external import; hosts use only the api package API.

## Purpose

- Capability layer: plastic synapse graph — inter-agent connections (Link/Unlink/Reinforce) and signal delivery (Fire), plus Direct implementation
- Depends on nerve; does not depend on cell (uses Resolver closure to resolve targets, avoiding cell import)
- The graph is content-agnostic (it never reads a `Payload` and has no opinion on what a signal means), but a signal's routing fields are framework-owned end to end since v1.3.8: `api.Resolve` hands it cell queues instead of host channels, and the cell pairs replies against its own delegations
- Learning rules (Hebbian/STDP) are host-side: the framework stores state (weights, delivery counts), never decides when to change it

## Dependencies

- nerve (Signal type)
- Go standard library only (zero third-party dependencies)

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
  - `Resolver func(id) (chan<- nerve.Signal, bool)`: resolves target inbox by ID (host-injected closure; `api.Resolve(agents...)` builds one from the agent list, so the host names members instead of owning channels)
  - `NewDirect(r Resolver, initial ...Edge)`: initial restores a persisted graph (omitted = empty); negative weights clamp
  - `SetResolver`: late resolver injection at assembly time. It exists for the colony's circular order — the routing table is built from the agents, and each agent's Colony organ is the synapse itself, so neither can be constructed first; `api.NewDirect` deliberately returns `*Direct` (not the interface) to keep this step reachable without adding it to the `Synapse` contract. Connectivity is read through the `Edges` snapshot
- Error variables: `ErrNoTarget`, `ErrNotLinked`, `ErrTargetBusy`
- Reference learning rules (host-callable; the framework never auto-applies):
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
- Two sanctioned routes through Fire (v1.3.8): the framework's own — `Organs.Colony` is this graph's `Fire`, driven by the kernel for a delegation (`Effect.Send`) and for the answers it owes, which is what gets ids minted and replies paired — and the host's — a `send_message` tool firing signals of its own convention, which the cell cannot pair (it never minted those ids) and therefore surfaces as ordinary inbound traffic
- Delivery resistance stays feedback: a `Fire` error reaches `Effect{Err}` (EventToolResult) on the host route and a tool feedback on the delegation route, so the loop continues rather than dying — a busy or unknown peer is information, not a failure

## Pitfalls

- Fire is non-blocking: inbox full returns ErrTargetBusy; host tool should retry or report status
- Fired counter bumps only on successful delivery; an edge unlinked concurrently is skipped (counts lost with the connection)
- Edges returns a deep copy: mutating the snapshot never mutates internal state
- Weights clamp at 0 (both Link and Reinforce); negative delta can only approach 0, never go negative
- Closed target, still in the table: `api.Resolve` snapshots membership and the cell's inbox is never closed, so a dead cell's ID keeps resolving and signals queue to capacity, then ErrTargetBusy — retiring a member means resolving again without it, not closing a channel
- Link ctx is only for cancellation check, does not block
- Delivering is not waking: `Fire` fills a queue, and the target reads it only at its own next Think gap (or when its host reads `Resumptions`) — nothing in the framework Stimulates a cell on a signal's behalf, so a peer nobody runs never picks up its share of the colony
- The event stream is a synchronous pull model (iter.Seq): external events cannot be injected mid-iteration; waiting on a peer is `Effect.Send` (the round suspends on its own Session), waiting on a human is `Effect.WaitInput`
