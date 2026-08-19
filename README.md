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
- **Small and readable.** ~1.2k lines of source. The entire loop is one file
  (`internal/nerve/loop.go`, ~370 lines with tests).
- **Sealed internals.** All implementation lives under `internal/` — the Go compiler guarantees
  the only importable surface is the `api/` package (`New` / `Stimulate` / `Close` + contract types).

## Features

- **Think→Act decision loop** with per-round retry and a hard round limit
- **Typed event stream** — `Stimulate` returns `iter.Seq[Event]`; the host observes
  `EventText`, `EventToolCall`, `EventToolResult`, `EventState`, `EventDone`, `EventError`, `EventUsage`
- **Six host-injected ports** (all required, no stubs):
  `Thinker` (LLM), `Effector` (tools), `Closer` (cleanup), `Hooks` (interception),
  `Sandbox` (permission gate), `ContextBudget` (context trimming)
- **Step-Resume** — each `Stimulate` is one stateless step; stop the iterator, do host-side work
  (human approval, async tool), then `Stimulate` again. Human-in-the-loop without framework support
- **Host-managed history** (MemHop pattern) — context accumulation and memory injection are yours
- **Flat multi-agent model** — sub-agents and inter-agent messaging are host tools
  (`spawn_agent` / `send_message`), never framework-level nesting
- **Resistance is feedback, not failure** — denied tools, tool errors, and busy targets flow back
  into the loop as `EventToolResult` feedback; the loop continues

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
| `Hooks` | ports | BeforeThink / AfterThink / BeforeAct / AfterAct / OnError / OnCycleEnd |
| `Sandbox` | guard | Tool permission gate, invoked before each Act |
| `ContextBudget` | guard | Trims context before each Think |
| `Event` | events | Typed observation mirror of the loop |
| `Synapse` / `Memory` | internal | Standalone reference contracts for hosts |

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

func main() {
	agent, err := meowire.New(meowire.Organs{
		Think:   thinker{},
		Act:     effector{},
		Closer:  closer{},
		Hooks:   &meowire.Hooks{},
		Sandbox: sandbox{},
		Budget:  &meowire.ContextBudget{
			MaxTokens: 8192,
			Trimmer:   func(c []string, max int) []string { return c },
		},
	}, meowire.Config{})
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
| Email | qyiun666@163.com |
