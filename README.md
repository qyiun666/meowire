# Meowire

Bionic agent harness base for Go — **pure wiring, zero default implementations.**

Meowire is a minimal decision-loop kernel for building agent hosts. It wires the orchestration
(Think → Act → yield events) and leaves everything else to you: the LLM, the tools, the memory,
the security policy. No framework opinion about your stack — just a clean, dependency-free loop
you can rely on.

> Requires Go 1.27+ (uses `iter.Seq`).

## Why Meowire

- **You own the intelligence.** Meowire provides no LLM adapter, no tool framework, no memory
  backend — it injects seven host ports and expects you to implement them. The framework never
  hides what your agent actually does.
- **Zero dependencies.** Standard library only. No transitive dependency tree to audit.
- **Small and readable.** ~3.5k lines of Go. The decision loop reads as one concern
  per file (`internal/nerve/`: loop, gate, pause, retry, feedback, parallel).
- **Sealed internals.** All implementation lives under `internal/` — the Go compiler guarantees
  the only importable surface is the `api/` package (`New` / `Stimulate` / `Close` + contract types).

## Features

- **Think→Act decision loop** with per-round retry and a hard round limit
- **Typed event stream** — `Stimulate` returns `iter.Seq[Event]`; the host observes
  `EventText`, `EventToolCall`, `EventToolResult`, `EventState`, `EventDone`, `EventError`, `EventUsage`,
  `EventSandbox` (membrane ruling audit record, either side of the loop), `EventWaitInput` (loop suspended waiting for external input),
  `EventPaused` (pause request honored — snapshot + resume handle), `EventReplace` (port-swap audit record),
  `EventConfig` (config-swap audit record)
- **Membrane audit trail** — every ruling the `Sandbox` hands back (allowed, denied or asked,
  on either side of the loop) yields one `EventSandbox` record (the gated tool, or a zero tool for a
  text ruling; plus reason and error); persist the event stream for a complete
  "who/what/why was permitted" audit, per the Authority security model
- **Wiring graph inspection** — `Connectome`/`Validate`/`RenderDiagram`/`RenderJSON` treat the
  assembly as a graph (data-object nodes + slot edges) and render it for humans or machines
- **Dynamic wiring (synaptic plasticity)** — `Agent.Replace(slot, port)` swaps
  `Think`/`Act`/`Sandbox`/`Budget`/`Hooks` at runtime; takes effect at the next `Stimulate`,
  an in-flight `Stimulate` keeps the ports it started with; every successful swap is
  audited as `EventReplace` at the start of the next Stimulate/Resume
- **Runtime config updates** — `Agent.UpdateConfig(cfg)` / `Agent.GetConfig()` tune
  `MaxRounds` and the other scalar limits hot, without rebuilding the agent
- **Agent Card (A2A-ready)** — `AgentCard(Organs)` renders the assembly as a machine-readable
  capability card (JSON); publish it at `/.well-known/agent-card.json` for agent discovery
- **A2A-style task states** — `Signal.Status` carries the six task lifecycle states
  (submitted/working/needs-input/completed/failed/cancelled) for end-to-end inter-agent tracking
- **Plastic synapse graph** — `Synapse` five-method contract (Link-with-weight/Unlink/Reinforce/Edges)
  with reference learning rules `Hebbian`/`STDP`/`STDPFrom`/`Prune` (host-side; framework stores state,
  never decides when to learn); each edge also carries when it last conducted, so `STDPFrom` pairs
  spikes from graph state alone, and a host-injected `Floor` makes a connection able to exist yet
  refuse traffic (`ErrWeakSynapse`); persistence round-trip via `Edges` export + `NewDirect` restore
- **Colony wiring with zero host plumbing** — `Resolve(agents...)` maps every agent ID to the inbox
  its cell already owns (capacity `InboxCapacity`), so a synapse built over it delivers to the right
  neuron with no host channel map; each cell drains its inbox into `Prompt.Stimuli` at the Think
  gap, and a `KindNotice` naming a tool withdraws that tool for the round (`Prompt.Inhibit`) —
  no consumer pump, and an in-flight Act is never preempted. The card projection doubles as a
  routing index: `NewSkillIndex(agents...)` answers "who can do X" and `FanOut` delivers to each
  of them, reporting per target
- **Agent-to-agent tasks are primitives, not host code** — a tool delegates by returning
  `Effect{Send: &Signal{To: …}}`: the cell mints the id, stamps `submitted`, suspends the call, and
  the serving cell answers at its own terminal with the state `TaskOutcome` derives (six states, one
  writer each). The answer is paired back into `agent.Resumptions()` — the framework never resumes
  on its own; the host continues with `Resume` and retires the pairing with `Ack`
- **Unified composite view** — `BuildComposite`/`RenderComposite`/`RenderCompositeJSON` merge the
  static assembly subgraph with the live synapse graph into one picture (view unified, data separate)
- **Seven host-injected ports, all organs required** (no stubs, no optional
  port wiring — the one opt-in is `Organs.Colony`, the delivery organ of a
  multi-agent wiring): `Thinker` (LLM), `Effector` (tools), `Closer` (cleanup), `Hooks`
  (interception — all eight callbacks H1–H8 required: explicit no-op, not
  absence), `Sandbox` (permission membrane), `ContextBudget` (token
  regulator over both accumulating tracks — needs Trimmer, TrimResults and MaxTokens),
  `Memory` (experience port — `Recall` before each Think, `Remember` once per invocation)
- **Several organs behind one port** — `GuardStack` / `FallbackThinker` / `FallbackEffector` compose
  implementations into the one organ the loop sees and return **the port type itself**, so a composed
  organ wires like a plain one and the blueprint gained nothing
- **Organs can be brought up before they are used** — any port may declare `Bootable`; `New` boots
  each declaring organ once, in `PortOrder()` (the sequence the blueprint implies, not a list written
  beside it), and `Replace` boots before a swap commits. A failing boot aborts and releases what the
  attempt opened through the host `Closer` — the framework still closes nothing
- **Step-Resume** — each `Stimulate` is one stateless step; stop the iterator, do host-side work
  (async tool, manual takeover, `ErrMaxRounds` continuation), then `Stimulate` again. Tool-requested
  input (`ask_user`) is not done this way — see Suspension-resume below (the only form)
- **Unified suspension-resume (v1.3.2 onward)** — one snapshot + resume path for all suspension kinds:
  - **ask_user**: a tool returns `Effect{WaitInput: question}` and the loop yields `EventState(StateWaiting)` + `EventWaitInput` (tool, question, opaque `Session`) and ends the iterator normally — no blocking, no extra round, no budget during the wait. `Agent.Resume(ctx, sess, response)` continues: the response enters the loop as the pending tool's structured result (`Prompt.ToolResults` entry, ID preserved), remaining tools run first, then the loop resumes from the suspended round.
  - **Pause**: `Agent.Pause()` is honored at gap points (before each Think / tool execution); the loop yields `EventState(StatePaused)` + `EventPaused` (Session snapshot) and ends the iterator normally — `Agent.Resume(ctx, sess, "")` continues (no pending tool to inject). A pause before a tool keeps that tool in the snapshot, so Resume runs it first.
  - **Membrane ask**: a tri-state ruling — `Sandbox.Allow` before a call runs, or `Sandbox.Emit` before a round's text is heard — yields the same StateWaiting + EventWaitInput pair (the question is the ruling's reason; an `Emit` ask withholds the draft inside the `Session`). Resolution follows one shared response grammar — "" denies (`[sandbox-denied: declined]`), a `[denied:` prefix denies with that text (the feedback lands in the canonical `[sandbox-denied: ...]` form), any other response approves: the pending call runs without re-gating, or the withheld draft is said as generated without another Think.
  - **Persistent**: `Session.Marshal()` / `UnmarshalSession` give versioned JSON persistence — a suspended or paused loop survives process restarts (alignment with mainstream checkpoint/resume).
  Timeouts are host-controlled (default deny); replaces the old host-side synchronous block
- **Structured tool feedback** — tool results flow back as `Prompt.ToolResults`
  (`ToolResult{ID, Name, Result, Err}`, single track; `call_xxx` IDs preserved);
  rendering (tool-role messages, `[tool_call_id=xxx]` markers, plain text) is the
  host Thinker's decision — the text track (`Context`) keeps host base + sandbox denials
- **Per-tool timeout & retry** — `Config.ToolTimeout` bounds each tool execution;
  `ToolMaxRetries` retries effector errors (business errors in `Effect.Err` are never retried)
- **Parallel tool batches (v1.3.3, opt-in)** — `Config.ParallelActs` executes a round's
  multiple independent tool calls concurrently (serial gating → parallel Act → serial
  feedback in call order); events and hooks stay serial. Off by default; requires a
  concurrency-safe Effector. `MaxParallelActs` (v1.3.8) caps how many calls of a batch run at
  once. `Session.RemainingCalls()` exposes the pending calls of a
  suspension (empty when nothing is left to replay)
- **The stream is journalable** (v1.3.8) — `EncodeEvent`/`DecodeEvent` write one versioned JSON
  record per event, carry enums by name, restore framework errors by identity, and name anything
  that could not cross in the event's `Dropped` field; every event carries the `CellID` of the cell
  that produced it, so one log can hold a whole colony
- **Tri-state rulings on both sides & reflection primitives** — `Sandbox.Allow` (before a tool
  runs) and `Sandbox.Emit` (before a round's text is heard) each return a
  `Verdict`: Deny (zero value, fail-closed), Allow, or Ask; an ask suspends via the same
  suspension-resume protocol as ask_user and takes effect only after the host approves
  (one audit chain closes with a terminal resolve record). `Hooks.OnCycleEnd(ctx, output,
  outcome)` classifies how every cycle ended (`CycleOutcome`: Done/Suspended/MaxRounds/
  Error/Aborted); `BeforeStimulate` may write a turn-scoped self-review note onto
  `Prompt.Reflection`, carried onto every Thinker prompt of the cycle
- **Host-managed history** (MemHop pattern) — context accumulation and memory injection are yours
- **Flat multi-agent model** — sub-agents stay host tools (`spawn_agent`), while
  inter-agent delegation is a framework primitive (`Effect.Send` + `Resumptions`); never
  framework-level nesting
- **Resistance is feedback, not failure** — denied tools, tool errors, and busy targets flow back
  into the loop as `EventToolResult` feedback; the loop continues

## Upgrading to v1.2.0

- **Every wiring point is now required** — hooks H1–H8 must all be set
  (explicit no-op where no behavior is wanted); a missing callback fails
  `New`. `Sandbox` and `ContextBudget` were already required; now the loop
  never tolerates nil — `Bounds()` snapshot, `EventSandbox` audit verdicts
  and `Budget.Trimmer` runs are unconditional.
- **`Blueprint.Strict` removed** — with no warn level left there is nothing
  to promote; drop the field from `Blueprint` literals.
- **`ContextBudget` completeness enforced** — nil Trimmer or MaxTokens <= 0
  fails assembly (a budget that does not trim is not a budget).
- **`Replace` rejects nil/incomplete ports** — swapped organs must be
  complete.
- **`Sandbox` requires `Bounds() string`** — return the execution boundary
  description; the framework snapshots it once per `Stimulate` and surfaces
  it read-only to hooks and the Thinker via `Prompt.Bounds`:
  ```go
  func (s *MySandbox) Bounds() string { return "read-only /workspace" }
  ```
- **Missing-port errors are `errors.Join`-aggregated** — match with
  `errors.Is` / `strings.Contains`, never exact string equality.

## Architecture

```
meowire (module root)
  └── api/            facade + composition root — the sole public surface
      ├── internal/cell     agent kernel (ID + ports + DecisionLoop)
      ├── internal/nerve    decision loop, ports (incl. memory), hooks, events, guards
      └── internal/synapse  inter-agent connection & delivery contract (host reference)
```

| Concept | Where | Role |
|---|---|---|
| `Agent` / `New` / `Stimulate` / `Close` | `api/` | Facade: the entire public surface |
| `DecisionLoop.Cycle` | `internal/nerve/loop.go` | Pure orchestration: Think → Act → yield |
| `Cell` | `internal/cell/cell.go` | Minimal kernel: ID + ports + loop |
| `Thinker` / `Effector` / `Closer` | ports | Host-provided capabilities |
| `Hooks` | ports | BeforeStimulate / AfterStimulate / BeforeThink / AfterThink / BeforeAct / AfterAct / OnError / OnCycleEnd |
| `Sandbox` | guard | Permission membrane on both sides: `Allow` before each Act, `Emit` before a round's text reaches anyone; `Bounds()` surfaces the execution boundary to the Thinker via `Prompt.Bounds` |
| `ContextBudget` | guard | Trims the text `Context` and the structured `ToolResults` before each Think, same limit |
| `Memory` | port | Recalls this round's records into `Prompt.Memories`; takes the finished cycle's facts back |
| `Event` | events | Typed observation mirror of the loop |
| `Synapse` / `Memory` | internal | Standalone reference contracts for hosts |

## Installation

```sh
go get github.com/qyiun666/meowire@latest
```

Import the facade package — the sole public surface:

```go
import meowire "github.com/qyiun666/meowire/api"
```

For the full host-side integration contract — the seven ports, field-by-field
semantics, the event stream, and the pitfalls — see the
[Host Integration Guide](host-integration.en.md).

## Quick Start

```go
package main

import (
	"context"
	"fmt"

	meowire "github.com/qyiun666/meowire/api"
)

// thinker implements meowire.Thinker — the LLM port.
type thinker struct{}

func (thinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	return &meowire.Decision{Text: "Hello from meowire!"}, nil
}

// effector implements meowire.Effector — the tool-execution port.
type effector struct{}

func (effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
	return &meowire.Effect{Result: "ran " + a.Call.Name}, nil
}

// closer implements meowire.Closer — host resource cleanup.
type closer struct{}

func (closer) Close() error { return nil }

// sandbox implements meowire.Sandbox — the membrane on both sides of the loop.
type sandbox struct{}

func (sandbox) Allow(ctx context.Context, a meowire.Action) (meowire.Verdict, string, error) {
	return meowire.VerdictAllow, "", nil
}

func (sandbox) Emit(context.Context, meowire.Utterance) (meowire.Verdict, string, error) {
	return meowire.VerdictAllow, "", nil
}

func (sandbox) Bounds() string { return "read-only /workspace" }

// memory implements meowire.Memory — the experience port.
type memory struct{}

func (memory) Recall(context.Context, meowire.MemoryQuery) ([]meowire.Record, error) {
	return nil, nil
}

func (memory) Remember(context.Context, meowire.CycleFacts) error { return nil }

func main() {
	// Blueprint: define once, New many times (flat-model multi-agent)
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			Think:   thinker{},
			Act:     effector{},
			Closer:  closer{},
			Hooks:   meowire.FullHooks(meowire.Hooks{}), // all eight callbacks, explicit no-ops
			Sandbox: sandbox{},
			Budget: &meowire.ContextBudget{
				MaxTokens:   8192,
				Trimmer:     func(c []string, max int) []string { return c },
				TrimResults: func(rs []meowire.ToolResult, max int) []meowire.ToolResult { return rs },
			},
			Mem:     memory{},
		},
		Config: meowire.Config{},
	}
	agent, err := meowire.New(bp)
	if err != nil {
		panic(err)
	}
	defer agent.Close()

	for ev := range agent.Stimulate(context.Background(), "hello") {
		switch ev.Kind {
		case meowire.EventText:
			fmt.Println(ev.Text)
		case meowire.EventToolCall:
			fmt.Printf("[tool] %s(%s)\n", ev.ToolCall.Name, ev.ToolCall.Args)
		case meowire.EventDone:
			fmt.Println("[done]")
		case meowire.EventError:
			fmt.Println("[error]", ev.Err)
		}
	}
}
```

## Core Concepts

### The event stream is an observation mirror

`Stimulate` runs one step of the loop and returns an `iter.Seq[Event]`. Tool execution happens
*inside* the loop via your `Effector`; results enter the next round's `Prompt.ToolResults`
(structured track). The host observes (`EventToolCall` / `EventToolResult`) but never feeds data back
into an open iterator.

Stopping consumption (yield returns false) **abandons the round**: tools at or after the stop
point do **not** execute, and all state accumulated in this round is discarded.
`OnCycleEnd` is guaranteed to run exactly once per `Cycle` — on success, on error, and on abort — and reports how it ended via its `outcome CycleOutcome` parameter (Done / Suspended / MaxRounds / Error / Aborted; zero reserved for an iterator abandoned mid-cycle → Aborted).

### Step-Resume (host-driven takeover)

Stop the iterator, do host-side work (async tool, manual approval, external service), save your own
progress, then call `Stimulate` again. Each `Stimulate` is a stateless step — this is the way to
implement host-driven takeover, long-running tasks, and retries. Tool-requested input (`ask_user`)
is **not** done this way: a tool returns `Effect{WaitInput: question}` and the loop suspends with an
opaque `Session` — resume it via `Agent.Resume(ctx, sess, response)` (see Suspension-resume above).
The two paths are mutually exclusive; a break-based `ask_user` would lose the suspended context
(the `Session` is opaque and cannot be rebuilt by hand).

### Unified suspension-resume (v1.3.2)

All three suspension kinds — tool-requested input (ask_user), a sandbox ask (`VerdictAsk`), and host-requested pause — share one
mechanism: the loop yields a suspension event carrying an opaque `Session` snapshot and ends the
iterator normally; the host saves the Session (optionally persisting it via `Session.Marshal()` /
`UnmarshalSession` for cross-process recovery), then calls `Agent.Resume(ctx, sess, response)` to
continue from the suspended point — no extra round, no budget during the wait.

- **ask_user**: `Effect{WaitInput: question}` → `EventState(StateWaiting)` + `EventWaitInput`;
  the response is injected as the pending tool's structured result.
- **Pause**: `Agent.Pause()` honored at gap points → `EventState(StatePaused)` + `EventPaused`;
  `Resume(sess, "")` continues without injecting anything (no pending tool). A pause before a tool
  keeps that tool (and the calls after it) in `Session.remaining`, so Resume runs them first.
- **Membrane ask**: `Sandbox.Allow` or `Sandbox.Emit` returning `VerdictAsk` → the same
  `EventState(StateWaiting)` + `EventWaitInput` pair (question from the ruling's reason, and an
  `Emit` ask carries no `Call`); the response grammar matches ask_user.
- `Agent.Resume` clears a stale pause request automatically; `Agent.Unpause()` only backs out a
  pause request that has not taken effect yet.
- The Session is single-use: resuming it twice re-executes the remaining tool calls (host
  responsibility). This unified model replaces the old blocking PauseGate wait (v1.3.2 breaking).

### Round limits

`Config.MaxRounds` (default 8) is a hard cap. If the last round still has pending tool calls,
the loop ends with `ErrMaxRounds` — the tool results from that round were never re-thought.
Use Step-Resume to continue from where the loop stopped.

### Memory: the framework owns the timepoints, you own the store

The framework never stores anything. The `Memory` port decides *when* experience moves: `Recall`
before every Think (into `Prompt.Memories`, replaced wholesale per round), `Remember` once per
invocation at its terminal (carrying `CycleFacts`). What is stored, how it is retrieved, and when
it is deleted stays yours (MemHop). The text track is unchanged: `Organs.Context` and
`Hooks.BeforeThink` still feed `Prompt.Context`.

## Multi-agent

Meowire is a **flat model**: one `Agent` = one kernel. The host owns all instances.

- Sub-agents: a `spawn_agent` host tool that returns results as `EventToolResult` feedback
- Inter-agent tasks: a tool returns `Effect{Send: &Signal{To: …}}` and the framework delivers,
  answers at the serving cell's terminal and pairs the reply into `agent.Resumptions()`;
  resistance (busy target, unknown agent) becomes feedback, never a hard stop
  (`Resolve(agents...)` builds the routing table; `internal/synapse` is the reference graph)
- Capability discovery: `AgentCard` renders the A2A-style card; `Signal.Status` (A2A six-state
  task lifecycle) tracks each inter-agent task end to end

## Development

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

## License

[MIT](LICENSE)

## Links

| | |
|---|---|
| MeowAgent | [github.com/meowagent/meowagent](https://github.com/meowagent/meowagent) |
| MemHop | [github.com/qyiun666/memhop](https://github.com/qyiun666/memhop) |
| MeowDesk | [github.com/qyiun666/MeowDesk](https://github.com/qyiun666/MeowDesk) |
| Website | [qyiun666.github.io/meowagent.github.io](https://qyiun666.github.io/meowagent.github.io/) |
| Host Integration Guide | [host-integration.en.md](host-integration.en.md) |
| Reference Host (zh-CN) | [reference-host.md](reference-host.md) — step-by-step runnable AI host |
| Protocol Mapping Guide | [protocols.md](protocols.md) — MCP / A2A / AGENTS.md / Authority |
| Email | qyiun666@163.com |
