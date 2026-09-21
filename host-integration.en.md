# meowire Host Integration Guide

> Audience: host developers/AIs integrating meowire.
> This document is the authoritative integration contract — all interface
> signatures, field semantics, and event sequences match the source code.
> Module path: `github.com/qyiun666/meowire`. Public package:
> `github.com/qyiun666/meowire/api` (aliased `meowire`).

## 0. One-line model

meowire is an **orchestration kernel with its own brain**: the brain is bundled (openai-go —
the host passes five parameters via `Organs.Brain`: BaseURL/Key/Model/Stream/Mode, aimed at any
OpenAI-compatible endpoint; `Mode` selects the wire — `BrainModeChat` (default, zero value
included) speaks chat completions, `BrainModeResponses` speaks the Responses API, and both wires
render the same stateless Prompt and fold back into the same Decision), the host implements six ports (tools, cleanup, hooks, the
permission gate, context trimming, experience memory), and the framework runs the Think →
Act → yield event loop. **The framework manages no history and no multi-agent** — the six
ports plus the brain parameters are all required; a missing one is an error.

## 1. Integration Flow Overview (6 Steps)

```
Fill Brain parameters → Implement six ports → Assemble Organs → Set Config → Assemble Blueprint → New() → Stimulate() consume event stream → Close()
```

| Step | What | Key point |
|------|------|-----------|
| 1 | Fill `Brain` parameters; implement Effector/Closer/Hooks/Sandbox/ContextBudget/Memory | The brain arrives by parameters; all six ports required |
| 2 | Assemble the `Organs` struct | Inject ports + fixed context |
| 3 | Set `Config` | Zero values are defaults; nothing must be set explicitly |
| 4 | Assemble `Blueprint{Organs, Config}` and `New(bp)` | Missing port/callback returns an error; incomplete Budget too |
| 5 | `for ev := range agent.Stimulate(ctx, text)` | Consume the event stream |
| 6 | `agent.Close()` | Idempotent; Stimulate/Resume after Close yield `EventError` (`ErrCellClosed`) |

---

## 2. Step 1: Fill the Brain parameters, implement the Six Ports (Host Capabilities)

### 2.1 Brain — the bundled organ (parameters, not a port)

The brain is the one organ the framework ships: the host fills five parameters in
`Organs.Brain`, the composition root constructs it and injects it, and the loop calls it as
its Thinker. The host never implements a Thinker and never imports an SDK.

```go
type BrainConfig struct {
    BaseURL string    // OpenAI-compatible endpoint ("" = the official one)
    Key     string    // credential (required)
    Model   string    // model id (required)
    Mode    BrainMode // wire selector: BrainModeChat (default, zero value included) or BrainModeResponses
    Stream  bool      // true = SSE transport
}
```

Streaming deltas ride `meowire.WithSink(ctx, sink)`: mount the Sink on the context handed to
`Stimulate`/`Resume`, and the brain's text deltas arrive **before** the output membrane rules
(stream-and-correct — `EventText`, whole and already ruled, replaces what was pushed); the
event stream only carries whole-segment Text. Retry stays single-layer: transport retries are
off, the whole budget belongs to `Config.MaxRetries`. A round with no tools (`Prompt.Tools`
empty) sends no `tools` field at all — both wires omit the field rather than send an empty
array, which some OpenAI-compatible endpoints reject outright.

**The `Prompt` entering the brain (assembled by the framework; `BeforeStimulate`/`BeforeThink` hooks may rewrite it) fields:**

| Field | Content | Injected by |
|-------|---------|-------------|
| `System` | System instructions ("You are a support agent…") | Host, fixed at construction |
| `Identity` | Identity description text (host composed, e.g. "You are meow, role assistant, warm tone") | Host, fixed at construction |
| `Methods` | Built-in capability description (describes only, never consumed by the framework; `MethodSpec{Name, Desc, Input, Output}`) | Host, fixed at construction |
| `Tools` | Available tool list (function schemas) | Host, fixed at construction |
| `Context` | Context slice: host base + framework-appended sandbox denials (`[sandbox-denied: ...]`, emitted in final form); tool results never enter the text track | Host base + framework appends |
| `ToolResults` | Structured tool results (`ToolResult{ID, Name, Result, Err}`): accumulated within the cycle, `ID` is the LLM-provided call id (`call_xxx`), `Result`/`Err` carry the truncated raw output; rendering (tool-role messages, `[tool_call_id=xxx]` markers, plain text) is the bundled brain's | Framework, appended within the cycle |
| `Bounds` | Execution boundary description (`Sandbox.Bounds()` snapshot, e.g. "only /workspace") | Framework, once per Stimulate/Resume |
| `Reflection` | Self-review note from the previous round (Reflexion slot, passed through verbatim) | Host (write-back in `BeforeStimulate`) |
| `Memories` | This round's recalled experience records (`Memory.Recall` output; replaced wholesale per round, never accumulated, never enters a Session snapshot) | Framework, before each Think |
| `Input` | Current stimulus text (the Stimulate argument) | Framework, per round |
| `Plan` | Task plan/progress text | Host, via `Hooks.BeforeThink` (`p.Plan` is pointer-writable; the same round's Think reads it); pairs with a host `update_plan` tool for a closed loop (§7.3 ③ — a tool's write into the host hub is read back by the hook next round) |

**The `Decision` leaving the brain (observable via the `AfterThink` hook) fields:**

| Field | Content | Constraint |
|-------|---------|------------|
| `Text` | LLM text output (whole segment) | Surfaced as EventText by the framework; **token deltas never enter the event stream** |
| `ToolCalls` | Tool calls this round `{ID, Name, Args}` | `Args` is a JSON string; **empty = end of cycle** |
| `Usage` | Token usage `{Prompt, Completion, Total}` | **nil = skip the EventUsage accounting event** |

The field-by-field account of that rendering and transport (assistant/tool pairing, the retry
trade-off, streaming versus the output membrane) is
[thinker-openai-go.md](thinker-openai-go.md) — the bundled brain's implementation spec and
protocol reference.

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
| `Effect.Result` | Success result text (truncated by the framework into `ToolResults.Result`; rendering is the bundled brain's) |
| `Effect.Err` | Tool-side error text (truncated into `ToolResults.Err`; non-empty `Err` = the call failed) |
| `Effect.WaitInput` | Non-empty = request external input (the field carries the question text); the framework yields `EventWaitInput` (carrying a Session) and **ends the iterator normally**; the host collects the answer and calls `agent.Resume(sess, meowire.Response{Answer: text})` (see §6.4) |
| return error | Execution-layer error (also written to `ToolResults.Err`; the loop continues) |

**Host responsibilities:**
- Tool registry + dispatcher: route by `Name`, deserialize `Args`, serialize results
- Multi-agent tools live here: the `spawn_agent` tool itself `New`s an `Agent`, consumes
  its `Stimulate` stream and hands the result back as an ordinary `Effect.Result`
  (see §7.2) — the kernel never learns a second instance exists
- `ask_user`-style tools: **return `&Effect{WaitInput: question}` instead of blocking
  synchronously** (blocking drags the Close wait; the suspension is expressed by the
  framework so the UI can show "the cat is waiting for an answer"), see §6.4
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
  the loop checks it at two gap points (before each Think, before each tool execution; a `ParallelActs` batch leaves one gap point before the whole batch, and its snapshot carries the unexecuted batch)
- **Snapshot suspension (non-blocking)**: an honored pause yields
  `EventState(StatePaused)` → `EventPaused` (carrying a Session snapshot) and **ends
  the iterator normally**; the host resumes via `agent.Resume(sess, meowire.Response{})`
  (nothing is being asked, so neither Response field is read) — the same Session/Resume
  path as ask_user (LangGraph-interrupt-style single suspension primitive)
- **Tool-gap pause**: the pause point sits before the tool runs, so the snapshot records
  the current tool and the calls after it into Session.remaining; Resume runs them
  first, then re-enters Think (the current tool has not run yet — nothing is lost)
- Pause state is **Agent-level and survives across Stimulate calls**: a Stimulate
  started while paused first suspends at its entry gap point (StatePaused + EventPaused)
- `Pause` / `Unpause` are **idempotent and concurrency-safe**; no-ops after `Close`
- **`Resume` clears only the pause request it is answering**: resuming a pause is
  the intent to continue, so that loop does not suspend again at its first gap point;
  resuming any other flavour (tool input, either membrane ask) leaves a standing pause
  request alone — it belongs to whichever loop is running, and an unrelated answer
  swallowing it would be a silent loss. `Unpause` is only for backing out before the
  pause takes effect
- Difference from Step-Resume: Step-Resume abandons the round and restarts
  statelessly; Pause **keeps the in-cycle state (Session snapshot) and suspends
  in place**, resuming without consuming a round

### 2.5 Hooks — interception callbacks (all eight required; explicit no-op where no behavior is wanted — absence fails assembly)

```go
type Hooks struct {
    BeforeStimulate func(ctx context.Context, p *Prompt) error // start of a Stimulate/Resume; may edit all prototype content fields (Bounds is read-only — a framework snapshot, never written back), written back for every round; error aborts the whole run
    AfterStimulate  func(ctx context.Context, output string)     // end of a Stimulate/Resume (exactly once on all paths)
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
| `BeforeStimulate` | Start of each Stimulate/Resume, before any event | Whole-run static injection (persona, retrieval prefix — one prototype edit applies to every round). Recall now lives on `Memory.Recall` (§2.8); don't read memory twice |
| `AfterStimulate` | End of each Stimulate/Resume (exactly once on all paths) | Session settlement. Persisting the round now lives on `Memory.Remember` (§2.8, ahead of `OnCycleEnd`) |
| `BeforeThink` | Before each Think | Inject retrieved memory, update Plan |
| `AfterThink` | After a successful Think | Serialize the Decision back into Plan/memory |
| `BeforeAct` | Before each tool execution | Approval, rewrite tool arguments |
| `AfterAct` | After tool execution | Tool logging, failure degradation (`err` non-nil = effector failure) |
| `OnError` | On unrecoverable error | Alerting |
| `OnCycleEnd` | **Exactly once per cycle** (normal, error, suspension and early-consumer-stop arms) | Settlement, persist final output; `outcome` classifies how the cycle ended (Done/Suspended/MaxRounds/Error/Aborted; zero reserved = iterator abandoned early → Aborted) |

**⚠️ `BeforeThink` must replace `p.Context` as a whole (`p.Context = append(p.Context[:0], newCtx...)` or assign a new slice) — it shares the backing array with the loop's accumulated context; appending into it can corrupt the loop context. The same applies to `p.ToolResults` (shared with the loop's structured track): replace wholesale, never append in place.**

### 2.6 Sandbox — the permission gate (the security red line, both sides of the loop)

```go
type Sandbox interface {
    Allow(ctx context.Context, a Action) (verdict Verdict, reason string, err error)
    Emit(ctx context.Context, u Utterance) (verdict Verdict, reason string, err error)
    Bounds() string // execution boundary description (host defined), snapshotted once per Stimulate/Resume
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
- A `Verdict` outside the three named states (a constructed integer) denies the same way, and the
  refusal names it: `sandbox returned an unknown ruling <n>` — a value this build cannot name
  cannot have permitted anything
- The audit record tells the sides apart by `Call`: a tool-side ruling names the gated call, an
  utterance-side ruling carries a zero `Call`. Every ruling yields exactly one record, and an ask
  chain closes with its terminal record
- `Bounds()` returns the execution boundary description; the framework
  snapshots it once per Stimulate/Resume and surfaces it to the LLM via `Prompt.Bounds`
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
o.Act     = meowire.FallbackEffector(localTools, remoteTools)  // one pair of hands
```

The brain has no combinator: it is the bundled organ, and there is nothing behind it to fall
back to — a different model is a new `New` with different `Brain` parameters.

- Every combinator **returns the port type itself**, so a composed organ is wired exactly like a
  plain one: no slot, no event, no config field was added, and `Replace` accepts a stack the same
  way it accepts a single organ
- `GuardStack` rules by severity, not by layer order: the first Deny ends the stack (a later, more
  permissive layer gets no vote), the first Ask wins over Allow, and a layer that errors is the
  fail-closed Deny the port contract already defines. `Bounds()` reports the first non-empty
  boundary. An empty stack denies — a membrane with no layers guards nothing
- `FallbackEffector` returns the first member's answer that carries **no
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
    ID:      "agent-001",                       // required, unique per live agent (no default)
    Brain:   meowire.BrainConfig{               // required (parameters): the bundled brain
        BaseURL: "https://api.deepseek.com/v1", // "" = the official OpenAI endpoint
        Key:     os.Getenv("LLM_KEY"),
        Model:   "deepseek-chat",
    },
    Act:     myEffector,                        // required
    Closer:  myCloser,                          // required
    Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: ...}), // required: all eight callbacks (helper fills missing ones)
    Sandbox: mySandbox,                         // required
    Budget:  &meowire.ContextBudget{...},       // required
    Mem:     myMemory,                         // required

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
| `ID` | string | Unique agent ID, **required with no default** — name each instance when one `Blueprint` is `New`ed many times; the framework authors events by it (`Event.CellID`) and checks suspension handles against it, and the host uses it to tell its own instances apart | **yes** |
| `Brain` | BrainConfig | Bundled-brain parameters (BaseURL "" = official endpoint; Key/Model required; `Mode` selects the wire — `BrainModeChat` (default, zero value included) = chat completions, `BrainModeResponses` = the Responses API; a value outside the enum fails assembly) | **required (parameters)** |
| `Act` | Effector | Tool execution | **yes** |
| `Closer` | Closer | Resource cleanup | **yes** |
| `Hooks` | *Hooks | Interception callbacks (all eight callbacks required) | **yes** |
| `Sandbox` | Sandbox | Permission gate (`Allow` guards execution, `Emit` guards the round's text egress) | **yes** |
| `Budget` | *ContextBudget | Token regulator (text track + structured-feedback track) | **yes** |
| `Mem` | Memory | Experience port (`Recall` before each Think, `Remember` at the invocation terminal) | **yes** |
| `System` | string | System instructions; feeds Prompt.System | no |
| `Identity` | string | Identity description text (host composed); feeds Prompt.Identity | no |
| `Methods` | []MethodSpec | Built-in capability description (describes only, never consumed by the framework); `MethodSpec{Name, Desc, Input, Output}`; feeds Prompt.Methods | no |
| `Tools` | []ToolSpec | Tool list; feeds Prompt.Tools; `ToolSpec{Name, Desc, Input, Output}`, `Input` is a JSON Schema (the brain builds tool definitions from it) | no |
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
    Organs Organs   // brain parameters + six ports + fixed context (all required)
    Config Config   // zero values are defaults
}

bp := meowire.Blueprint{Organs: organs, Config: cfg}
agent, err := meowire.New(bp)
```

- **Blueprint is define-once, assemble-many**: the same `bp` can `New` multiple independent Agent instances (flat-model multi-agent), but `Organs.ID` names one instance — every `New` must give it a different one
- `New` validates against the **assembly graph** (blueprint = data-object nodes + slot edges), two levels only:
  - `error` (missing required port / missing hook callback / incomplete Budget / empty `Organs.ID`): **always blocks**, returns `meow: required port X not injected`
    (X ∈ Act/Closer/Hooks/Sandbox/Budget/Mem plus the missing callback's own name — `BeforeStimulate`…`OnCycleEnd`, i.e. the `Name` of H1–H8; `Issue.Wire` is what carries a blueprint id like P4/H4), multiple findings joined; the brain's parameters (Model/Key/Mode) and an empty `Organs.ID` have their own findings
  - `info` — six findings in total (empty Identity, empty Tools, empty Context, `MaxRounds<=0` taking
    DefaultMaxRounds(8), `ParallelActs` on as a reminder that the Effector **must** be concurrency-safe,
    and `MaxParallelActs` set while `ParallelActs` is off so the ceiling binds nothing): **never blocks** —
    inspect via `meowire.Validate(organs, cfg)`. The framework never tests whether the Effector really is
    safe to call concurrently; that finding is a reminder, not a check
  - No `warn` level: against the host every wiring point is required — a missing point is a missing organ, there is no "half-wired pass" (sub-slots implied by a parent — `P3b`/`P5b`/`P5c`/`P6b`/`P7b` — and the api-injected `G1` are not checked separately; swapping the parent swaps the whole port)
- `New` runs three steps in order: **validate** the blueprint against the graph, **boot** every
  organ that declared `Bootable` (§2.9), then **construct** the cell. Nothing after a failing boot
  is reached, and the failed attempt is released through the host `Closer`
- After assembly the host calls `Stimulate` / `Resume` / `Pause` / `Unpause` / `Close`, and may
  swap ports at runtime via `Replace` (below)
- The brain parameters, six ports and eight hook callbacks must all be present — **no stubs, no optional port, no "minimal runnable" path**; the brain is the one organ the framework provides (every other organ has no default implementation), and `meowire.FullHooks(...)` declares unneeded hooks as explicit no-ops

### 5.1 Dynamic wiring: `Replace` (runtime organ swap)

```go
oldPort, err := agent.Replace(meowire.SlotSandbox, stricterMembrane) // takes effect at the next Stimulate/Resume
```

- Swappable slots: `SlotAct` / `SlotSandbox` / `SlotBudget` / `SlotMem` / `SlotHooks` (the
  blueprint's `WirePoint.Slot` field is the single source; filter `Connectome()` on
  `WirePoint.Slot != ""` to enumerate it (`WiringDiagram(o)` adds each edge's `Filled` state; `SwappableSlots`
  is an internal derivation, not on the public surface) and a guard test pins the constants to it);
  `Closer` (resource binding) and the pause gate (framework wiring) are never swappable — and
  **the brain has no slot**: a different model is a new `New` with different `Brain` parameters
- Semantics: each `Stimulate`/`Resume` snapshots ports into a fresh LoopContext — an **in-flight
  run is unaffected**; the swap takes effect at the next `Stimulate`/`Resume`; the previous
  port is returned (host decides whether to shut the old implementation down — the framework never
  closes an organ it swapped out); an incoming organ that declares `Bootable` is booted once the slot
  has accepted it and before the swap commits, so a failing boot — or a mistyped slot name —
  leaves the wiring exactly as it was
- Concurrency-safe; after `Close` it refuses the swap with `ErrCellClosed` and leaves the incoming port unbooted; **rejects nil and incomplete ports** (a Budget needs
  Trimmer + TrimResults + MaxTokens, a Hooks needs all eight callbacks); unknown slot or wrong port type returns an error
- **Audit event**: every successful Replace records a `ReplaceAudit{CellID, Slot, OldType, NewType}`
  emitted as `EventReplace` at the start of the next Stimulate/Resume (the moment the swap
  takes effect — same level as `EventSandbox`, persistable); failed swaps record nothing;
  zero output without swaps. The host closes its organ-swap audit loop from the event
  stream instead of maintaining a hand-rolled state machine

### 5.2 Assembly self-check: the graph and its readers

```go
for _, is := range meowire.Validate(o, cfg) { // error findings New also rejects; info ones are visible only here
    fmt.Println(is.Level, is.Wire, is.Msg)
}
for _, sl := range meowire.WiringDiagram(o) { // one blueprint edge + whether this assembly filled it
    fmt.Println(sl.Wire.ID, sl.Wire.TargetID, sl.Filled)
}
fmt.Println(meowire.RenderDiagram(o))          // the same state as ASCII
fmt.Println(meowire.PortOrder())               // boot order: Act, Closer, Hooks, Sandbox, Budget, Mem
```

- `Connectome()` (edges) and `ConnectomeNodes()` (data-object nodes) **are** the blueprint;
  `WiringDiagram(o)` is that blueprint crossed with one host assembly, and each `Slot` carries the
  whole `WirePoint` plus `Filled`. `RenderDiagram`/`RenderJSON` render the same graph for humans and
  machines; `Validate` reports against it.
- Node and edge counts live in the code — take them from `Connectome()` at runtime instead of copying
  a number into a document, which would rot on the next blueprint change.
- Framework built-ins (`F1` tool feedback, `F2` brain, `G1` pause gate) always read as filled: they
  are not host slots.

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
(The serial path's shape. With `ParallelActs` on and more than one call in a batch it becomes three
phases: the whole batch's `EventToolCall`s are announced in call order → the batch is gated (`EventSandbox`)
→ `EventToolResult`s land in call order; the gap point sits before the batch, see §2.4.)

**Pause path (takes effect at gap points; snapshot suspension — the iterator ends normally, Resume continues):**

```
… → EventState(paused) → EventPaused(Session snapshot) → iterator ends normally (no Done/Error)
→ host calls Resume(sess, Response{}) → remaining tools run first → back to EventState(thinking) → original sequence continues
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
→ host calls Resume(sess, Response{Answer: ...}) → EventSandbox(terminal) → EventText(draft as generated, or the veto text)
  → [that round's tools run as usual → back to EventState(thinking)] or [nothing left → EventState(done) → EventDone]
```

**Wiring/config audit (at the start of the next Stimulate/Resume, before any other event; both kinds in order):**

```
EventReplace(slot/old/new) × N → EventConfig(old/new) × M → normal sequence follows
```

Undelivered audits are not lost: when the run dies in its prelude (a refused `BeforeStimulate`, a
cancelled context, a consumer that stopped mid-list), the cell requeues the tail that never reached
the host and the next call leads with it — the exactly-once audit promise survives a failed prelude
(`TestAbandonedStreamRequeuesItsAudits`, `TestCellReplaceAuditSurvivesFailedPrelude`).

**Error path:**

```
EventState(error) → EventError(Err)
```

**After Close:** Stimulate/Resume yields `EventError(ErrCellClosed)` directly.

### 6.2 `Event` fields (only the fields for the Kind are set; the rest are zero values)

Three pieces of information are not kind-specific: `CellID` names the cell that produced the
event, `Seq` gives its emission order **within that cell** (1-based, strictly increasing across
Stimulate and Resume, restarting with the process) and `TS` is the emission moment (Unix
milliseconds) — all three are stamped at the cell boundary, including the error a closed agent
raises, so a journal can tell "this agent had nothing to say" from "a record went missing".
`Dropped` names values that could not cross the event wire (§6.5) — the loop itself never fills
it, so a non-empty `Dropped` means "this event was restored from a log".

| Kind | Active field | Content |
|------|--------------|---------|
| `EventText` | `Text` | This round's text, **already through the output membrane** (`[sandbox-denied: reason]` when `Emit` denied) |
| `EventToolCall` | `ToolCall *ToolCall` | Tool the LLM decided to call |
| `EventToolResult` | `Effect *Effect`, `ToolCall *ToolCall` | Tool execution result (includes Sandbox denials: `Effect.Err = "[sandbox-denied: reason]"`); `ToolCall` echoes the call for ID association |
| `EventSandbox` | `Verdict *SandboxVerdict` | Membrane ruling audit record (ruling Ruling: allow/deny/**ask**, the gated tool or a zero value for the text side, policy reason, ask question, evaluation error); one record per ruling on either side of the loop, an ask chain closes with its terminal second record |
| `EventState` | `State LoopState` | Loop state (idle/thinking/acting/paused/**waiting**/done/error) |
| `EventDone` | `Output` | Accumulated text output of the whole cycle |
| `EventError` | `Err` | Unrecoverable error: the sentinels `ErrMaxRounds`/`ErrCellClosed`/`ErrForeignSession` match with `errors.Is`; a zero-value handle passed to `Resume` yields `nerve: resume: invalid session` (not a sentinel — match it by text; resuming the same handle twice is not detected — the remaining tool calls replay, see §6.4); a refused hook, an exhausted Think retry, a failed `Recall` and a cancelled context all land here too |
| `EventUsage` | `Usage *Usage` | Token usage of the last Think |
| `EventWaitInput` | `Wait *WaitInput` | The loop waits on external input: `WaitInput{CellID, Call, Question, Session}` — the four flavours are named by `Session.Kind()` (`WaitTool` a tool's own request, `WaitCallAsk` a pre-execution confirmation, `WaitUtterance` an utterance confirmation, `WaitPause` a pause); `Call` + `Question` say who is asked, not which of the first two it is; the host **saves the Session**, shows the question, and resumes via `agent.Resume(sess, resp)` |
| `EventPaused` | `Wait *WaitInput` | A pause request took effect: `WaitInput{CellID, Session}` (Call zero value, Question empty, `Session.Kind() == WaitPause`) — the host saves the Session and resumes via `agent.Resume(sess, meowire.Response{})` (the same channel every suspension uses) |
| `EventReplace` | `Replace *ReplaceAudit` | Port-swap audit: `ReplaceAudit{CellID, Slot, OldType, NewType}` — the swapped ports named by Go type, not held (the record outlives the swap and must stay serializable; `Replace` returns the previous port to the caller); emitted at the start of the next Stimulate/Resume (the moment the swap takes effect), persistable |
| `EventConfig` | `Config *ConfigAudit` | Config-swap audit: `ConfigAudit{CellID, Old LoopConfig, New LoopConfig}`; emitted at the start of the next Stimulate/Resume after EventReplace, persistable |

`SandboxVerdict{CellID, Call, Ruling Verdict, Reason, Question, Err}`: one record per ruling on
either side of the loop (`Ruling` is tri-state; Deny is the zero value — fail-closed), where `Call`
is the gated tool action and a zero `Call` means the ruling was about this round's text; an ask
ruling produces two records on one chain (ask → resolve — the terminal record
carries the final ruling and the host's response in `Reason`: a denial lands as `[sandbox-denied: ...]`, an approval keeps the reply verbatim).
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
                answerCh <- meowire.Response{Answer: ans}
            case <-time.After(60 * time.Second):
                answerCh <- meowire.Response{Deny: "timeout"}
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
  `agent.Resume(sess, meowire.Response{})` continues without injecting anything (nothing
  was asked, so neither Response field is read); when
  the pause point is before a tool, the current tool is recorded in Session.remaining
  and Resume runs it first
- **`Session` is an opaque value object** (snapshot of round/context/remaining tool calls/
  accumulated output): the host only saves and returns it, reading it through the
  accessors below and never mutating it;
  **it belongs to the cell that suspended it** — `Resume` rejects a handle another cell
  returned (`ErrForeignSession`; replaying it would run A's round on B's organs);
  **single-use** — resuming twice re-executes the remaining tool calls (duplicate side
  effects; host responsibility)
- **`sess.Kind()`**: reports which flavour suspended the loop, which is what decides how
  the Resume `Response` is read (`WaitTool` wants content, an ask wants a ruling, a pause
  wants neither)
- **`sess.RemainingCalls()`**: a clone of
  the calls not yet executed at the suspension point (empty when nothing is left): a
  pause snapshot keeps the whole unexecuted batch (Resume replays it); a WaitInput
  suspension inside a `ParallelActs` batch is empty (the batch fully executed — Resume
  only injects the answer, never replays)
- **Persistence**: `sess.Marshal()` produces versioned JSON bytes (the serialized record carries
  the owning cell, the wait kind and the withheld draft; the version field is only the format number);
  the host
  persists them; after a restart `meowire.UnmarshalSession(data)` restores the handle
  and Resume continues — suspensions and pauses recover across processes; a version
  mismatch or an unknown wait name is rejected (a stale or future handle must not be replayed)
- **No extra round**: Resume continues from the suspended round; the Think that digests
  the response uses the suspended round's quota (`MaxRounds` is not extra-consumed)
- **No budget during the wait**: no Think happens while waiting, the trimmers are not called;
  it runs before the next Think after resume
- **Answer grammar (one typed `Response`; `Session.Kind()` says which field it reads)**:
  a **non-empty `Deny`** refuses, and the reason lands in the shape each flavour owns — the
  call side gets `[sandbox-denied: reason]` feedback, the text side gets that text in place
  of the draft, a tool's own question records the call as failed in `ToolResults.Err` (a
  refusal is a tool failure, not tool output); an **empty `Deny` with a non-empty
  `Answer`** approves — the pending call executes without re-gating, a withheld draft is
  said as generated. The **zero `Response`** declines
  (`[sandbox-denied: declined]`), so not answering fails closed.
  **ask_user** answers are written as the suspended call's structured result in `ToolResults`
  (`ID` = the call's `call_xxx`), visible to the first Think after resume — an empty
  `Answer` is an empty result. An answer is never parsed for a directive inside it: text the
  host put in `Answer` stays text even when it begins with `[denied:`.
  Timeouts are host-controlled (default deny: `Response{Deny: "timeout"}`)
- **One suspension per round**: once a call in the batch has suspended, a later sibling's own
  `WaitInput` is refused before it is delivered, as tool feedback
  (`one wait per round: <name> already waits`) — the snapshot has no second suspension to spend, so it
  is better for the model to read the refusal than for a wait to sit unanswered forever
- **Remaining tools**: when the suspension happens mid-list, Resume first runs the rest
  of the round's tools, then re-enters Think
- **Resume hooks are identical to Stimulate** (`BeforeStimulate` fires as usual; with
  host append semantics `Session.Context` merges naturally with retrieval results);
  `Close` makes Resume yield `ErrCellClosed`; `Session` is an in-memory handle — surviving
  a process restart is what `Marshal()` + `UnmarshalSession()` are for (see Persistence);
  a handle that was never saved is treated as timeout-deny
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

- Every record carries `WireEvent.Version` (**currently v2**, and only v2 onward carries `Seq`/`TS`); a mismatch is rejected, never read as a close enough
  match. Kinds, states and membrane rulings travel **by name**, so reordering an enum in a new
  release cannot silently reinterpret a log written by the last one
- `EventError` identity: a framework sentinel (`ErrMaxRounds`, `ErrForeignSession`, `ErrCellClosed`) comes back as
  the identical value — wrapped text included, so `errors.Is` still holds. Any other error (a host's
  own `rate limited`) returns as the same **text** only, and the event lists `err.identity` in
  `Dropped` — the loss is reported instead of hiding behind a false comparison
- A suspension handle inside `EventWaitInput` / `EventPaused` is embedded in its own serialized
  form, so the `Session` version guard still applies: a stale suspension is rejected by the handle,
  not by a looser event wire
- `Event.CellID` names the producing cell, so a host that writes several agents into one log can
  still tell who spoke; `Dropped` says whether the record arrived complete
- Do **not** `json.Marshal` an `Event`: it writes the `Err` field as `{}` (the text is gone) and
  numbers the enums, all without reporting anything

---

## 7. Step 6: `Close` and composing instances

### 7.1 `Close() error`

Idempotent (CAS): only the call that flips open→closed runs the host `Closer`, and its
error is wrapped as `meow: closer: ...`; the cell itself produces no error, so Close has nothing to
join. After Close, `Stimulate`/`Resume` yield `EventError` whose chain carries `ErrCellClosed`
(`errors.Is` matches it), while `Replace` returns that error directly. The host
should `defer agent.Close()`.

### 7.2 Multi-agent = the host composes instances

**There is no inter-agent layer**: the kernel does not address, deliver, pair replies, keep a
synapse graph, publish capability cards or render a colony-wide view. `Organs` has no slot
addressed to a neighbour and `Prompt` has no inbound track. `test/wiring_free_test.go` guards
that boundary mechanically (a host-side file containing a channel, a goroutine or a receive,
or an extra neighbour-facing field on `Organs`, fails the build).

**Reading the name back**: `agent.ID()` returns the `Organs.ID` declared at assembly — the same fact
every `Event.CellID` carries and every `Session` is checked against. It is the only public way to tell
two instances apart once they are built.

**Flat model**: one `Agent` = one kernel, and the host owns every instance. Sub-agents are
created inside host Effector tools (`New(bp) → Stimulate → consume the stream`), transparent to
the main loop; if A must wait for B, make B a tool of A. One `Blueprint` can `New` many times,
but `Organs.ID` names the instance (events are attributed to it and a Session is checked against
it) and each instance shares its `Organs.Context` slice and `Hooks` closures, so a second agent
overrides them per instance (see [reference-host.md](reference-host.md) Step 7).

### 7.3 Multi-agent state visibility: the host-side trio

> The framework event stream is "visible only to its consumer": the main loop
> never sees sub-agent events (flat model). Unified main/sub-agent state
> visibility is implemented by the host-side trio — **zero framework changes**:
> StreamHub (shared state container) + `WithSink` streaming injection (optional) +
> Hooks/tools (state & plan, required).

**StreamHub — shared state container (keyed by `Organs.ID`)**

```go
type StreamHub struct {
	mu      sync.RWMutex
	streams map[string]chan meowire.Event // streaming output (optional; no channel = silent)
	tasks   map[string]*taskView          // task state (required, written by Hooks)
	plans   map[string]string             // plan tree (written by update_plan tool)
}
```

```go
// taskView is the host's own task projection: the framework keeps no inter-agent task
// lifecycle — the states here are written by the host's own hooks and tools.
type taskView struct {
	State    string
	LastText string
	LastTool string
	Output   string
}
```

The host creates **one instance** and injects the same hub into every agent
(main and sub); the UI reads the hub for a real-time view of all agents.

**① Streaming (optional) — via `WithSink`**

The framework's `EventText` is always the whole segment (each round's `Decision.Text`);
token-level deltas ride the bundled brain's Sink channel — mounted on the context handed to
`Stimulate`, they arrive before the output membrane rules, and the host pushes each delta
into `hub.streams[id]`:

```go
func streamingCtx(ctx context.Context, id string, hub *StreamHub) context.Context {
	ch := hub.channel(id) // no channel injected → nil → silent
	if ch == nil {
		return ctx
	}
	return meowire.WithSink(ctx, func(delta string) {
		select {
		case ch <- meowire.Event{Kind: meowire.EventText, Text: delta}:
		case <-ctx.Done():
		}
	})
}
```

Silent execution (a sub-agent running in the background, no streaming): simply do not wrap
`WithSink` — the deltas have nowhere to go and are never pushed. When `EventText` arrives,
replace what was pushed (stream-and-correct).

**② State sync (required) — written by Hooks callbacks**

The host writes state into the hub in `Organs.Hooks`; the framework invokes
them at fixed points. The id is captured via closure (`AfterAct` may also use
`a.CellID`):

```go
hooksFor := func(id string) *meowire.Hooks {
	return meowire.FullHooks(meowire.Hooks{
		AfterThink: func(ctx context.Context, d *meowire.Decision) error {
			hub.updateTask(id, taskView{State: "thinking", LastText: d.Text})
			return nil
		},
		AfterAct: func(ctx context.Context, a *meowire.Action, e *meowire.Effect, err error) {
			hub.updateTask(a.CellID, taskView{State: "acting", LastTool: a.Call.Name})
		},
		OnCycleEnd: func(ctx context.Context, output string, outcome meowire.CycleOutcome) {
			switch outcome {
			case meowire.OutcomeDone:
				hub.updateTask(id, taskView{State: "done", Output: output})
			case meowire.OutcomeSuspended:
				hub.updateTask(id, taskView{State: "needs-input"})
			default:
				hub.updateTask(id, taskView{State: "failed"})
			}
		},
	})
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

// Hooks.BeforeThink: re-inject p.Plan (pointer; this round's Think reads it)
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
			Brain:  baseBrain, // same parameters (or a different Model per sub-task)
			Act:    sameEffector,
			Hooks:   hooksFor(args.ID), // closures over the same hub
			Closer:  closerStub,
			Sandbox: sandboxStub,
			Budget:  passBudget, // pass-through trimmers: Trimmer + TrimResults + MaxTokens>0
			Mem:     sameMemory, // required port: Recall + Remember (§8 skeleton does the same)
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
	"os"

	meowire "github.com/qyiun666/meowire/api"
)

// ① Brain: parameters, not a port (BaseURL/Key/Model/Stream/Mode — Mode selects the wire, chat by default)

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
	ctx := context.Background()
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-001",
			Brain:   meowire.BrainConfig{Model: "gpt-5.2", Key: os.Getenv("OPENAI_API_KEY")},
			Act:     &effector{registry: toolRegistry},
			Closer:  &closer{},
			Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: injectMemory, OnCycleEnd: persistOutput}),
			Sandbox: &sandbox{},
			Budget:  &meowire.ContextBudget{MaxTokens: 4000, Trimmer: trim, TrimResults: trimResults},
			Mem:     &memory{}, // required port: Recall + Remember
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
2. **Port concurrency safety**: Effector is called concurrently if the same
   Agent is stimulated concurrently (the bundled brain is stateless — safe by
   construction)
3. **Host ports must respect ctx cancellation**; `ask_user` must never block
   inside Effector (framework-level suspension protocol, §6.4) — the host-side
   wait timeout is self-controlled (default deny: `Resume(sess, Response{Deny: "timeout"})`)
4. **`Organs.Context` is a slice**: update it to the latest history via
   `BeforeThink` before each Think (MemHop)
5. **`ErrMaxRounds` is not a bug**: it fires when the last round has pending
   tool calls and the round budget is exhausted; Step-Resume to continue is the intended use
6. **The event stream is an observation mirror**: the host never feeds data
   back into an open iterator; feedback goes through the next `Stimulate`
7. **Streaming UX lives in `WithSink`**: `EventText` is always whole-segment;
   token deltas never enter the event stream and reach the Sink before the
   output membrane rules
8. **Errors take three channels, not one**: a hook's error and an exhausted Think retry
   surface via `EventError` (classify with `errors.Is`, e.g. `meowire.ErrMaxRounds`); an error from
   `Sandbox` never reaches `EventError` — it fails closed as `[sandbox-denied: sandbox error: ...]`
   feedback; an error from the `Effector` lands in `ToolResults.Err` and the loop continues; an error
   from `Memory.Remember` only reaches `Hooks.OnError` and cannot rewrite the round's terminal
   (a failed `Recall` is the one that yields `EventError`)
9. **Pause never interrupts a running tool**: Pause takes effect at gap points;
   interrupt a running tool with ctx cancellation instead (§2.4)
10. **hub lifecycle belongs to the host** (§7.3): a full streaming channel blocks
   the agent loop (Sink forwarding is synchronous); the host must manage
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
| Brain spec (openai-go) | [thinker-openai-go.md](thinker-openai-go.md) | The bundled brain's placement table and protocol reference (`openai-go/v3`) |
| Protocol Mapping Guide | [protocols.md](protocols.md) (zh-CN) | Host-side mapping to MCP / A2A / AGENTS.md / Authority |
| api module context | [api/agent.md](api/agent.md) | Long-term api package context, key decisions, pitfalls |
| Decision loop source | [internal/nerve/loop.go](internal/nerve/loop.go) | Loop orchestration (Think→Act→yield) |
| Integration tests | [test/](test/) | End-to-end behavior (lifecycle, port injection, event sequences) |
