# Meowire

Bionic agent harness base for Go — **pure wiring, zero default implementations.**

Meowire is a minimal decision-loop kernel for building agent hosts. It wires the orchestration
(Think → Act → yield events) and leaves everything else to you: the LLM, the tools, the memory,
the security policy. No framework opinion about your stack — just a clean, dependency-free loop
you can rely on.

> Requires Go 1.26+ (uses `iter.Seq`).

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
  `EventSandbox` (action-level audit record)
- **Action-level audit trail** — every sandbox decision (allowed or denied) yields an
  `EventSandbox` verdict (tool, reason, error); persist the event stream for a complete
  "who/what/why was permitted" audit, per the Authority security model
- **Wiring graph inspection** — `Connectome`/`Validate`/`RenderDiagram`/`RenderJSON` treat the
  assembly as a graph (data-object nodes + slot edges) and render it for humans or machines
- **Dynamic wiring (synaptic plasticity)** — `Agent.Replace(slot, port)` swaps
  `Think`/`Act`/`Sandbox`/`Budget`/`Hooks` at runtime; takes effect at the next `Stimulate`,
  an in-flight `Stimulate` keeps the ports it started with
- **Agent Card (A2A-ready)** — `AgentCard(Organs)` renders the assembly as a machine-readable
  capability card (JSON); publish it at `/.well-known/agent-card.json` for agent discovery
- **A2A-style task states** — `Signal.Status` carries the six task lifecycle states
  (submitted/working/needs-input/completed/failed/cancelled) for end-to-end inter-agent tracking
- **Six host-injected ports** (all required, no stubs):
  `Thinker` (LLM), `Effector` (tools), `Closer` (cleanup), `Hooks` (interception),
  `Sandbox` (permission gate), `ContextBudget` (context trimming)
- **Step-Resume** — each `Stimulate` is one stateless step; stop the iterator, do host-side work
  (human approval, async tool), then `Stimulate` again. Human-in-the-loop without framework support
- **Pause/Resume** — process-level suspension at gap points (before each Think / tool execution);
  the loop yields `EventState(StatePaused)` and keeps its in-cycle state until resumed
- **Per-tool timeout & retry** — `Config.ToolTimeout` bounds each tool execution;
  `ToolMaxRetries` retries effector errors (business errors in `Effect.Err` are never retried)
- **Host-managed history** (MemHop pattern) — context accumulation and memory injection are yours
- **Flat multi-agent model** — sub-agents and inter-agent messaging are host tools
  (`spawn_agent` / `send_message`), never framework-level nesting
- **Resistance is feedback, not failure** — denied tools, tool errors, and busy targets flow back
  into the loop as `EventToolResult` feedback; the loop continues

## Upgrading to v2.0

- **`New` takes a single `Blueprint{Organs, Config, Strict}`** — define once, `New` many times:
  wrap your `Organs`/`Config` in a `Blueprint` at the call site. `Strict: true` also rejects
  warn-level assembly findings (half-wired hook pairs, incomplete memory/plan paths).
- **`Sandbox` now requires `Bounds() string`** — return the execution boundary description;
  the framework snapshots it once per `Stimulate` and surfaces it read-only to hooks and the
  Thinker via `Prompt.Bounds`:
  ```go
  func (s *MySandbox) Bounds() string { return "read-only /workspace" }
  ```
- **Missing-port errors are `errors.Join`-aggregated** — single missing-port errors keep the
  exact `meow: required port X not injected` format; match with `errors.Is` / `strings.Contains`, never exact string equality.

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
| `ContextBudget` | guard | Trims context before each Think |
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
[Host Integration Guide](docs/host-integration.en.md).

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

func (sandbox) Allow(ctx context.Context, a meowire.Action) (bool, string, error) {
	return true, "", nil
}

func (sandbox) Bounds() string { return "read-only /workspace" }

func main() {
	// Blueprint: define once, New many times (flat-model multi-agent)
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			Think:   thinker{},
			Act:     effector{},
			Closer:  closer{},
			Hooks:   &meowire.Hooks{},
			Sandbox: sandbox{},
			Budget: &meowire.ContextBudget{
				MaxTokens: 8192,
				Trimmer:   func(c []string, max int) []string { return c },
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
*inside* the loop via your `Effector`; results are appended to the next round's `Prompt.Context`
automatically. The host observes (`EventToolCall` / `EventToolResult`) but never feeds data back
into an open iterator.

Stopping consumption (yield returns false) **abandons the round**: tools at or after the stop
point do **not** execute, and all state accumulated in this round is discarded.
`OnCycleEnd` is guaranteed to run exactly once per `Cycle` — on success, on error, and on abort.

### Step-Resume (human-in-the-loop)

Stop the iterator, execute the tool host-side (human approval, async work, external service),
append the result to your history, then call `Stimulate` again. Each `Stimulate` is a stateless
step — this is the recommended way to implement `ask_user`, long-running tasks, and retries.

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
| Host Integration Guide | [docs/host-integration.en.md](docs/host-integration.en.md) |
| Protocol Mapping Guide | [docs/protocols.md](docs/protocols.md) — MCP / A2A / AGENTS.md / Authority |
| Email | qyiun666@163.com |
