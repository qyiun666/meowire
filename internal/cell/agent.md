# MeowAgent cell module context (capability layer: agent kernel)

> Package lives at internal/cell — sealed from external import; hosts use only the api package API.

## Purpose

- Capability layer: minimal agent kernel (ID + ports + DecisionLoop → iter.Seq[Event])
- One file: `cell.go` (the kernel, Stimulate/Resume/Replace/Close and the per-invocation port snapshot)
- Depends on nerve; nothing else above the loop

## Dependencies

- nerve (the decision loop and the port interfaces)
- Go standard library only

## Interface Contract

- `Cell`: minimal agent kernel
  - `ID string`: the agent's declared identity — `api.New` requires it to be non-empty, because every event is attributed to it and `Resume` refuses a `Session` produced under another one
  - `Identity string`: identity description text (host composed, injected into Prompt)
  - Required ports (the api assembly enforces all six host ports): `Think nerve.Thinker` (the bundled brain, injected by the composition root — not swappable, no "think" slot), `Act nerve.Effector`, `Hooks`, `Sandbox`, `Budget`, `Mem nerve.Memory`; `snapshot` refuses to run only when Think/Act are missing, so a cell built directly (not through `api.New`) dereferences whatever it was given
  - Framework wiring: `PauseGate func() *nerve.PauseGate` (nil = pause unsupported; factory called per Stimulate for a fresh gate; PauseGate carries IsPaused and Clear — an honored pause yields EventPaused + Session and ends the iterator, resumed via Resume(sess, nerve.Response{}) — nothing is asked, so the Response is not read — and the loop clears the request only when the suspension it resumes was that pause)
  - Config: `MaxRounds`, `MaxToolOutput`, `MaxRetries`, `ToolTimeout`, `ToolMaxRetries`, `ParallelActs`, `MaxParallelActs` (zero values use defaults / disabled; ParallelActs=false = strict serial, and a ceiling set while it is off binds nothing — `Validate` says so); `UpdateConfig(cfg)` swaps wholesale at the next Stimulate/Resume and records a `ConfigAudit{CellID, Old, New}` drained into the next LoopContext and emitted as EventConfig after EventReplace (the config-update counterpart of the Replace audit)
  - Host-injected fixed parts: `System string`, `Methods []nerve.MethodSpec`, `Tools []nerve.ToolSpec`, `Context []string`
  - `Stimulate(ctx, text) iter.Seq[nerve.Event]`: runs DecisionLoop, yields events; snapshots ports under the wire lock, so a concurrent Replace takes effect at the next Stimulate. Every event — including the cell's own guard errors — leaves stamped with `CellID`, `Seq` (1-based, strictly increasing across Stimulate and Resume, restarting with the process) and `TS` (Unix milliseconds), because who spoke, in what order and when are the boundary's facts to guarantee, not each producer's. A refusal of a closed cell carries `nerve.ErrCellClosed` wrapped with the call site — the api re-exports the sentinel, so the host matches it with `errors.Is`
  - `Replace(slot string, port any) (any, error)`: dynamic wiring — a table keyed by the blueprint's `Slot` values swaps think/act/sandbox/budget/mem/hooks at runtime (`slots_sync_test.go` fails if the table and `nerve.SwappableSlots()` drift); returns the previous port; takes effect at the next Stimulate; concurrency-safe (wireMu); a closed cell refuses the swap; unknown slot / wrong port type errors; **rejects nil and incomplete ports** — a Budget must carry Trimmer, TrimResults and MaxTokens > 0, a Hooks must carry all eight callbacks; Closer/PauseGate not swappable. The audit it appends records `OldType`/`NewType` (the ports' Go type names), never the values: the record is emitted later than the swap and must stay serializable
  - `CheckSwap(slot, port) error`: the same assertion without the commit — for a caller that must bring an organ up before it is wired, so a refused slot never costs that organ its one startup
  - `Close() bool`: marks the cell closed and reports whether this call performed the transition (the facade runs the host `Closer` on that answer, so it runs once); `IsClosed() bool` reads the same single flag — the api layer keeps no second copy that could disagree with it mid-call

## Key Decisions

- Cell is the minimal kernel: no routing table, no Divide, no tree management, and no queue of its own — it runs one loop over the ports it was given
- Multi-agent is achieved via flat model: host creates multiple Cell instances (via root New)
- Sub-agent = host tool pattern (spawn_agent tool), not framework-level nesting
- Stimulate returns `iter.Seq[nerve.Event]` — host consumes events directly
- Close is idempotent (a single CAS on the cell's flag; the caller that wins it runs the host `Closer`)
- Memory is a required port (`Mem`, the seventh): the cell carries the host's Recall/Remember organ and the loop owns the two timepoints; the text track stays host-injected via `Context`
- All errors wrapped with `fmt.Errorf("...: %w", err)`

## Pitfalls

- The cell runs no goroutine of its own: no lifecycle to shut down and no pump to order against `Close`. Which instance is reachable, and when it runs, stay host decisions in the flat model
- Host ports must respect ctx (long operations must monitor ctx.Done)
- Stimulate creates a fresh LoopContext per invocation (no state carried between calls)
- Replace is the runtime counterpart of Blueprint assembly: it mutates the cell's port fields under wireMu; in-flight Stimulates keep the ports they snapshotted; it rejects nil/incomplete ports (a missing organ cannot be swapped in)
- Stimulate iterator abort = abandon the round; the tool at the abort point does not execute
- Context and Tools slices are copied on each Stimulate to prevent mutation across calls
