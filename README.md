# Meowire

Bionic agent harness base for Go — **pure wiring, zero default implementations.**

Meowire is a minimal decision-loop kernel for building agent hosts. It wires the orchestration
(Think → Act → yield events) and leaves everything else to you: the LLM, the tools, the memory,
the security policy. No framework opinion about your stack — just a clean, dependency-free loop
you can rely on.

> Requires Go 1.27+ (uses `iter.Seq`).

## Why Meowire

- **You own the intelligence.** Meowire provides no LLM adapter, no tool framework, no memory
  backend — it injects six host ports and expects you to implement them. The framework never
  hides what your agent actually does.
- **Zero dependencies.** Standard library only. No transitive dependency tree to audit.
- **Small and readable.** ~1.9k lines of source. The entire loop is one file
  (`internal/nerve/loop.go`, ~530 lines with tests).
- **Sealed internals.** All implementation lives under `internal/` — the Go compiler guarantees
  the only importable surface is the `api/` package (`New` / `Stimulate` / `Close` + contract types).

## Features

- **Think→Act decision loop** with per-round retry and a hard round limit
- **Typed event stream** — `Stimulate` returns `iter.Seq[Event]`; the host observes
  `EventText`, `EventToolCall`, `EventToolResult`, `EventState`, `EventDone`, `EventError`, `EventUsage`,
  `EventSandbox` (action-level audit record), `EventWaitInput` (loop suspended waiting for external input),
  `EventPaused` (pause request honored — snapshot + resume handle), `EventReplace` (port-swap audit record),
  `EventConfig` (config-swap audit record)
- **Action-level audit trail** — every sandbox decision (allowed or denied) yields an
  `EventSandbox` verdict (tool, reason, error); persist the event stream for a complete
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
  with reference learning rules `Hebbian`/`STDP`/`Prune` (host-side; framework stores state, never
  decides when to learn); persistence round-trip via `Edges` export + `NewDirect` restore
- **Unified composite view** — `BuildComposite`/`RenderComposite`/`RenderCompositeJSON` merge the
  static assembly subgraph with the live synapse graph into one picture (view unified, data separate)
- **Six host-injected ports, all organs required** (no stubs, no optional
  wiring): `Thinker` (LLM), `Effector` (tools), `Closer` (cleanup), `Hooks`
  (interception — all eight callbacks H1–H8 required: explicit no-op, not
  absence), `Sandbox` (permission membrane), `ContextBudget` (token
  regulator over both accumulating tracks — needs Trimmer, TrimResults and MaxTokens)
- **Step-Resume** — each `Stimulate` is one stateless step; stop the iterator, do host-side work
  (async tool, manual takeover, `ErrMaxRounds` continuation), then `Stimulate` again. Tool-requested
  input (`ask_user`) is not done this way — see Suspension-resume below (the only form)
- **Unified suspension-resume (v1.3.2 onward)** — one snapshot + resume path for all suspension kinds:
  - **ask_user**: a tool returns `Effect{WaitInput: question}` and the loop yields `EventState(StateWaiting)` + `EventWaitInput` (tool, question, opaque `Session`) and ends the iterator normally — no blocking, no extra round, no budget during the wait. `Agent.Resume(ctx, sess, response)` continues: the response enters the loop as the pending tool's structured result (`Prompt.ToolResults` entry, ID preserved), remaining tools run first, then the loop resumes from the suspended round.
  - **Pause**: `Agent.Pause()` is honored at gap points (before each Think / tool execution); the loop yields `EventState(StatePaused)` + `EventPaused` (Session snapshot) and ends the iterator normally — `Agent.Resume(ctx, sess, "")` continues (no pending tool to inject). A pause before a tool keeps that tool in the snapshot, so Resume runs it first.
  - **Sandbox ask**: a tri-state ruling (`Sandbox.Allow` returning `VerdictAsk`) yields the same StateWaiting + EventWaitInput pair (the question is the ruling's reason); resolution follows one shared response grammar — "" denies (`[sandbox-denied: declined]`), a `[denied:` prefix denies with that text (the feedback lands in the canonical `[sandbox-denied: ...]` form), any other response approves and runs the pending call without re-gating.
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
  concurrency-safe Effector. `Session.RemainingCalls()` exposes the pending calls of a
  suspension (empty when nothing is left to replay)
- **Tri-state sandbox rulings & reflection primitives** — `Sandbox.Allow` returns a
  `Verdict`: Deny (zero value, fail-closed), Allow, or Ask; an ask suspends via the same
  suspension-resume protocol as ask_user and executes only after the host approves
  (one audit chain closes with a terminal resolve record). `Hooks.OnCycleEnd(ctx, output,
  outcome)` classifies how every cycle ended (`CycleOutcome`: Done/Suspended/MaxRounds/
  Error/Aborted); `BeforeStimulate` may write a turn-scoped self-review note onto
  `Prompt.Reflection`, carried onto every Thinker prompt of the cycle
- **Host-managed history** (MemHop pattern) — context accumulation and memory injection are yours
- **Flat multi-agent model** — sub-agents and inter-agent messaging are host tools
  (`spawn_agent` / `send_message`), never framework-level nesting
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
      ├── internal/nerve    decision loop, ports, hooks, events, guards
      ├── internal/synapse  inter-agent connection & delivery contract (host reference)
      └── internal/memory   memory CRUD contract (host reference, not consumed by the framework)
```

| Concept | Where | Role |
|---|---|---|
| `Agent` / `New` / `Stimulate` / `Close` | `api/` | Facade: the entire public surface |
| `DecisionLoop.Cycle` | `internal/nerve/loop.go` | Pure orchestration: Think → Act → yield |
| `Cell` | `internal/cell/cell.go` | Minimal kernel: ID + ports + loop |
| `Thinker` / `Effector` / `Closer` | ports | Host-provided capabilities |
| `Hooks` | ports | BeforeStimulate / AfterStimulate / BeforeThink / AfterThink / BeforeAct / AfterAct / OnError / OnCycleEnd |
| `Sandbox` | guard | Tool permission gate, invoked before each Act; `Bounds()` surfaces the execution boundary to the Thinker via `Prompt.Bounds` |
| `ContextBudget` | guard | Trims the text `Context` and the structured `ToolResults` before each Think, same limit |
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

For the full host-side integration contract — the six ports, field-by-field
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

// sandbox implements meowire.Sandbox — the tool permission gate.
type sandbox struct{}

func (sandbox) Allow(ctx context.Context, a meowire.Action) (meowire.Verdict, string, error) {
	return meowire.VerdictAllow, "", nil
}

func (sandbox) Bounds() string { return "read-only /workspace" }

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
- **Sandbox ask**: `Sandbox.Allow` returning `VerdictAsk` → the same `EventState(StateWaiting)` +
  `EventWaitInput` pair (question from the ruling's reason); the response grammar matches ask_user.
- `Agent.Resume` clears a stale pause request automatically; `Agent.Unpause()` only backs out a
  pause request that has not taken effect yet.
- The Session is single-use: resuming it twice re-executes the remaining tool calls (host
  responsibility). This unified model replaces the old blocking PauseGate wait (v1.3.2 breaking).

### Round limits

`Config.MaxRounds` (default 8) is a hard cap. If the last round still has pending tool calls,
the loop ends with `ErrMaxRounds` — the tool results from that round were never re-thought.
Use Step-Resume to continue from where the loop stopped.

### Memory is host-managed (MemHop)

The framework never stores history. You keep the conversation context yourself and inject it via
`Organs.Context` (or per-round via `Hooks.BeforeThink`). `internal/memory` provides a reference
`Memory` contract (`Save` / `Recall` / `Forget`) for your backend — the framework does not consume it.

## Multi-agent

Meowire is a **flat model**: one `Agent` = one kernel. The host owns all instances.

- Sub-agents: a `spawn_agent` host tool that returns results as `EventToolResult` feedback
- Inter-agent messaging: a `send_message` host tool (reference routing semantics in
  `internal/synapse`); resistance (busy target, unknown agent) becomes feedback, never a hard stop
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
