# meowire Host Integration Guide

> Audience: host developers/AIs integrating meowire.
> This document is the authoritative integration contract — all interface
> signatures, field semantics, and event sequences match the source code.
> Module path: `github.com/qyiun666/meowire`. Public package:
> `github.com/qyiun666/meowire/api` (aliased `meowire`).

## 0. The One-Line Model

meowire is a **pure orchestration kernel**: the host implements six ports
(LLM, tools, cleanup, hooks, permission gate, context trimming), and the
framework runs the Think → Act → yield event loop. **The framework does not
manage history, does not manage multi-agent, and provides no default
implementations** — all six ports are required; a missing one is an error.

The host touches exactly three methods: `New` (assembly), `Stimulate` (run one
step), `Close` (shutdown).

## 1. Integration Flow Overview (6 Steps)

```
Implement six ports → Assemble Organs → Set Config → Assemble Blueprint → New() → Stimulate() consume event stream → Close()
```

| Step | What | Key point |
|------|------|-----------|
| 1 | Implement Thinker/Effector/Closer/Hooks/Sandbox/ContextBudget | All six ports required |
| 2 | Assemble the `Organs` struct | Inject ports + fixed context |
| 3 | Set `Config` | Zero values are defaults; nothing must be set explicitly |
| 4 | Assemble `Blueprint{Organs, Config}` and `New(bp)` | Missing port/callback returns an error; incomplete Budget too |
| 5 | `for ev := range agent.Stimulate(ctx, text)` | Consume the event stream |
| 6 | `agent.Close()` | Idempotent; Stimulate after Close returns `ErrCellClosed` |

---

## 2. Step 1: Implement the Six Ports (Host Capabilities)

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
| `Context` | Context slice: host base + tool feedback appended by the framework within the cycle | Host base + framework appends |
| `Bounds` | Execution boundary description (`Sandbox.Bounds()` snapshot, e.g. "only /workspace") | Framework, once per Stimulate |
| `Input` | Current stimulus text (the Stimulate argument) | Framework, per round |
| `State` | Current loop state string (`"thinking"`/`"acting"`/…) | Framework, auto-updated |
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
| `Effect.Result` | Success result text (formatted by the framework as `[tool] result` and appended to Context) |
| `Effect.Err` | Tool-side error text (formatted as `[tool] error: ...`) |
| return error | Execution-layer error (also formatted into feedback) |

**Host responsibilities:**
- Tool registry + dispatcher: route by `Name`, deserialize `Args`, serialize results
- Multi-agent tools live here: `spawn_agent` (New → Stimulate → return result),
  `send_message` (cross-agent messaging, see §7.2) — flat-model convention
- `ask_user`-style tools may **block synchronously** here waiting for human
  input; must monitor `ctx.Done` so cancellation interrupts the wait
- Tool failures can be returned as `Effect{Err: ...}` instead of an error; both
  flow back as feedback and the loop continues (**resistance is feedback, not failure**)

### 2.3 Closer — the cleaner

```go
type Closer interface {
    Close() error
}
```

Releases host resources: LLM clients, HTTP connections, etc. The framework
guarantees `Agent.Close` is idempotent (CAS); repeated calls have no side effects.

### 2.4 Pause / Resume — process-level suspension (Agent-level control, not a port)

```go
agent.Pause()   // request a pause: takes effect at the next gap point
agent.Resume()  // resume: clears the pause flag
```

- **Gap-effective**: a pause request never interrupts an in-flight Think/Act;
  the loop checks it at two gap points (before each Think, before each tool execution)
- A pending pause yields `EventState(StatePaused)`; resuming continues without an extra event
- Pause state is **Agent-level and survives across Stimulate calls**: a Stimulate
  started while paused first suspends at its entry gap point
- `Pause` / `Resume` are **idempotent and concurrency-safe**; no-ops after `Close`
- **`Close` unblocks a pending pause**: an iterator suspended in pause is woken up
  and can finish normally (or be stopped by ctx cancellation) instead of hanging
- A ctx cancellation while paused follows the normal error path
  (`EventState(StateError)` → `EventError`)
- Difference from Step-Resume: Step-Resume abandons the round and restarts
  statelessly; Pause/Resume **keeps the in-cycle state and suspends in place**

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
    OnCycleEnd      func(ctx context.Context, output string)
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
| `OnCycleEnd` | **Exactly once per cycle** (normal, error, and early-consumer-stop paths) | Settlement, persist final output |

**⚠️ `BeforeThink` must replace `p.Context` as a whole (`p.Context = append(p.Context[:0], newCtx...)` or assign a new slice) — it shares the backing array with the loop's accumulated context; appending into it can corrupt the loop context.**

### 2.6 Sandbox — the permission gate (the security red line)

```go
type Sandbox interface {
    Allow(ctx context.Context, a Action) (allowed bool, reason string, err error)
    Bounds() string // execution boundary description (host defined), snapshotted once per Stimulate
}
```

- Invoked before each tool execution; when `allowed=false` the framework
  appends `[denied: reason]` feedback to Context and **the loop continues** (no halt)
- An error from `Allow` denies as `[sandbox error: ...]`
- `Bounds()` returns the execution boundary description; the framework
  snapshots it once per Stimulate and surfaces it to the LLM via `Prompt.Bounds`
  (so the brain perceives its limits, e.g. "only files under /workspace")
- The host implements security policy: tool allowlist/denylist, human
  confirmation, sensitive-operation interception

### 2.7 ContextBudget — the context trimmer

```go
type ContextBudget struct {
    MaxTokens int
    Trimmer   func(ctx []string, max int) []string
}
```

- `Trimmer` is called before each Think to trim Context down to `MaxTokens`
- It is a trimmer, not a hard stop: over budget trims, no error
- Return the input slice unchanged to skip trimming

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
| `Sandbox` | Sandbox | Permission gate | **yes** |
| `Budget` | *ContextBudget | Context trimming | **yes** |
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
}
```

| Field | Semantics | Zero-value default |
|-------|-----------|--------------------|
| `MaxRounds` | Hard round limit; **if the last round still has pending tool calls the loop ends with `ErrMaxRounds`** (those tool results are never re-thought); pair with Step-Resume to prevent infinite loops | 8 |
| `MaxToolOutput` | Tool-feedback truncation length (UTF-8-safe; overlong output gets `[truncated, N bytes total]`) | no truncation |
| `MaxRetries` | Think retry count (retries Think only; tool-failure protection is host-side, in Effector/AfterAct) | no retry |
| `ToolTimeout` | Per-tool execution timeout (each attempt timed independently; timeout-derived errors are not retried and flow back as feedback) | no timeout |
| `ToolMaxRetries` | Tool retry count on effector errors (**executor err only**; `Effect.Err` is never retried — avoids duplicate side effects) | no retry |

---

## 5. Step 4: `New` Assembly Validation

```go
type Blueprint struct {
    Organs Organs   // six ports + fixed context (all required)
    Config Config   // zero values are defaults
}

bp := meowire.Blueprint{Organs: organs, Config: cfg}
agent, err := meowire.New(bp)
```

- **Blueprint is define-once, assemble-many**: the same `bp` can `New` multiple independent Agent instances (flat-model multi-agent)
- `New` validates against the **assembly graph** (blueprint = data-object nodes + slot edges), two levels only:
  - `error` (missing required port / missing hook callback / incomplete Budget): **always blocks**, returns `meow: required port X not injected`
    (X ∈ Think/Act/Closer/Hooks/Sandbox/Budget/H1–H8), multiple findings joined
  - `info` (empty Identity/Tools/Context, default rounds): **never blocks** — inspect via `meowire.Validate(organs, cfg)`
  - No `warn` level: every wiring point is required — a missing point is a missing organ, there is no "half-wired pass"
- After assembly the host calls `Stimulate` / `Pause` / `Resume` / `Close`, and may
  swap ports at runtime via `Replace` and export the capability card via `AgentCard` (below)
- All six ports and eight hook callbacks must be implemented by the host — **no stubs, no defaults, no "minimal runnable" path**; use `meowire.FullHooks(...)` to declare unneeded hooks as explicit no-ops

### 5.1 Dynamic wiring: `Replace` (runtime organ swap)

```go
oldThink, err := agent.Replace(meowire.SlotThink, myOtherLLM) // takes effect at the next Stimulate
```

- Swappable slots: `SlotThink` / `SlotAct` / `SlotSandbox` / `SlotBudget` / `SlotHooks`;
  `Closer` (resource binding) and `PauseGate` (framework wiring) are never swappable
- Semantics: each `Stimulate` snapshots ports into a fresh LoopContext — an **in-flight
  Stimulate is unaffected**; the swap takes effect at the next Stimulate; the previous
  port is returned (host decides whether to shut the old implementation down)
- Concurrency-safe; no-op after `Close`; unknown slot or wrong port type returns an error

### 5.2 Capability card: `AgentCard` (A2A style)

```go
card, _ := meowire.AgentCard(organs) // JSON: name/description/skills
```

Projected from the assembly (`ID`/`Identity`/`Methods`) as a machine-readable capability
declaration; publish it at `/.well-known/agent-card.json` so other agents can discover
this agent. See [docs/protocols.md](protocols.md) §2.

### 5.3 Composite view: `BuildComposite` (static assembly × live synapses, one picture)

```go
colony := meowire.NewDirect(resolver) // host-domain Synapse (plastic synapse graph)
// ...runtime Link/Fire/Reinforce...

text, _ := meowire.RenderComposite(ctx, organs, colony) // ASCII: internal nodes/slots + external agents/synapses
snap, _ := meowire.RenderCompositeJSON(ctx, organs, colony) // JSON: machine-readable snapshot
```

- Internal subgraph: static assembly (12 data nodes + 18 slot edges); external subgraph:
  live synaptic edges (weight + delivery count)
- Weak synapses (weight < 0.3) are flagged `! weak` for periodic `Prune` review
- View unified, data separate: internal assembly and external connections store
  independently, merged only at render time
- Persist `RenderCompositeJSON` snapshots for a unified observability view of the whole colony

---

## 6. Step 5: `Stimulate` — Consuming the Event Stream

`Stimulate(ctx context.Context, text string) iter.Seq[Event]`; consume with
`for ev := range agent.Stimulate(...)`.

### 6.1 Event sequences

**Single round without tools (4 events):**

```
EventState(thinking) → EventText → [EventUsage(optional)] → EventState(done) → EventDone(accumulated output)
```

**With tool calls (inserted per round; may loop over multiple rounds; with a
configured Sandbox, an audit event is inserted before each tool execution):**

```
EventState(thinking) → EventText → [EventUsage] → EventState(acting)
  → (EventToolCall → [EventSandbox] → EventToolResult) × N → back to EventState(thinking) → …
```

**Pause path (inserted at gap points; the original sequence resumes after Resume):**

```
… → EventState(paused) → (blocks until Resume / ctx cancellation) → continue the original sequence
```

**Error path:**

```
EventState(error) → EventError(Err)
```

**After Close:** Stimulate yields `EventError(ErrCellClosed)` directly.

### 6.2 `Event` fields (only the fields for the Kind are set; the rest are zero values)

| Kind | Active field | Content |
|------|--------------|---------|
| `EventText` | `Text` | Whole-segment LLM text output |
| `EventToolCall` | `ToolCall *ToolCall` | Tool the LLM decided to call |
| `EventToolResult` | `Effect *Effect` | Tool execution result (includes Sandbox denials: `Effect.Err = "[denied: reason]"`) |
| `EventSandbox` | `Verdict *SandboxVerdict` | Sandbox decision audit record (allowed/denied, tool, policy reason, evaluation error); not emitted when no sandbox is wired |
| `EventState` | `State LoopState` | Loop state (idle/thinking/acting/paused/done/error) |
| `EventDone` | `Output` | Accumulated text output of the whole cycle |
| `EventError` | `Err` | Unrecoverable error (incl. `ErrMaxRounds`, `ErrCellClosed`) |
| `EventUsage` | `Usage *Usage` | Token usage of the last Think |

`SandboxVerdict{CellID, Call, Allowed, Reason, Err}`: one record per decision of a
configured sandbox, emitted before the tool runs; persisting the event stream yields
the action-level audit log (who, on whose behalf, when, what, why permitted). See
[docs/protocols.md](protocols.md) §4 Authority.

### 6.3 Two semantics the host must know

1. **Early stop (basis of Step-Resume)**: breaking/returning in `for range`
   abandons the round — tools at or after the stop point **do not** execute,
   and all state accumulated in this round is discarded. The host may
   interrupt the stream for human approval/async work, then `Stimulate` again.
2. **Stateless step**: each `Stimulate` is one stateless step with no state
   across calls. The host keeps history itself and re-injects it via
   `Organs.Context` (or `Hooks.BeforeThink`).

---

## 7. Step 6: `Close` and Optional Extensions

### 7.1 `Close() error`

Idempotent (CAS); closes the cell then the host Closer; errors are joined
with `errors.Join`. Stimulate after Close returns `ErrCellClosed`. The host
should `defer agent.Close()`.

### 7.2 Optional extensions

**Memory (host-built backend)**: `internal/memory` is a reference contract the
framework does **not** consume:

```go
type Record struct { Key, CellID, Kind string; Content []byte; Created int64 }  // Created is Unix seconds
type Query  struct { CellID, Prefix, Kind string; Limit int }                  // empty CellID matches all; Limit<=0 unlimited
type Memory interface {
    Save(ctx, Record) error
    Recall(ctx, Query) ([]Record, error)
    Forget(ctx, key, cellID string) error
}
```

The host implements a backend and injects it into the loop via
`Organs.Context` + `Hooks.BeforeThink` (MemHop pattern).

**Synapse (inter-agent messaging reference; a plastic synapse graph since 1.1.1)**:

```go
type Edge struct {
    From   string
    To     string
    Weight float64 // synaptic strength (host learning rules read/write)
    Fired  int64   // cumulative successful deliveries (host statistics)
}

type Synapse interface {
    Link(ctx, from, to string, weight float64) error // synaptogenesis (idempotent overwrite, clamp ≥ 0)
    Unlink(ctx, from, to string) error               // synapse elimination (missing → ErrNotLinked)
    Reinforce(ctx, from, to string, delta float64) error // LTP/LTD (result clamp ≥ 0)
    Fire(ctx, sig Signal) error                      // signal delivery (success increments Fired)
    Edges(ctx, from string) ([]Edge, error)          // out-edge / whole-graph snapshot (persistence primitive)
}
```

- `SignalKind`: `KindStimulus` (host sends a task) / `KindResponse` (agent reply)
  / `KindNotice` (side-channel notice, no DecisionLoop)
- Real routing (channel/HTTP/Redis/gRPC) is host-chosen; the host tool
  `send_message` fires via `synapse.Fire`; resistance (busy target, unknown
  agent) flows back as `EventToolResult` feedback, never a hard stop
- Synapse errors (`ErrNoTarget`/`ErrNotLinked`/`ErrTargetBusy`) are re-exported at the api layer; `Edge` re-exported too
- **Persistence round-trip**: export the whole graph via `Edges(ctx, "")` at
  runtime, serialize it host-side; on restart deserialize and inject via
  `NewDirect(resolver, restored...)`. Learning rules (Hebbian/STDP) are
  host-side — the framework stores state, never decides

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
		OnCycleEnd: func(ctx context.Context, output string) {
			hub.updateTask(id, TaskStatus{State: "done", Output: output})
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
			Budget:  &meowire.ContextBudget{},
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
	msgs := buildMessages(p) // System/Identity/Methods/Tools/Context/Bounds/Input/State/Plan → messages
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
			Budget:  &meowire.ContextBudget{MaxTokens: 4000, Trimmer: trim},
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
3. **Host ports must respect ctx cancellation**, especially in blocking
   `ask_user` scenarios (otherwise cancellation cannot interrupt)
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
| README | [README.md](../README.md) | English quick start + concept overview |
| README (zh-CN) | [README.zh-CN.md](../README.zh-CN.md) | Chinese quick start + concept overview |
| Host Integration Guide (zh-CN) | [host-integration.md](host-integration.md) | Chinese integration contract |
| api module context | [api/agent.md](../api/agent.md) | Long-term api package context, key decisions, pitfalls |
| Decision loop source | [internal/nerve/loop.go](../internal/nerve/loop.go) | Loop orchestration (Think→Act→yield) |
| Integration tests | [test/](../test/) | End-to-end behavior (lifecycle, port injection, event sequences) |
