# MeowAgent cell module context (capability layer: agent kernel)

> Package lives at internal/cell — sealed from external import; hosts use only the api package API.

## Purpose

- Capability layer: minimal agent kernel (ID + ports + DecisionLoop → iter.Seq[Event])
- Two files: `cell.go` (the kernel, Stimulate/Resume/Replace/Close and the per-invocation port snapshot) and `colony.go` (this cell's half of an agent-to-agent exchange: mint ids, stamp what only it knows, pair replies back to the round that waited)
- Depends on nerve; does not depend on synapse/memory/root

## Dependencies

- nerve (the decision loop, the port interfaces, and the signal/correlation types the cell pairs)
- Go standard library only

## Interface Contract

- `Cell`: minimal agent kernel
  - `ID string`: unique identifier
  - `Identity string`: identity description text (host composed, injected into Prompt)
  - Required ports (the api assembly enforces all seven): `Think nerve.Thinker`, `Act nerve.Effector`, `Hooks`, `Sandbox`, `Budget`, `Mem nerve.Memory`; `snapshot` refuses to run only when Think/Act are missing, so a cell built directly (not through `api.New`) dereferences whatever it was given
  - Framework wiring: `PauseGate func() *nerve.PauseGate` (nil = pause unsupported; factory called per Stimulate for a fresh gate; since v1.3.2 PauseGate carries only IsPaused — an honored pause yields EventPaused + Session and ends the iterator, resumed via Resume(sess, ""))
  - `Egress func(context.Context, nerve.Signal) error`: the host's Colony organ, injected once at assembly (`api.Organs.Colony` → `Fire`), never swapped at runtime; nil = this cell sends nothing, so it can neither delegate a call nor answer a request it received
  - `Inbox() chan<- nerve.Signal`: the send end of this cell's inbound queue, created on first use with capacity `nerve.InboxCapacity` and handed to the routing table (`api.Resolve`). Never closed: a cell that stops consuming applies backpressure (senders get `ErrTargetBusy`) instead of losing the queue under a running sender. The loop reaches the queue only through `ingest()`, which pairs any reply answering a live delegation before handing the rest to the round
  - `Resumptions() []nerve.Correlation` / `Ack(signalID)`: the cell's half of agent-to-agent bookkeeping — `take()` moves the queue out and records each pairing (`delegations` keyed by the minted id → `resumptions` waiting for the host), `Resumptions` snapshots that list without consuming it, `Ack` ends a delegation and drops its resumptions. The cell never resumes anything itself
  - Config: `MaxRounds`, `MaxToolOutput`, `MaxRetries`, `ToolTimeout`, `ToolMaxRetries`, `ParallelActs` (zero values use defaults / disabled; ParallelActs=false = strict serial); `UpdateConfig(cfg)` swaps wholesale at the next Stimulate/Resume and — since v1.3.2 — records a `ConfigAudit{CellID, Old, New}` drained into the next LoopContext and emitted as EventConfig after EventReplace (the config-update counterpart of the Replace audit)
  - Host-injected fixed parts: `System string`, `Methods []nerve.MethodSpec`, `Tools []nerve.ToolSpec`, `Context []string`
  - `Stimulate(ctx, text) iter.Seq[nerve.Event]`: runs DecisionLoop, yields events; snapshots ports under the wire lock, so a concurrent Replace takes effect at the next Stimulate
  - `Replace(slot string, port any) (any, error)`: dynamic wiring — a table keyed by the blueprint's `Slot` values swaps think/act/sandbox/budget/mem/hooks at runtime (`slots_sync_test.go` fails if the table and `nerve.SwappableSlots()` drift); returns the previous port; takes effect at the next Stimulate; concurrency-safe (wireMu); no-op after Close; unknown slot / wrong port type errors; **rejects nil and incomplete ports** — a Budget must carry Trimmer, TrimResults and MaxTokens > 0, a Hooks must carry all eight callbacks; Closer/PauseGate not swappable
  - `Close() error`: idempotent close (marks cell as closed)
  - `IsClosed() bool`: query close status

## Key Decisions

- Cell is the minimal kernel: no routing table, no Divide, no tree management — it owns only its own traffic's bookkeeping (ids, stamping, pairing)
- Multi-agent is achieved via flat model: host creates multiple Cell instances (via root New)
- Sub-agent = host tool pattern (spawn_agent tool), not framework-level nesting
- Stimulate returns `iter.Seq[nerve.Event]` — host consumes events directly
- Close is idempotent (mutex-protected boolean flag)
- Memory is a required port (`Mem`, the seventh): the cell carries the host's Recall/Remember organ and the loop owns the two timepoints; the text track stays host-injected via `Context`
- Colony bookkeeping is drained lazily (v1.3.8): `take()` runs at the two moments someone looks — the loop's Think gap (`ingest`) and a host reading `Resumptions()` — not in a background goroutine. The cell stays goroutine-free (no lifecycle to shut down, no pump to order against `Close`), and a full `inbound` buffer makes `take` stop, leaving signals in the channel so senders keep hitting `ErrTargetBusy` instead of the cell absorbing colony traffic in memory
- All errors wrapped with `fmt.Errorf("...: %w", err)`

## Pitfalls

- Cell owns its own queue and its own ids, and nothing else on the wire: no routing table and no pump — which cell is reachable, and when a cell runs, stay host decisions in the flat model
- `Egress` runs inline on the loop's goroutine — once for a delegation and once per answer at the invocation terminal (on a context detached from cancellation, so the notice still goes out) — so a Colony organ that blocks stalls the cycle; it is injected at assembly and is not a `Replace` slot
- Host ports must respect ctx (long operations must monitor ctx.Done)
- Stimulate creates a fresh LoopContext per invocation (no state carried between calls)
- Replace is the plasticity counterpart of Blueprint assembly: it mutates the cell's port fields under wireMu; in-flight Stimulates keep the ports they snapshotted; since 1.2.0 it rejects nil/incomplete ports (a missing organ cannot be swapped in)
- Stimulate iterator abort = abandon the round; the tool at the abort point does not execute
- Context and Tools slices are copied on each Stimulate to prevent mutation across calls
