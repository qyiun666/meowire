# meowire Host Integration Guide

> Audience: host developers/AIs integrating meowire.
> This document is the authoritative integration contract — all interface
> signatures, field semantics, and event sequences match the source code.
> Module path: `github.com/qyiun666/meowire`. Public package:
> `github.com/qyiun666/meowire/api` (aliased `meowire`).

## 0. The One-Line Model

meowire is a **pure orchestration kernel**: the host implements seven ports
(LLM, tools, cleanup, hooks, permission gate, context trimming, experience memory), and the
framework runs the Think → Act → yield event loop. **The framework does not
manage history, does not manage multi-agent, and provides no default
implementations** — all seven ports are required; a missing one is an error.

The host touches exactly three methods: `New` (assembly), `Stimulate` (run one
step), `Close` (shutdown).

## 1. Integration Flow Overview (6 Steps)

```
Implement seven ports → Assemble Organs → Set Config → Assemble Blueprint → New() → Stimulate() consume event stream → Close()
```

| Step | What | Key point |
|------|------|-----------|
| 1 | Implement Thinker/Effector/Closer/Hooks/Sandbox/ContextBudget/Memory | All seven ports required |
| 2 | Assemble the `Organs` struct | Inject ports + fixed context |
| 3 | Set `Config` | Zero values are defaults; nothing must be set explicitly |
| 4 | Assemble `Blueprint{Organs, Config}` and `New(bp)` | Missing port/callback returns an error; incomplete Budget too |
| 5 | `for ev := range agent.Stimulate(ctx, text)` | Consume the event stream |
| 6 | `agent.Close()` | Idempotent; Stimulate after Close returns `ErrCellClosed` |

---

## 2. Step 1: Implement the Seven Ports (Host Capabilities)

### 2.1 Thinker — the LLM wrapper (the brain)

```go
type Thinker interface {
    Think(ctx context.Context, p *Prompt) (*Decision, error)
}
```

**Input `Prompt` (assembled by the framework; the Thinker only reads) fields:**

| Field | Content | Injected by |
|-------|---------|-------------|
| `System` | System instructions ("You are a support agent…") | Host, fixed at construction |
| `Identity` | Identity description text (host composed, e.g. "You are meow, role assistant, warm tone") | Host, fixed at construction |
| `Methods` | Built-in capability description (gene projection, describes only; `MethodSpec{Name, Desc, Input, Output}`) | Host, fixed at construction |
| `Tools` | Available tool list (function schemas) | Host, fixed at construction |
| `Context` | Context slice: host base + framework-appended sandbox denials (`[sandbox-denied: ...]`, emitted in final form); tool results no longer enter the text track | Host base + framework appends |
| `ToolResults` | Structured tool results (`ToolResult{ID, Name, Result, Err}`): accumulated within the cycle, `ID` is the LLM-provided call id (`call_xxx`), `Result`/`Err` carry the truncated raw output; rendering (tool-role messages, `[tool_call_id=xxx]` markers, plain text) is the host Thinker's decision | Framework, appended within the cycle |
| `Bounds` | Execution boundary description (`Sandbox.Bounds()` snapshot, e.g. "only /workspace") | Framework, once per Stimulate |
| `Reflection` | Self-review note from the previous round (Reflexion slot, passed through verbatim) | Host (write-back in `BeforeStimulate`) |
| `Memories` | This round's recalled experience records (`Memory.Recall` output; replaced wholesale per round, never accumulated, never enters a Session snapshot) | Framework, before each Think |
| `Stimuli` | Signals neighbours delivered to this agent (`Signal{From, Kind, ReplyTo, Skill, Payload}`); replaced wholesale per round, never accumulated, never enters a Session snapshot | Framework, draining the inbox at the Think gap before each Think |
| `Inhibit` | Tool names withdrawn this round by a `KindNotice`: a matching call is refused before it ever reaches the gate; an in-flight Act is not preempted | Framework, alongside `Stimuli` |
| `Input` | Current stimulus text (the Stimulate argument) | Framework, per round |
| `Plan` | Task plan/progress text | Host, updatable via Hooks |

**Return `Decision` fields:**

| Field | Content | Constraint |
|-------|---------|------------|
| `Text` | LLM text output (whole segment) | Surfaced as EventText by the framework; **token deltas never enter the event stream** |
| `ToolCalls` | Tool calls this round `{ID, Name, Args}` | `Args` is a JSON string; **empty = end of cycle** |
| `Usage` | Token usage `{Prompt, Completion, Total}` | **nil = skip the EventUsage accounting event** |

**Host responsibilities:**
- Render the Prompt into messages, call the LLM API, parse tool calls
- Consume streaming output inside the Thinker (e.g. push to a WebSocket channel);
  the event stream only carries whole-segment Text
- **Must respect `ctx.Done`** (long requests may be cancelled by the framework)
- Must be concurrency-safe if the same Agent is stimulated concurrently

### 2.2 Effector — the tool executor (the hands)

```go
type Effector interface {
    Act(ctx context.Context, a Action) (*Effect, error)
}
```

**Fields:**

| Field | Content |
|-------|---------|
| `Action.CellID` | Agent ID that triggered the tool (= Organs.ID) |
| `Action.Call` | Tool call `{ID, Name, Args}`, `Args` is a JSON string |
| `Effect.Result` | Success result text (truncated by the framework into `ToolResults.Result`; rendering is the host's call) |
| `Effect.Err` | Tool-side error text (truncated into `ToolResults.Err`; non-empty `Err` = the call failed) |
| `Effect.WaitInput` | Non-empty = request external input (the field carries the question text); the framework yields `EventWaitInput` (carrying a Session) and **ends the iterator normally**; the host collects the response and calls `agent.Resume(sess, response)` (see §6.4) |
| `Effect.Send` | Delegate to a peer: the host names `To` (optionally `Skill`/`Payload`), the framework mints `ID`/`From`/`Kind`/`Status=submitted`, delivers through `Organs.Colony` and **suspends that call exactly like `WaitInput` does**; when the answer arrives the framework pairs it back to that suspension — the host takes the handle from `agent.Resumptions()`, continues with `Resume`, then `Ack`s it (see §7.2). An undeliverable request (no Colony, busy or unknown target) never suspends: its error flows back as that call's tool feedback |
| return error | Execution-layer error (also written to `ToolResults.Err`; the loop continues) |

**Host responsibilities:**
- Tool registry + dispatcher: route by `Name`, deserialize `Args`, serialize results
- Multi-agent tools live here: `spawn_agent` (New → Stimulate → return result);
  cross-agent delegation **needs no synapse inside the tool** — return
  `Effect{Send: ...}` and the framework delivers and pairs (see §7.2); only
  side-channel messages call `Synapse.Fire` directly
- `ask_user`-style tools: **return `&Effect{WaitInput: question}` instead of blocking synchronously**
  (blocking drags the Close wait; the suspension is expressed by the framework so the
  UI can show "the cat is waiting for an answer"), see §6.4
- Tool failures can be returned as `Effect{Err: ...}` instead of an error; both
  flow back as structured `ToolResults` entries and the loop continues
  (**resistance is feedback, not failure**)

### 2.3 Closer — the cleaner

```go
type Closer interface {
    Close() error
}
```

Releases host resources: LLM clients, HTTP connections, etc. The framework
guarantees `Agent.Close` is idempotent (CAS); repeated calls have no side effects.

### 2.4 Pause / Unpause — snapshot suspension (unified with ask_user into a single suspend-resume mechanism)

```go
agent.Pause()     // request a pause: takes effect at the next gap point (before a Think / before a tool)
agent.Unpause()   // clear a pause request that has not taken effect yet (back out after Pause)
```

- **Gap-effective**: a pause request never interrupts an in-flight Think/Act;
  the loop checks it at two gap points (before each Think, before each tool execution)
- **Snapshot suspension (non-blocking)**: an honored pause yields
  `EventState(StatePaused)` → `EventPaused` (carrying a Session snapshot) and **ends
  the iterator normally**; the host resumes via `agent.Resume(sess, "")` (empty
  response — there is no pending tool to inject into) — the same Session/Resume path
  as ask_user (LangGraph-interrupt-style single suspension primitive)
- **Tool-gap pause**: the pause point sits before the tool runs, so the snapshot records
  the current tool and the calls after it into Session.remaining; Resume runs them
  first, then re-enters Think (the current tool has not run yet — nothing is lost)
- Pause state is **Agent-level and survives across Stimulate calls**: a Stimulate
  started while paused first suspends at its entry gap point (StatePaused + EventPaused)
- `Pause` / `Unpause` are **idempotent and concurrency-safe**; no-ops after `Close`
- **`Resume` clears the pause request automatically**: resuming is the intent to
  continue, so the resumed loop does not suspend again at its first gap point;
  `Unpause` is only for backing out before the pause takes effect
- Difference from Step-Resume: Step-Resume abandons the round and restarts
  statelessly; Pause **keeps the in-cycle state (Session snapshot) and suspends
  in place**, resuming without consuming a round

### 2.5 Hooks — interception callbacks (all eight required; explicit no-op where no behavior is wanted — absence fails assembly)

```go
type Hooks struct {
    BeforeStimulate func(ctx context.Context, p *Prompt) error // start of a Stimulate; may edit all prototype content fields, written back for every round; error aborts the whole Stimulate
    AfterStimulate  func(ctx context.Context, output string)     // end of a Stimulate (exactly once on all paths)
    BeforeThink     func(ctx context.Context, p *Prompt) error
    AfterThink      func(ctx context.Context, d *Decision) error
    BeforeAct       func(ctx context.Context, a *Action) error
    AfterAct        func(ctx context.Context, a *Action, e *Effect, err error) // err non-nil = effector failure
    OnError         func(ctx context.Context, err error)
    OnCycleEnd      func(ctx context.Context, output string, outcome CycleOutcome)
}
```

| Field | Fires | Typical host use |
|-------|-------|------------------|
| `BeforeStimulate` | Start of each Stimulate, before any event | Turn-level memory Recall (one-shot injection: prototype content edits are written back to the loop for all rounds) |
| `AfterStimulate` | End of each Stimulate (exactly once on all paths) | Turn-level memory Save, session settlement |
| `BeforeThink` | Before each Think | Inject retrieved memory, update Plan |
| `AfterThink` | After a successful Think | Serialize the Decision back into Plan/memory |
| `BeforeAct` | Before each tool execution | Approval, rewrite tool arguments |
| `AfterAct` | After tool execution | Tool logging, failure degradation (`err` non-nil = effector failure) |
| `OnError` | On unrecoverable error | Alerting |
| `OnCycleEnd` | **Exactly once per cycle** (normal, error, and early-consumer-stop paths) | Settlement, persist final output; `outcome` classifies how the cycle ended (Done/Suspended/MaxRounds/Error/Aborted; zero reserved = iterator abandoned early → Aborted) |

**⚠️ `BeforeThink` must replace `p.Context` as a whole (`p.Context = append(p.Context[:0], newCtx...)` or assign a new slice) — it shares the backing array with the loop's accumulated context; appending into it can corrupt the loop context. The same applies to `p.ToolResults` (shared with the loop's structured track): replace wholesale, never append in place.**

### 2.6 Sandbox — the permission gate (the security red line, both sides of the loop)

```go
type Sandbox interface {
    Allow(ctx context.Context, a Action) (verdict Verdict, reason string, err error)
    Emit(ctx context.Context, u Utterance) (verdict Verdict, reason string, err error)
    Bounds() string // execution boundary description (host defined), snapshotted once per Stimulate
}
```

`Utterance{CellID, Round, Text}` is the text this round's Think generated. Both sides share one
tri-state grammar and one audit channel (`EventSandbox`).

- `Allow`'s tri-state ruling (`Verdict`): `VerdictAllow` proceeds to execution; on `VerdictDeny`
  (the zero value — fail-closed) the framework appends `[sandbox-denied: reason]` feedback to
  Context and **the loop continues** (no halt); `VerdictAsk` **suspends for external
  confirmation** — the reason becomes the question text shown externally, the loop
  suspends through the §6.4 protocol, and execution happens only after the host approves
- `Emit` rules on a round's text **before anyone hears it** (the consumer, the accumulated
  output and the next Think all count as hearing it): `Allow` says it as generated; `Deny`
  replaces it with `[sandbox-denied: reason]`, which also joins Context (the brain reads its own
  veto next round); `Ask` **withholds the draft** and suspends through §6.4 — the draft rides the
  `Session`, and the answer either says it as generated or replaces it with the veto text,
  **without re-running that round's Think**
- An error from either method denies (fail-closed); the feedback lands as
  `[sandbox-denied: sandbox error: ...]` (the audit record keeps the inner
  `sandbox error: ...` reason)
- The audit record tells the sides apart by `Call`: a tool-side ruling names the gated call, an
  utterance-side ruling carries a zero `Call`. Every ruling yields exactly one record, and an ask
  chain closes with its terminal record
- `Bounds()` returns the execution boundary description; the framework
  snapshots it once per Stimulate and surfaces it to the LLM via `Prompt.Bounds`
  (so the brain perceives its limits, e.g. "only files under /workspace")
- The host implements security policy: tool allowlist/denylist, human confirmation
  (returning `VerdictAsk` is all it takes — suspension and resume are the framework's job),
  sensitive-operation interception, egress review (`Emit`)

### 2.7 ContextBudget — the token regulator

```go
type ContextBudget struct {
    MaxTokens   int
    Trimmer     func(ctx []string, max int) []string
    TrimResults func(results []ToolResult, max int) []ToolResult
}
```

- Both trimmers run before each Think, **at the same checkpoint and under the same
  `MaxTokens`**: `Trimmer` for the text track `Context`, `TrimResults` for the
  structured feedback track `ToolResults` — within a round only these two tracks
  accumulate; every other Prompt field is replaced wholesale per round
- It is a trimmer, not a hard stop: over budget trims, no error
- Return the input slice unchanged to skip trimming (the function must still be
  provided: a Budget carrying only `Trimmer` fails assembly)

### 2.8 Memory — the experience port

```go
type Memory interface {
    Recall(ctx context.Context, q MemoryQuery) ([]Record, error) // before every Think
    Remember(ctx context.Context, facts CycleFacts) error        // once at the invocation terminal
}
```

- The framework owns only the **two timepoints**: `Recall` runs after the budget trim and
  before the `BeforeThink` hook; `Remember` runs before `OnCycleEnd`, exactly once on each of
  the four exit arms (done / error / suspension / consumer abort)
- What recall returns lands in `Prompt.Memories` (the structured track, separate from H3's
  text track `p.Context`): **replaced wholesale each round, never accumulated, never carried
  into a `Session` snapshot** — the round that resumes recalls again
- `MemoryQuery` carries only the two things the framework knows: who is asking (`CellID`) and
  what this invocation was asked (`Cue`); the retrieval algorithm, ranking, how many records
  and how long they live all belong to the organ
- `CycleFacts` carries only what the framework builds by construction: `CellID` / `Input` /
  `Output` / `Outcome`; what is worth writing never passes through this port, and
  **deletion and forgetting never do either** (the host does that against its own backend)
- A failing `Recall` ends that Think with an error (the organ is part of the assembly — the
  framework does not decide to think on half a context); a failing `Remember` never rewrites
  the already-decided outcome and reaches the host through `OnError`

### 2.9 Composing organs: stacks, fallbacks, and the optional boot step

Several implementations behind the one port the loop sees — the loop never learns there was a
choice:

```go
o.Sandbox = meowire.GuardStack(workspacePolicy, networkPolicy) // one membrane, two layers
o.Think   = meowire.FallbackThinker(primary, backup)          // one brain, two candidates
o.Act     = meowire.FallbackEffector(localTools, remoteTools)  // one pair of hands
```

- Every combinator **returns the port type itself**, so a composed organ is wired exactly like a
  plain one: no slot, no event, no config field was added, and `Replace` accepts a stack the same
  way it accepts a single organ
- `GuardStack` rules by severity, not by layer order: the first Deny ends the stack (a later, more
  permissive layer gets no vote), the first Ask wins over Allow, and a layer that errors is the
  fail-closed Deny the port contract already defines. `Bounds()` reports the first non-empty
  boundary. An empty stack denies — a membrane with no layers guards nothing
- `FallbackThinker` / `FallbackEffector` return the first member's answer that carries **no
  execution error**; when every member fails, the joined errors surface (the last failure is no
  more the reason than the first). A tool that ran and said no (`Effect.Err`) is a result, not a
  broken organ, so the stack never moves on for it
- `Bootable{Boot(ctx) error}` is the one optional lifecycle point: any port may declare it, and
  `New` calls it once per instance, in the order the blueprint lists its required ports
  (`meowire.PortOrder()`), before an agent exists. `Replace` boots an incoming organ **before**
  committing it, so a dead replacement never takes effect mid-round
- A failing `Boot` aborts assembly, and what the attempt opened is released through the host
  `Closer`: the framework never closes an organ, so `Close` stays the one cleanup channel. An organ
  swapped out is never closed either — an in-flight Stimulate may still hold it, and retiring the
  old implementation stays the host's decision

---

## 3. Step 2: Assemble `Organs` (composition root — the single assembly point)

```go
o := meowire.Organs{
    ID:      "agent-001",                       // unique ID; empty = "agent"
    Think:   myThinker,                         // required
    Act:     myEffector,                        // required
    Closer:  myCloser,                          // required
    Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: ...}), // required: all eight callbacks (helper fills missing ones)
    Sandbox: mySandbox,                         // required
    Budget:  &meowire.ContextBudget{...},       // required
    Mem:     myMemory,                         // required
    Colony:  mySynapse,                         // optional: multi-agent delivery organ (absent = cannot ask or answer peers)

    System:   "You are a meow agent, answer in English", // fixed system instructions
    Identity: "You are meow, role assistant, warm tone",   // identity description text (host composed)
    Methods:  []meowire.MethodSpec{...},          // built-in capability description (describes only)
    Tools:    []meowire.ToolSpec{...},          // tool list
    Context:  []string{"[memory] user prefers concise"}, // resident context base (memory entry)
}
```

**Field details:**

| Field | Type | Content | Required |
|-------|------|---------|----------|
| `ID` | string | Unique agent ID; empty = `"agent"`. Used for synapse routing and logging in the flat multi-agent model | no |
| `Think` | Thinker | LLM wrapper | **yes** |
| `Act` | Effector | Tool execution | **yes** |
| `Closer` | Closer | Resource cleanup | **yes** |
| `Hooks` | *Hooks | Interception callbacks (all eight callbacks required) | **yes** |
| `Sandbox` | Sandbox | Permission gate (`Allow` guards execution, `Emit` guards the round's text egress) | **yes** |
| `Budget` | *ContextBudget | Token regulator (text track + structured-feedback track) | **yes** |
| `Mem` | Memory | Experience port (`Recall` before each Think, `Remember` at the invocation terminal) | **yes** |
| `Colony` | Colony | Multi-agent delivery organ (the `Fire` subset of `Synapse`): peer delegations and answers to inbound requests go out through it; without it the agent can be asked but cannot ask or answer (`Validate` reports info) | no |
| `System` | string | System instructions; feeds Prompt.System | no |
| `Identity` | string | Identity description text (host composed); feeds Prompt.Identity | no |
| `Methods` | []MethodSpec | Built-in capability description (gene projection, describes only); `MethodSpec{Name, Desc, Input, Output}`; feeds Prompt.Methods | no |
| `Tools` | []ToolSpec | Tool list; feeds Prompt.Tools; `ToolSpec{Name, Desc, Input, Output}`, `Input` is a JSON Schema (the Thinker generates ToolCall.Args from it) | no |
| `Context` | []string | Resident context base (history/memory injected here, MemHop); initial Prompt.Context | no |

**Note: `New` validates per callback — any nil field in `Hooks` (H1–H8) fails assembly (explicit no-op, not absence). Hosts can use `meowire.FullHooks(...)` to fill missing callbacks with declared no-ops.**

---

## 4. Step 3: `Config` (zero value is default; all optional)

```go
type Config struct {
    MaxRounds      int
    MaxToolOutput  int
    MaxRetries     int
    ToolTimeout    time.Duration
    ToolMaxRetries int
    ParallelActs   bool
    MaxParallelActs int
}
```

| Field | Semantics | Zero-value default |
|-------|-----------|--------------------|
| `MaxRounds` | Hard round limit; **if the last round still has pending tool calls the loop ends with `ErrMaxRounds`** (those tool results are never re-thought); pair with Step-Resume to prevent infinite loops | 8 |
| `MaxToolOutput` | Tool-feedback truncation length for `ToolResults.Result`/`Err` (UTF-8-safe; overlong output gets `[truncated, N bytes total]`) | no truncation |
| `MaxRetries` | Think retry count (retries Think only; tool-failure protection is host-side, in Effector/AfterAct) | no retry |
| `ToolTimeout` | Per-tool execution timeout (each attempt timed independently; timeout-derived errors are not retried and are written to `ToolResults.Err`) | no timeout |
| `ToolMaxRetries` | Tool retry count on effector errors (**executor err only**; `Effect.Err` is never retried — avoids duplicate side effects) | no retry |
| `ParallelActs` | **Parallel execution of a round's multi-tool batch (opt-in)**: serial gating (per-call events/sandbox verdicts/BeforeAct) → parallel Act (timeout/retry included) → serial feedback in call order (never completion order). A single call always keeps the serial path; **prerequisite: the Effector must be safe for concurrent Act calls** | strict serial |
| `MaxParallelActs` | Ceiling on how many calls of one batch execute at the same time. It only narrows `ParallelActs` — same calls, same call-order feedback — for a host that owes a rate limit or a connection pool somewhere else | whole batch at once |

**Runtime hot update:**

```go
cfg := agent.GetConfig()  // read current values
cfg.MaxRounds = 20        // change only the fields you want
agent.UpdateConfig(cfg)   // wholesale swap: takes effect at the next Stimulate/Resume (an in-flight loop keeps its snapshot)
```

- Zero-value semantics identical to `New`: **wholesale replacement** — unset fields
  fall back to defaults; always read-modify-write (as above)
- Division of labor with `Replace`: `Replace` swaps ports (capability), `UpdateConfig` tunes scalars (parameters)

---

## 5. Step 4: `New` Assembly Validation

```go
type Blueprint struct {
    Organs Organs   // seven ports + fixed context (all required)
    Config Config   // zero values are defaults
}

bp := meowire.Blueprint{Organs: organs, Config: cfg}
agent, err := meowire.New(bp)
```

- **Blueprint is define-once, assemble-many**: the same `bp` can `New` multiple independent Agent instances (flat-model multi-agent)
- `New` validates against the **assembly graph** (blueprint = data-object nodes + slot edges), two levels only:
  - `error` (missing required port / missing hook callback / incomplete Budget): **always blocks**, returns `meow: required port X not injected`
    (X ∈ Think/Act/Closer/Hooks/Sandbox/Budget/H1–H8), multiple findings joined
  - `info` (empty Identity/Tools/Context, default rounds, ParallelActs without a concurrency-safe
    Effector, `Organs.Colony` absent): **never blocks** — inspect via `meowire.Validate(organs, cfg)`
  - No `warn` level: every wiring point is required — a missing point is a missing organ, there is no "half-wired pass"
- `New` runs three steps in order: **validate** the blueprint against the graph, **boot** every
  organ that declared `Bootable` (§2.9), then **construct** the cell. Nothing after a failing boot
  is reached, and the failed attempt is released through the host `Closer`
- After assembly the host calls `Stimulate` / `Resume` / `Pause` / `Unpause` / `Close`, and may
  swap ports at runtime via `Replace` and export the capability card via `AgentCard` (below)
- All seven ports and eight hook callbacks must be implemented by the host — **no stubs, no defaults, no "minimal runnable" path**; use `meowire.FullHooks(...)` to declare unneeded hooks as explicit no-ops

### 5.1 Dynamic wiring: `Replace` (runtime organ swap)

```go
oldThink, err := agent.Replace(meowire.SlotThink, myOtherLLM) // takes effect at the next Stimulate
```

- Swappable slots: `SlotThink` / `SlotAct` / `SlotSandbox` / `SlotBudget` / `SlotMem` / `SlotHooks` (the
  blueprint's `WirePoint.Slot` field is the single source; `Connectome()` / `SwappableSlots()`
  enumerate it and a guard test pins the constants to it);
  `Closer` (resource binding) and `PauseGate` (framework wiring) are never swappable
- Semantics: each `Stimulate` snapshots ports into a fresh LoopContext — an **in-flight
  Stimulate is unaffected**; the swap takes effect at the next Stimulate; the previous
  port is returned (host decides whether to shut the old implementation down — the framework never
  closes an organ it swapped out); an incoming organ that declares `Bootable` is booted once the slot
  has accepted it and before the swap commits, so a failing boot — or a mistyped slot name —
  leaves the wiring exactly as it was
- Concurrency-safe; after `Close` it refuses the swap with `ErrCellClosed` and leaves the incoming port unbooted; **rejects nil and incomplete ports** (a Budget needs
  Trimmer + TrimResults + MaxTokens, a Hooks needs all eight callbacks); unknown slot or wrong port type returns an error
- **Audit event**: every successful Replace records a `ReplaceAudit{CellID, Slot, OldType, NewType}`
  emitted as `EventReplace` at the start of the next Stimulate/Resume (the moment the swap
  takes effect — same level as `EventSandbox`, persistable); failed swaps record nothing;
  zero output without swaps. The host closes its model-switch audit loop from the event
  stream instead of maintaining a hand-rolled state machine

### 5.2 Capability card: `AgentCard` (A2A style)

```go
card, _ := meowire.AgentCard(organs) // JSON: name/description/skills
```

Projected from the assembly (`ID`/`Identity`/`Methods`) as a machine-readable capability
declaration; publish it at `/.well-known/agent-card.json` so other agents can discover
this agent. See [protocols.md](protocols.md) §2.

### 5.3 Composite view: `BuildComposite` (static assembly × live synapses, one picture)

```go
colony := meowire.NewDirect(meowire.DirectConfig{Resolver: resolver}) // host-domain Synapse (plastic synapse graph)
// ...runtime Link/Fire/Reinforce...

text, _ := meowire.RenderComposite(ctx, organs, colony) // ASCII: internal nodes/slots + external agents/synapses
snap, _ := meowire.RenderCompositeJSON(ctx, organs, colony) // JSON: machine-readable snapshot
```

- Internal subgraph: static assembly (data-object nodes + slot edges, blueprint ×
  assembly); external subgraph: live synaptic edges (weight + delivery count + last conduct time)
- Edges below the graph's own conduction floor (`DirectConfig{Floor}`) are flagged
  `! below floor` for `Prune` review; a graph with no floor flags nothing
- View unified, data separate: internal assembly and external connections store
  independently, merged only at render time
- Persist `RenderCompositeJSON` snapshots for a unified observability view of the whole colony

---

## 6. Step 5: `Stimulate` — Consuming the Event Stream

`Stimulate(ctx context.Context, text string) iter.Seq[Event]`; consume with
`for ev := range agent.Stimulate(...)`.

### 6.1 Event sequences

**Single round without tools (5 events):**

```
EventState(thinking) → [EventUsage(optional)] → EventSandbox(utterance ruling) → EventText → EventState(done) → EventDone(accumulated output)
```

**With tool calls (inserted per round; may loop over multiple rounds):**

```
EventState(thinking) → [EventUsage] → EventSandbox(utterance ruling) → EventText → EventState(acting)
  → (EventToolCall → EventSandbox(call ruling) → EventToolResult) × N → back to EventState(thinking) → …
```

**Pause path (takes effect at gap points; snapshot suspension — the iterator ends normally, Resume continues):**

```
… → EventState(paused) → EventPaused(Session snapshot) → iterator ends normally (no Done/Error)
→ host calls Resume(sess, "") → remaining tools run first → back to EventState(thinking) → original sequence continues
```

**Suspension path (tool returns `Effect.WaitInput`; the iterator ends normally; see §6.4):**

```
… → EventState(acting) → EventToolCall → EventSandbox → EventState(waiting)
  → EventWaitInput(tool name + question + Session) → iterator ends normally (no Done/Error)
```

**Utterance-confirmation suspension path (`Sandbox.Emit` returns `VerdictAsk`; the draft was never produced; see §6.4):**

```
EventState(thinking) → [EventUsage] → EventSandbox(ask) → EventState(waiting)
  → EventWaitInput(question + withheld draft inside the Session, zero Call) → iterator ends normally
→ host calls Resume(sess, answer) → EventSandbox(terminal) → EventText(draft as generated, or the veto text)
  → [that round's tools run as usual → back to EventState(thinking)] or [nothing left → EventState(done) → EventDone]
```

**Port-swap audit (at the start of the next Stimulate/Resume, before any other event; multiple in order):**

```
EventReplace(slot/old/new) → normal sequence follows
```

**Error path:**

```
EventState(error) → EventError(Err)
```

**After Close:** Stimulate/Resume yields `EventError(ErrCellClosed)` directly.

### 6.2 `Event` fields (only the fields for the Kind are set; the rest are zero values)

Two fields are not kind-specific: `CellID` names the cell that produced the event (stamped on every
event at the cell boundary, including the error a closed agent raises), and `Dropped` names values
that could not cross the event wire (§6.5) — the loop itself never fills it, so a non-empty
`Dropped` means "this event was restored from a log".

| Kind | Active field | Content |
|------|--------------|---------|
| `EventText` | `Text` | This round's text, **already through the output membrane** (`[sandbox-denied: reason]` when `Emit` denied) |
| `EventToolCall` | `ToolCall *ToolCall` | Tool the LLM decided to call |
| `EventToolResult` | `Effect *Effect`, `ToolCall *ToolCall` | Tool execution result (includes Sandbox denials: `Effect.Err = "[sandbox-denied: reason]"`); `ToolCall` echoes the call for ID association |
| `EventSandbox` | `Verdict *SandboxVerdict` | Membrane ruling audit record (ruling Ruling: allow/deny/**ask**, the gated tool or a zero value for the text side, policy reason, ask question, evaluation error); one record per ruling on either side of the loop, an ask chain closes with its terminal second record |
| `EventState` | `State LoopState` | Loop state (idle/thinking/acting/paused/**waiting**/done/error) |
| `EventDone` | `Output` | Accumulated text output of the whole cycle |
| `EventError` | `Err` | Unrecoverable error (incl. `ErrMaxRounds`, `ErrCellClosed`) |
| `EventUsage` | `Usage *Usage` | Token usage of the last Think |
| `EventWaitInput` | `Wait *WaitInput` | The loop waits on external input: `WaitInput{CellID, Call, Question, Session}` — three causes (a tool's own request: `Call` + question; a pre-execution confirmation: `Call` + question; an utterance confirmation: zero `Call` + question); the host **saves the Session**, shows the question, and resumes via `agent.Resume(sess, response)` |
| `EventPaused` | `Wait *WaitInput` | A pause request took effect: `WaitInput{CellID, Session}` (Call zero value, Question empty) — the host saves the Session and resumes via `agent.Resume(sess, "")` (the same channel every suspension uses) |
| `EventReplace` | `Replace *ReplaceAudit` | Port-swap audit: `ReplaceAudit{CellID, Slot, OldType, NewType}` — the swapped ports named by Go type, not held (the record outlives the swap and must stay serializable; `Replace` returns the previous port to the caller); emitted at the start of the next Stimulate/Resume (the moment the swap takes effect), persistable |
| `EventConfig` | `Config *ConfigAudit` | Config-swap audit: `ConfigAudit{CellID, Old LoopConfig, New LoopConfig}`; emitted at the start of the next Stimulate/Resume after EventReplace, persistable |

`SandboxVerdict{CellID, Call, Ruling Verdict, Reason, Question, Err}`: one record per ruling on
either side of the loop (`Ruling` is tri-state; Deny is the zero value — fail-closed), where `Call`
is the gated tool action and a zero `Call` means the ruling was about this round's text; an ask
ruling produces two records on one chain (ask → resolve — the terminal record
carries the final ruling: deny keeps its text, approval shows allow with an empty Reason).
Persisting the event stream yields
the action-level audit log (who, on whose behalf, when, what, why permitted). See
[protocols.md](protocols.md) §4 Authority.

### 6.3 Two semantics the host must know

1. **Early stop (basis of Step-Resume, host-driven takeover)**: breaking/returning in `for range`
   abandons the round — tools at or after the stop point **do not** execute,
   and all state accumulated in this round is discarded. The host may
   interrupt the stream for human approval/async work (saving its own
   progress), then `Stimulate` again. **Note: the only form for a tool
   requesting external input (ask_user) is the §6.4 `WaitInput` + `Resume`
   protocol — do not simulate it with break + `Stimulate`** (the `Session`
   is opaque; a suspended context cannot be rebuilt by hand).
2. **Stateless step**: each `Stimulate` is one stateless step with no state
   across calls. The host keeps history itself and re-injects it via
   `Organs.Context` (or `Hooks.BeforeThink`).

**Boundary of the two paths, in plain words**: a tool requesting input
(ask_user) must use the §6.4 suspension-resume protocol — the framework
keeps the `Session` and consumes no extra round; the host saves the session,
shows the question, and resumes with `Resume` once the answer arrives. Use
break + `Stimulate` only for host-driven takeover (manual approval, async
work, `ErrMaxRounds` continuation) — the round's state is discarded and you
re-inject your own progress. The two paths cannot replace each other: break
is just the standard Go iterator consumption semantics (tools after the stop
point never run), not a second implementation — simulating ask_user with
break loses the suspended context.

### 6.4 Suspension-resume protocol (tool input and membrane asks)

The framework-level protocol for tools that need external input — or for a
`Sandbox.Allow` / `Sandbox.Emit` returning `VerdictAsk` — **no blocking,
no lost rounds**, so a host never has to block synchronously inside Effector:

```go
// ① Tool side (Effector): declare the suspension, never block
func (e *effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
    if a.Call.Name == "ask_user" {
        return &meowire.Effect{WaitInput: "May I delete this file?"}, nil
    }
    ...
}
```

```go
// ② Host side: save the Session, show the question, resume once the response arrives
//    (consume Resume exactly like Stimulate)
var sess meowire.Session
for ev := range agent.Stimulate(ctx, "tidy the desktop") {
    switch ev.Kind {
    case meowire.EventWaitInput:
        sess = ev.Wait.Session          // save the resume handle
        ui.Show(ev.Wait.Question)       // "the cat is waiting for an answer"
        go func() {                     // timeout is host-controlled (default deny)
            select {
            case ans := <-ui.Answer():
                answerCh <- ans
            case <-time.After(60 * time.Second):
                answerCh <- "[denied: timeout]"
            }
        }()
    }
}
ans := <-answerCh
for ev := range agent.Resume(ctx, sess, ans) {  // stream isomorphic with Stimulate
    ...
}
```

**Semantics:**

- **Suspension = the iterator ends normally**: yields `EventState(StateWaiting)` →
  `EventWaitInput` and ends — no `EventDone`/`EventError`; `OnCycleEnd`/`AfterStimulate`
  still fire exactly once with `outcome = OutcomeSuspended` (distinguish the suspension
  via `StateWaiting`/outcome — **do not persist an unfinished round**)
- **Pre-execution ask (`Allow` = Ask) = the same suspension mechanism**: the loop yields the identical
  `EventState(StateWaiting)` + `EventWaitInput` pair (the question text comes from the
  ruling's reason), preceded by an ask-kind `EventSandbox` audit record; the host saves
  the Session, shows the question, and resolves via Resume exactly like ask_user;
  **an approved call is never re-gated** (a stateless membrane would re-ask forever),
  while sibling calls of the same round pass the normal gate on replay; the terminal
  resolve record closes the audit chain
- **Utterance ask (`Emit` = Ask) = the same suspension mechanism**: the round's draft is
  withheld — nothing in the event stream contains it (`EventWaitInput.Call` is a zero value and the
  question comes from the ruling's reason) and the draft rides the `Session`; on approval it is
  said **as generated, without re-running that round's Think**, on denial `[sandbox-denied: ...]`
  takes its place and joins Context; the tool calls the round already declared run after the draft,
  and if nothing else was pending the cycle closes with Done
- **Pause = the same suspension mechanism**: yields `EventState(StatePaused)` →
  `EventPaused` (Session snapshot, Call zero value) and ends the iterator normally;
  `agent.Resume(sess, "")` continues without injecting anything (no pending tool); when
  the pause point is before a tool, the current tool is recorded in Session.remaining
  and Resume runs it first
- **`Session` is an opaque value object** (snapshot of round/context/remaining tool calls/
  accumulated output): the host only saves and returns it, never inspects it;
  **it belongs to the cell that suspended it** — `Resume` rejects a handle another cell
  returned (`ErrForeignSession`; replaying it would run A's round on B's organs);
  **single-use** — resuming twice re-executes the remaining tool calls (duplicate side
  effects; host responsibility)
- **`sess.RemainingCalls()`**: the sole sanctioned read-only probe — a clone of
  the calls not yet executed at the suspension point (empty when nothing is left): a
  pause snapshot keeps the whole unexecuted batch (Resume replays it); a WaitInput
  suspension inside a `ParallelActs` batch is empty (the batch fully executed — Resume
  only injects the response, never replays)
- **Persistence**: `sess.Marshal()` produces versioned JSON bytes (version 3 carries the
  owning cell, the wait kind and the withheld draft); the host
  persists them; after a restart `meowire.UnmarshalSession(data)` restores the handle
  and Resume continues — suspensions and pauses recover across processes; a version
  mismatch or an unknown wait name is rejected (a stale or future handle must not be replayed)
- **No extra round**: Resume continues from the suspended round; the Think that digests
  the response uses the suspended round's quota (`MaxRounds` is not extra-consumed)
- **No budget during the wait**: no Think happens while waiting, the trimmers are not called;
  it runs before the next Think after resume
- **Response grammar (three-state ruling for both membrane asks, verbatim injection for ask_user)**:
  **Sandbox asks** resolve by the three-state ruling: an **empty string** = deny (the call side
  gets `[sandbox-denied: declined]` feedback, the text side gets that text in place of the draft);
  a **`[denied:` prefix** = deny with that text (timeout recipe
  `[denied: timeout]` as shown above; it lands in the canonical `[sandbox-denied: ...]`
  form); **any other response** = approve — the pending call executes without re-gating, a
  withheld draft is said as generated.
  **ask_user** responses are written as the suspended call's structured result in `ToolResults`
  (`ID` = the call's `call_xxx`), visible to the first Think after resume — an empty
  string is an empty result; a denial is expressed in the response text itself — the
  `[denied: timeout]` recipe lands as tool feedback the model reads.
  Timeouts are host-controlled (default deny)
- **Remaining tools**: when the suspension happens mid-list, Resume first runs the rest
  of the round's tools, then re-enters Think
- **Resume hooks are identical to Stimulate** (`BeforeStimulate` fires as usual; with
  host append semantics `Session.Context` merges naturally with retrieval results);
  `Close` makes Resume yield `ErrCellClosed`; `Session` is an in-memory handle — it
  dies on host restart (treat as timeout-deny)
- **Division of labor with `BeforeThink`**: `BeforeThink` rewrites this round's
  Prompt (it replaces `p.Context` as a whole slice); `Resume` injects the suspension response (structured result in `ToolResults`) — no overlap

### 6.5 Journaling the stream: `EncodeEvent` / `DecodeEvent`

```go
for ev := range agent.Stimulate(ctx, text) {
    line, err := meowire.EncodeEvent(ev) // one JSON record per event: append it to a log
    ...
}

// later, or in another process
ev, err := meowire.DecodeEvent(line)
```

- Every record carries `WireEvent.Version`; a mismatch is rejected, never read as a close enough
  match. Kinds, states and membrane rulings travel **by name**, so reordering an enum in a new
  release cannot silently reinterpret a log written by the last one
- `EventError` identity: a framework sentinel (`ErrMaxRounds`, `ErrForeignSession`) comes back as
  the identical value — wrapped text included, so `errors.Is` still holds. Any other error (a host's
  own `rate limited`) returns as the same **text** only, and the event lists `err.identity` in
  `Dropped` — the loss is reported instead of hiding behind a false comparison
- A suspension handle inside `EventWaitInput` / `EventPaused` is embedded in its own serialized
  form, so the `Session` version guard still applies: a stale suspension is rejected by the handle,
  not by a looser event wire
- `Event.CellID` names the producing cell, so one log can carry a whole colony and still say who
  spoke; `Dropped` says whether the record arrived complete
- Do **not** `json.Marshal` an `Event`: it writes the `Err` field as `{}` (the text is gone) and
  numbers the enums, all without reporting anything

---

## 7. Step 6: `Close` and Optional Extensions

### 7.1 `Close() error`

Idempotent (CAS); closes the cell then the host Closer; errors are joined
with `errors.Join`. Stimulate/Resume after Close returns `ErrCellClosed`. The host
should `defer agent.Close()`.

### 7.2 Optional extensions

**Synapse (inter-agent messaging reference; a plastic synapse graph since 1.1.1)**:

```go
type Edge struct {
    From   string
    To     string
    Weight float64 // synaptic strength (host learning rules read/write)
    Fired  int64   // cumulative successful deliveries (host statistics)
    Spiked int64   // last successful conduct, Unix nanoseconds (0 = never)
}

type Synapse interface {
    Link(ctx, from, to string, weight float64) error // synaptogenesis (idempotent overwrite, clamp ≥ 0)
    Unlink(ctx, from, to string) error               // synapse elimination (missing → ErrNotLinked)
    Reinforce(ctx, from, to string, delta float64) error // LTP/LTD (result clamp ≥ 0)
    Fire(ctx, sig Signal) error                      // delivery (weight below Floor → ErrWeakSynapse; a success writes Fired++ and Spiked together)
    Edges(ctx, from string) ([]Edge, error)          // out-edge / whole-graph snapshot (persistence primitive)
}
```

- `SignalKind`: `KindStimulus` (a request carrying `ID`/`From` is a task, and a task gets an answer) / `KindResponse` (the answer, `ReplyTo` names the request) / `KindNotice` (side-channel notice, runs no DecisionLoop)
- **Assembly order (a colony is circular)**: the routing table names the agents while each agent carries that table, so build the graph first, create the members, then hand it the table:
  ```go
  syn := meowire.NewDirect(meowire.DirectConfig{})   // reference graph, no resolver yet
  a, _ := meowire.New(meowire.Blueprint{Organs: meowire.Organs{ID: "a", Colony: syn, ...}})
  b, _ := meowire.New(meowire.Blueprint{Organs: meowire.Organs{ID: "b", Colony: syn, ...}})
  r, _ := meowire.Resolve(a, b)                      // ID → each cell's own inbox (duplicate IDs refused)
  syn.SetResolver(r)                                 // close the circle; custom routers do this their own way
  syn.Link(ctx, "a", "b", 1)                         // only a linked direction conducts
  ```
- **The inbox belongs to the framework**: every cell carries an inbox of capacity `InboxCapacity` (8); the framework drains it into `Prompt.Stimuli` at the next Think gap, so the host maintains no channel map and runs no consumer pump
- The one framework-read meaning of `KindNotice`: if its Payload is a tool name, calls to that name are refused this round before the gate even sees them (`Prompt.Inhibit`); a notice never starts a loop itself, and everything else it carries is the host's own convention
- **Delegation is a return value, not a Fire call**: a tool returns `Effect{Send: &Signal{To, Skill, Payload}}`; the framework mints `ID`/`From`/`Kind`/`Status=submitted`, delivers through `Organs.Colony` and suspends that call on the same Session path `WaitInput` uses. An undeliverable request never suspends — its error is that call's tool feedback (resistance is feedback)
- **The answer is the framework's debt too**: an invocation that served a request (one carrying `ID`/`From`) replies to the requester when it ends, with its final output as the payload and the state computed by `TaskOutcome` from how the loop terminated. That gives each of the six `TaskStatus` values exactly one writer: `Submitted` = the request going out, `Working` = the request drained into a round, and `NeedsInput`/`Completed`/`Failed`/`Cancelled` = the terminal mapping (a consumer that abandons the iterator invents no state and sends no answer). A cell with no Colony that owed an answer reports it through `OnError` instead of dropping it
- **Pairing is the kernel's, continuation is the host's**: a reply landing in the sender's inbox is matched by `ReplyTo` to the round that delegated and queued; the host reads `agent.Resumptions()` (a snapshot — reading does not consume), calls `Resume(r.Session, string(r.Response))`, then `Ack(r.SignalID)`. One task may report `needs-input` before it reports `completed`; both pair against the same delegation
- **Conduction floor `DirectConfig{Floor}` (host-injected; 0 switches gating off)**: an edge weighing less refuses `Fire` outright (`ErrWeakSynapse`) while staying in the graph — still linkable, still Reinforce-able back over the line, carrying nothing meanwhile. `Prune`'s `weightFloor` is the elimination threshold; the two are never merged because their consequences differ (traffic stopped vs. connection deleted)
- **The graph keeps its own moments**: every successful delivery writes `Fired++` and `Spiked = now` in one step, so `STDPFrom(ctx, s, pre, post, params)` pairs spikes from graph state alone (a firing cell is observable here only through what it sent) and the host carries no clock; `Edges` exports both, so a restored snapshot remembers them
- **Routing by capability is a host broadcast, not a delegation**: `NewSkillIndex(agents...)` indexes the same projection the Agent Card publishes (`Methods` → skills); `TargetsFor(skill)` answers "who can do X", and `FanOut(ctx, syn, sig)` fires at each target and **reports per target** (the delivered list plus one wrapped error per refusal, joined) — a capability may match zero, one or many cells, so it has no return path; a round that wants an answer delegates with `Effect.Send` instead (where `Signal.Skill` is content, not an address)
- Real routing (channel/HTTP/Redis/gRPC) is host-chosen; `Colony` is only the `Fire` subset of `Synapse`, and a host tool may still call `Fire` directly for side-channel messages (resistance flows back as `EventToolResult` feedback, never a hard stop)
- Synapse errors (`ErrNoTarget`/`ErrNotLinked`/`ErrTargetBusy`/`ErrWeakSynapse`) are re-exported at the api layer, along with `Edge` (now carrying `Spiked`) and `DirectConfig`
- **Persistence round-trip**: export the whole graph via `Edges(ctx, "")` at
  runtime, serialize it host-side; on restart deserialize and inject via
  `NewDirect(DirectConfig{Resolver: r, Initial: restored})`. Learning rules
  (Hebbian/STDP) are host-side — the framework stores state, never decides

**Flat multi-agent model**: one Agent = one kernel; the host owns all
instances. Sub-agents are created inside host Effector tools
(`New → Stimulate`), transparent to the main loop.

### 7.3 Multi-agent state visibility: the host-side trio

> The framework event stream is "visible only to its consumer": the main loop
> never sees sub-agent events (flat model). Unified main/sub-agent state
> visibility is implemented by the host-side trio — **zero framework changes**:
> StreamHub (shared state container) + Thinker wrapper (streaming, optional) +
> Hooks/tools (state & plan, required).

**StreamHub — shared state container (keyed by `Organs.ID`)**

```go
type StreamHub struct {
	mu      sync.RWMutex
	streams map[string]chan meowire.Event // streaming output (optional; no channel = silent)
	tasks   map[string]*TaskStatus        // task state (required, written by Hooks)
	plans   map[string]string             // plan tree (written by update_plan tool)
}
```

The host creates **one instance** and injects the same hub into every agent
(main and sub); the UI reads the hub for a real-time view of all agents.

**① Streaming (optional) — forwarded inside the Thinker wrapper**

`EventText` is always whole-segment (per-round `Decision.Text`); **token-level
streaming can only be produced by the host's Thinker implementation**. The host
wraps the Thinker and pushes every token from the LLM streaming API into
`hub.streams[id]`:

```go
type streamingThinker struct {
	inner llm.Client // host's streaming LLM client
	id    string     // = Organs.ID
	hub   *StreamHub
}

func (t *streamingThinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	ch := t.hub.channel(t.id) // no channel registered → nil → silent
	stream := t.inner.Stream(p)
	var sb strings.Builder
	for tok := range stream {
		sb.WriteString(tok)
		if ch != nil {
			select {
			case ch <- meowire.Event{Kind: meowire.EventText, Text: tok}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return &meowire.Decision{Text: sb.String()}, nil // full text back to the framework
}
```

Silent execution (sub-agent running in the background, no push): do not register
a channel when creating that agent; `channel(id)` returns nil and naturally
pushes nothing.

**② State sync (required) — written by Hooks callbacks**

The host writes state into the hub in `Organs.Hooks`; the framework invokes
them at fixed points. The id is captured via closure (`AfterAct` may also use
`a.CellID`):

```go
hooksFor := func(id string) *meowire.Hooks {
	return &meowire.Hooks{
		AfterThink: func(ctx context.Context, d *meowire.Decision) error {
			hub.updateTask(id, TaskStatus{State: "thinking", LastText: d.Text})
			return nil
		},
		AfterAct: func(ctx context.Context, a *meowire.Action, e *meowire.Effect, err error) {
			hub.updateTask(a.CellID, TaskStatus{State: "acting", LastTool: a.Call.Name})
		},
		OnCycleEnd: func(ctx context.Context, output string, outcome meowire.CycleOutcome) {
			switch outcome {
			case meowire.OutcomeDone:
				hub.updateTask(id, TaskStatus{State: "done", Output: output})
			case meowire.OutcomeSuspended:
				hub.updateTask(id, TaskStatus{State: "needs-input"})
			default:
				hub.updateTask(id, TaskStatus{State: "failed"})
			}
		},
	}
}
```

**③ plan tree — `update_plan` tool + `BeforeThink` re-injection**

Plan content is generated by the LLM (brain decision); the write channel is a
host tool (host tool pattern):

```
LLM calls update_plan → host Effector writes hub.plans[id] → UI shows in real time
              ↑                                          ↓
Hooks.BeforeThink writes the latest plan back to p.Plan ← LLM sees its own plan next round
```

```go
// tool list (LLM may call)
{Name: "update_plan", Desc: "update the task plan tree (JSON)",
	Input: `{"type":"object","properties":{"nodes":{"type":"array"}}}`, Output: "ok"}

// Effector dispatch: write hub (host's own plan tree)
case "update_plan":
	hub.updatePlan(a.CellID, a.Call.Args)
	return &meowire.Effect{Result: "ok"}, nil

// Hooks.BeforeThink: re-inject p.Plan (pointer, takes effect next Think)
BeforeThink: func(ctx context.Context, p *meowire.Prompt) error {
	p.Plan = hub.plan(id) // id captured by closure
	return nil
},
```

**Unified sub-agent view**: the host injects the **same hub instance** when
`New`-ing a sub-agent inside an Effector tool; main/sub state converges
automatically:

```go
case "spawn_agent":
	// Same hub injected into the sub-agent; Blueprint define-once, New-many (flat model)
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      args.ID,
			Think:   &streamingThinker{inner: t.inner, id: args.ID, hub: hub}, // same hub
			Act:     sameEffector,
			Hooks:   hooksFor(args.ID), // closures over the same hub
			Closer:  closerStub,
			Sandbox: sandboxStub,
			Budget:  passBudget, // pass-through trimmers: Trimmer + TrimResults + MaxTokens>0
			Tools:   subTools,
		},
		Config: subCfg,
	}
	sub, _ := meowire.New(bp)
	// … consume sub's event stream, or run asynchronously
```

---

## 8. Complete Code Skeleton

```go
package main

import (
	"context"
	"fmt"

	meowire "github.com/qyiun666/meowire/api"
)

// ① Thinker: wraps the LLM
type llmThinker struct{ /* LLM client */ }

func (t *llmThinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msgs := buildMessages(p) // System/Identity/Methods/Tools/Context/Bounds/Input/Plan → messages
	resp := t.client.Chat(ctx, msgs, toolSchemas(p.Tools))
	return &meowire.Decision{
		Text:      resp.Text,
		ToolCalls: resp.Tools,
		Usage:     resp.Usage,
	}, nil
}

// ② Effector: tool dispatch
type effector struct{ registry map[string]func(ctx context.Context, args string) (*meowire.Effect, error) }

func (e *effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
	fn, ok := e.registry[a.Call.Name]
	if !ok {
		return &meowire.Effect{Err: "unknown tool: " + a.Call.Name}, nil
	}
	return fn(ctx, a.Call.Args)
}

// ③ Assemble + run (Blueprint define-once, multi-instance reuse)
func main() {
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-001",
			Think:   &llmThinker{},
			Act:     &effector{registry: toolRegistry},
			Closer:  &closer{},
			Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: injectMemory, OnCycleEnd: persistOutput}),
			Sandbox: &sandbox{},
			Budget:  &meowire.ContextBudget{MaxTokens: 4000, Trimmer: trim, TrimResults: trimResults},
			System:  "You are a meow agent, answer in English",
			Tools: []meowire.ToolSpec{
				{Name: "calc", Desc: "calculator", Input: `{"type":"object","properties":{"expr":{"type":"string"}}}`, Output: "number"},
			},
			Context:  loadHistory(ctx), // host-managed history (MemHop)
			Identity: "You are meow, role assistant, warm tone",
			Methods:  []meowire.MethodSpec{{Name: "spawn_agent", Desc: "spawn a sub agent"}},
		},
		Config: meowire.Config{MaxRounds: 8, MaxToolOutput: 2000, MaxRetries: 2},
	}
	agent, err := meowire.New(bp)
	if err != nil {
		panic(err) // assembly failure: missing port
	}
	defer agent.Close()

	for ev := range agent.Stimulate(ctx, "check my order") {
		switch ev.Kind {
		case meowire.EventText:
			streamToClient(ev.Text)
		case meowire.EventToolCall:
			showToolCall(ev.ToolCall)
		case meowire.EventToolResult:
			showToolResult(ev.Effect)
		case meowire.EventDone:
			saveOutput(ev.Output)
		case meowire.EventError:
			handleError(ev.Err) // incl. ErrMaxRounds → may Step-Resume
		}
	}
}
```

---

## 9. Pitfall Checklist (read before integrating)

1. **`Hooks.BeforeThink` must replace `p.Context` as a whole** — appending can
   corrupt the loop context via the shared backing array (§2.5)
2. **Port concurrency safety**: Thinker/Effector are called concurrently if the
   same Agent is stimulated concurrently
3. **Host ports must respect ctx cancellation**; `ask_user` must never block
   inside Effector (framework-level suspension protocol, §6.4) — the host-side
   wait timeout is self-controlled (default deny `[denied: timeout]`)
4. **`Organs.Context` is a slice**: update it to the latest history via
   `BeforeThink` before each Think (MemHop)
5. **`ErrMaxRounds` is not a bug**: it fires when the last round has pending
   tool calls and the round budget is exhausted; Step-Resume to continue is the intended use
6. **The event stream is an observation mirror**: the host never feeds data
   back into an open iterator; feedback goes through the next `Stimulate`
7. **Streaming UX lives in the Thinker**: `EventText` is always whole-segment;
   token deltas never enter the event stream
8. **Error handling**: errors from host ports are wrapped by the framework
   (`nerve.hookBeforeThink: ...` etc.) and surface via `EventError`; classify
   with `errors.Is` (e.g. `meowire.ErrMaxRounds`)
9. **Pause never interrupts a running tool**: Pause takes effect at gap points;
   interrupt a running tool with ctx cancellation instead (§2.4)
10. **hub lifecycle belongs to the host** (§7.3): a full streaming channel blocks
   the agent loop (Thinker forwarding is synchronous); the host must manage
   backpressure (buffer size) and cleanup (close/delete the channel after the
   agent ends); the framework does not participate
11. **Sub-agents must be injected with the same hub instance as the main agent**
   (§7.3): a different instance means losing contact; the unified state view
   depends on the shared instance

## 10. Related Documents

| Document | Location | Content |
|----------|----------|---------|
| README | [README.md](README.md) | English quick start + concept overview |
| README (zh-CN) | [README.zh-CN.md](README.zh-CN.md) | Chinese quick start + concept overview |
| Host Integration Guide (zh-CN) | [host-integration.md](host-integration.md) | Chinese integration contract |
| Reference Host (zh-CN) | [reference-host.md](reference-host.md) | Step-by-step runnable AI host (LLM, tools, permissions, memory, multi-agent, persistence) |
| api module context | [api/agent.md](api/agent.md) | Long-term api package context, key decisions, pitfalls |
| Decision loop source | [internal/nerve/loop.go](internal/nerve/loop.go) | Loop orchestration (Think→Act→yield) |
| Integration tests | [test/](test/) | End-to-end behavior (lifecycle, port injection, event sequences) |
