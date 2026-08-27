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

- `Organs{ID, Think, Act, Closer, Hooks, Sandbox, Budget, System, Identity, Methods, Tools, Context}`: host port container — **all ports required, every hook callback required** (H1–H8: explicit no-op, not absence), a missing port or callback returns an error (no stubs, no default implementations)
- `Blueprint{Organs, Config}`: host assembly blueprint instance; define once, `New` many times. `New(b Blueprint) (*Agent, error)` — error-level findings (missing ports, incomplete ports) always block; info findings never block. No Strict flag: every wiring point is required, so there are no warn-level findings to promote
- `FullHooks(h Hooks) *Hooks`: assembly helper — returns a copy with every nil callback filled by an explicit no-op; hosts declare only the hooks they need
- `Config{MaxRounds, MaxToolOutput, MaxRetries, ToolTimeout, ToolMaxRetries, ParallelActs}` (alias of `nerve.LoopConfig`): zero values fall back to defaults (8 rounds, no truncation, no retry, no per-tool timeout/retry, strict serial tools); ParallelActs (v1.3.3, opt-in) executes a round's multi-tool batch concurrently — serial gating → parallel Act → serial feedback in call order; prerequisite: concurrency-safe Effector (Validate surfaces an info finding when enabled); `UpdateConfig(cfg)` swaps wholesale at runtime (next Stimulate/Resume takes effect, in-flight loop keeps its snapshot), `GetConfig()` reads current values (read-modify-write; a zero field resets to default)
- `ID` empty defaults to "agent" (flat model: host gives each instance a unique ID for synapse routing/logs)
- Facade methods are thin delegates: `Stimulate` → Cell.Stimulate (returns `iter.Seq[Event]`); Close → Cell.Close + host Closer; Close is idempotent
- `Pause()` / `Unpause()`: Agent-level pause control — idempotent, concurrency-safe, no-op after Close; takes effect at the next gap point (before each Think / tool execution); since v1.3.2 a pause yields EventState(StatePaused) + EventPaused (Session snapshot) and ends the iterator normally — resume via `Resume(sess, "")` (unified suspension-resume); `Unpause()` only clears a pause request that has not taken effect yet; `Resume` clears a stale pause request automatically
- `Resume(ctx, sess Session, response string) iter.Seq[Event]`: continues a suspended loop from the Session captured in EventWaitInput (tool suspension) or EventPaused (pause suspension) — for a tool suspension the response is injected as the pending tool's structured result (ToolResults entry with the pending call's ID); for a pause suspension the response must be empty (pending.ID == "": nothing is injected, the loop just continues); for a sandbox ask suspension (`sess.sandboxAsk`, wire v2) the response follows the deny/approve grammar — "" denies as `[denied: declined]`, a `[denied:` prefix echoes its own text, anything else approves the pending call without re-gating; remaining tools of the suspended round run first, then the round loop resumes from the suspended round (no extra round, no budget during the wait); stream/hooks/guarantees isomorphic with Stimulate; zero-value Session rejected with an error event; after Close yields ErrCellClosed; persistence round-trip via `Session.Marshal()` / `meowire.UnmarshalSession(data)` (versioned JSON, cross-process recovery)
- `Replace(slot, port) (any, error)`: dynamic wiring — runtime port swap (SlotThink/SlotAct/SlotSandbox/SlotBudget/SlotHooks); takes effect at the next Stimulate (in-flight Stimulate keeps its ports); returns the previous port; concurrency-safe; no-op after Close; **rejects nil/incomplete ports** (Budget needs Trimmer+MaxTokens, Hooks needs all eight callbacks); Closer/PauseGate never swappable; every successful swap records a `ReplaceAudit{CellID, Slot, Old, New}` emitted as EventReplace at the start of the next Stimulate/Resume (the moment the swap takes effect), before any other event
- `AgentCard(o Organs) ([]byte, error)`: A2A-style capability card (JSON name/description/skills) projected from ID/Identity/Methods; publish at /.well-known/agent-card.json for agent discovery
- After Close, Stimulate returns ErrCellClosed
- Errors: ErrCellClosed defined here; synapse errors (ErrNoTarget/ErrNotLinked/ErrTargetBusy) re-exported via errors.go

## Wiring Blueprint (graph-based assembly inspection)

- The blueprint is a **graph**: `ConnectomeNodes()` are the data-object nodes (prompt/context/toolresults/plan/bounds/decision/action/effect/err/output/timing/resources/hooks); `Connectome()` is the slot edge list (ports P1–P6, hooks H1–H8, built-ins F1/G1). Each edge carries ID/Name/Phase(1 framework·2 host port·3 hook)/Category(sense-decide-act)/TargetID(graph node it reads or mutates)/Semantics(replace-append-trim-gate-read-only-act-container)/Parallel/Required/Desc
- `WiringDiagram(o Organs) []Slot`: extracts actual assembly state (filled/unwired) from Organs — no host registration needed; built-ins always filled
- `BuildGraph(o Organs) WiringGraph`: assembled graph — canonical nodes + slot edges with filled state
- `SlotsByTarget(o Organs, targetID) []Slot`: **find by function, not by port** — who touches a data object? e.g. Context → P6 (trim), H3 (replace); ToolResults → F1 (append)
- `Validate(o Organs, cfg Config) []Issue`: blueprint × assembly comparison. Levels: error (required slot missing or incomplete — ContextBudget without Trimmer/MaxTokens), info (empty Identity/Tools/Context, MaxRounds<=0 default). No warn level: a missing wiring point is a missing organ — an error
- `RenderDiagram(o Organs) string`: ASCII wiring graph (nodes section, then one edge per line with [x]/[ ] mark and -> target node)
- `RenderJSON(o Organs) ([]byte, error)`: machine-readable counterpart — marshals the assembled graph (nodes + slots with filled state) as indented JSON
- `BuildComposite(ctx, o Organs, syn Synapse) (CompositeGraph, error)`: unified graph view — internal subgraph (nodes+slots) merged with the live synapse graph (external agents + sorted synaptic edges); syn==nil renders internal-only
- `RenderComposite(ctx, o Organs, syn Synapse) (string, error)`: ASCII render of the composite (weak synapses w<0.3 flagged `! weak` for pruning review)
- `RenderCompositeJSON(ctx, o Organs, syn Synapse) ([]byte, error)`: indented JSON counterpart (colony-wide observability snapshot)
- Result types: `Slot{Wire WirePoint, Filled bool}` (blueprint entry + filled state), `Issue{ID, Level, Wire, Msg string}` (Validate finding; `Level` ∈ error/info), `WiringGraph{Nodes []WireNode, Slots []Slot}` (assembled graph), `IssueLevel` constants `LevelError` / `LevelInfo`
- `New` rejects error-level findings always (missing/incomplete ports, joined via errors.Join); info findings never block; hosts surface them by calling Validate at assembly/test time

## Key Decisions

- Flat model: one Agent = one kernel; host manages multiple instances for multi-agent
- Sub-agent = host tool pattern (spawn_agent tool), not framework-level nesting
- Multi-agent messaging = host tool pattern (send_message tool over synapse.Fire); resistance yields as EventToolResult feedback, loop continues
- Synapse construction at the facade: `NewDirect(r Resolver, initial ...Edge)` re-exported (initial = persisted graph restore); reference learning rules live in internal/synapse (host-side, framework never auto-applies)
- Synapse is a plastic graph since 1.1.1: `Link(ctx, from, to, weight)` / `Unlink` / `Reinforce(ctx, from, to, delta)` / `Fire` / `Edges(ctx, from)`; `Edge{From, To, Weight, Fired}` re-exported; persistence loop: `Edges(ctx, "")` export → host stores → `NewDirect(resolver, restored...)` restore (host-domain assembly, Agent.New unaware)
- Signal carries `Status TaskStatus` (A2A-style: submitted/working/needs-input/completed/failed/cancelled) for end-to-end task lifecycle tracking
- History managed by host (MemHop): host controls context accumulation via Organs.Context
- Structured tool feedback (v1.3.1): Prompt.ToolResults ([]ToolResult{ID, Name, Result, Err}) is the single tool-feedback track — tool results no longer enter Context (text track keeps host-injected base + sandbox denials); ID is the LLM-provided call id (call_xxx), Result/Err carry the truncated raw output (Err non-empty = failed); Resume preserves the accumulated track and appends the response as the pending tool's entry; EventToolResult echoes the call (Event.ToolCall). Rendering (tool-role messages, [tool_call_id=xxx] markers, plain text) is the host Thinker's decision — hosts must consume ToolResults to surface tool feedback to the model
- Host-driven multi-round resume (Step-Resume) = stop iterator + self-managed history + re-Stimulate; each Stimulate is one stateless step
- Streaming: host Thinker consumes LLM stream chunks and pushes them to its own channel (e.g. WebSocket); the event stream is an observation mirror of loop execution — EventText stays whole-segment, token deltas never enter it
- ask_user (v1.3.0): the tool returns `&Effect{WaitInput: question}` — the loop yields StateWaiting + EventWaitInput (with an opaque Session) and ends the iterator normally; the host saves the Session, shows the question, and calls `Resume(ctx, sess, response)` once the input arrives (timeout host-controlled, default deny `[denied: timeout]`). Never block inside Act — blocking drags the Close wait
- Sandbox ask (tri-state rulings): `Sandbox.Allow` returning `VerdictAsk` suspends through the identical StateWaiting + EventWaitInput pair (Question comes from the ruling, not a tool effect); the resolve uses the same Resume channel with the deny/approve response grammar ("", `[denied:` prefix, else approve — approved calls execute without re-gating); the audit chain closes with a terminal second EventSandbox record
- Dynamic memory injection: Stimulate has no per-round context channel; per-round retrieval happens in Hooks.BeforeThink; host controls once-vs-every-round semantics with its own closure flag
- Guards/emotion/plan: ContextBudget trims context before each Think (trimmer, not hard stop); MaxRetries retries Think only — tool-failure guards are host-side (Effector or AfterAct); ToolTimeout/ToolMaxRetries bound tool execution (per-attempt timeout; retry on effector err only, Effect.Err never retried); identity/emotion is host-composed into the `Identity` string and rendered by the host Thinker; Plan is a host-serialized string injected via BeforeThink and updated via AfterThink
- Sandbox: host membrane (tool security gate: allowlist/denylist, ask-confirm) maps to the tri-state `Sandbox.Allow` returning `(Verdict, reason string, err error)` — Deny (zero value, fail-closed) appends `[denied: ...]` feedback, Allow proceeds, Ask suspends for external confirmation (see above), err coerces to Deny `[sandbox error: ...]`; every decision of a configured sandbox first yields EventSandbox carrying a `SandboxVerdict{CellID, Call, Ruling Verdict, Reason, Question, Err}` (action-level audit record; ask chains close with a terminal resolve record); denial then yields EventToolResult(Err) and the loop continues; Sandbox.Bounds() is snapshotted once per Stimulate into Prompt.Bounds (execution boundary description surfaced to the LLM)
- Assembly only at composition root: components inside must not create dependencies
- No stubs/sentinels: all six ports must be injected; there is no "minimal runnable" path

## Pitfalls

- Host ports (Thinker/Effector) must be concurrency-safe if same Agent is stimulated concurrently
- Pause is gap-effective: it never interrupts an in-flight Think/Act — interrupt a running tool with ctx cancellation instead; since v1.3.2 the honored pause ends the iterator (EventPaused + Session), so a pause inside a tool list keeps the current tool in Session.remaining and Resume runs it first
- Session (from EventWaitInput/EventPaused) is an opaque single-use handle: never inspect/mutate it; resuming twice re-executes the remaining tool calls (duplicate side effects — host responsibility); persist across restarts via `Session.Marshal()` / `UnmarshalSession` (versioned; timeout-deny remains host-controlled)
- Suspension path fires OnCycleEnd/AfterStimulate exactly once with empty output — distinguish via StateWaiting/EventWaitInput and do not persist an unfinished round; the outcome parameter reports OutcomeSuspended (OnCycleEnd signature `(ctx, output, outcome CycleOutcome)` since the outcome-classification change: Done/Suspended/MaxRounds/Error/Aborted, zero reserved for consumer-abandoned iterators → Aborted)
- Stopping the iterator mid-cycle abandons the round; tools at or after the stop point do not execute
- Host ports must respect ctx (long operations must monitor ctx.Done)
- Hooks.BeforeThink must replace p.Context as a whole slice — it shares the backing array with lc.Context; appending into it can corrupt the loop's accumulated context
- Streaming UX lives in the Thinker (host side); the event stream never carries token deltas (EventText is whole-segment by contract)
- Synapse errors (ErrNoTarget/ErrNotLinked/ErrTargetBusy) re-exported at api level
- Facade adds no business logic (delegation only); extend semantics in capability packages or host
