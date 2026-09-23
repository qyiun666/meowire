# Meowire

Bionic agent harness base for Go — **a skeleton with its own brain.**

Meowire is a minimal decision-loop kernel for building agent hosts. It wires the orchestration
(Think → Act → yield events), ships the brain (an openai-go client you parameterize, not
implement), and leaves the rest to you: the tools, the memory, the security policy. No framework
opinion about your stack — just a clean loop you can rely on.

> Requires Go 1.27+ (the stream is an `iter.Seq`, and a parallel Act batch rides `sync.WaitGroup.Go` — 1.23 and 1.25 respectively; the ceiling comes from `go.mod`).

## Why Meowire

- **The brain arrives as parameters.** Pass `Organs.Brain{BaseURL, Key, Model, Stream, Mode}` and the
  composition root constructs the bundled openai-go brain — you never write a Thinker and never
  import an SDK. `Mode` picks the wire: chat completions by default (zero value included) or the
  Responses API (`BrainModeResponses`); both render the same stateless prompt and fold back into the
  same decision. The kernel (loop, events, ports) stays standard-library only; provider vocabulary
  stops at one internal package.
- **You own everything else.** Meowire provides no tool framework, no memory backend — it injects
  six host ports and expects you to implement them. The framework never hides what your agent
  actually does.
- **One direct dependency, pinned.** `github.com/openai/openai-go/v3` at v3.61.0 (chosen and
  upgraded deliberately, never ridden along); the kernel itself is standard library, and the only
  other entries in `go.mod` are the four `tidwall/*` indirects that SDK carries.
- **Small and readable.** ~4.7k lines of Go (production code: `api/` plus `internal/`, excluding
  `*_test.go` and the `internal/testutil` doubles; all `*_test.go` together are another ~9.6k).
  The decision loop reads as one concern
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
- **Wiring graph inspection** — `Connectome` (edges) / `ConnectomeNodes` (data-object nodes) /
  `WiringDiagram` (blueprint × this assembly, filled or not) / `Validate` / `RenderDiagram` /
  `RenderJSON` treat the assembly as a graph and render it for humans or machines
- **Dynamic wiring (swap an organ at runtime)** — `Agent.Replace(slot, port)` swaps
  `Act`/`Sandbox`/`Budget`/`Mem`/`Hooks` at runtime; takes effect at the next `Stimulate` or
  `Resume`, an in-flight run keeps the ports it started with; every successful swap is
  audited as `EventReplace` at the start of the next Stimulate/Resume. The brain has no slot:
  a different model is a new assembly with different `Brain` parameters
- **Runtime config updates** — `Agent.UpdateConfig(cfg)` / `Agent.GetConfig()` tune
  `MaxRounds` and the other scalar limits hot, without rebuilding the agent
- **Bundled brain + six host ports, all required** (no stubs, no optional organ — the kernel
  does not know a neighbour exists; anything between instances belongs to the host): the brain is
  `Organs.Brain` (BaseURL/Key/Model/Stream/Mode — any OpenAI-compatible endpoint; Mode selects chat completions by default or the Responses API), then `Effector` (tools), `Closer` (cleanup), `Hooks`
  (interception — all eight callbacks H1–H8 required: explicit no-op, not
  absence), `Sandbox` (permission membrane), `ContextBudget` (token
  regulator over both accumulating tracks — needs Trimmer, TrimResults and MaxTokens),
  `Memory` (experience port — `Recall` before each Think, `Remember` once per invocation).
  Streaming deltas ride `meowire.WithSink(ctx, sink)` — they reach the host before the output
  membrane rules; `EventText` (whole, already ruled) replaces what was pushed
- **Several organs behind one port** — `GuardStack` / `FallbackEffector` compose
  implementations into the one organ the loop sees and return **the port type itself**, so a composed
  organ wires like a plain one and the blueprint gained nothing
- **Organs can be brought up before they are used** — any port may declare `Bootable`; `New` boots
  each declaring organ once, in `PortOrder()` (the sequence the blueprint implies, not a list written
  beside it), and `Replace` boots before a swap commits. A failing boot aborts and releases what the
  attempt opened through the host `Closer` — the framework still closes nothing
- **Step-Resume** — each `Stimulate` is one stateless step; stop the iterator, do host-side work
  (async tool, manual takeover, `ErrMaxRounds` continuation), then `Stimulate` again. Tool-requested
  input (`ask_user`) is not done this way — see Suspension-resume below (the only form)
- **Unified suspension-resume** — one snapshot + resume path for all four suspension flavours. A suspended loop is answered with one typed `Response{Answer, Deny}`, and `Session.Kind()` says which field that handle reads:
  - **ask_user**: a tool returns `Effect{WaitInput: question}` and the loop yields `EventState(StateWaiting)` + `EventWaitInput` (tool, question, opaque `Session`) and ends the iterator normally — no blocking, no extra round, no budget during the wait. `Agent.Resume(ctx, sess, Response{Answer: ...})` continues: the answer enters the loop as the pending tool's structured result (`Prompt.ToolResults` entry, ID preserved), a `Response{Deny: why}` records that call as failed instead, remaining tools run first, then the loop resumes from the suspended round.
  - **Pause**: `Agent.Pause()` is honored at gap points (before each Think / tool execution); the loop yields `EventState(StatePaused)` + `EventPaused` (Session snapshot) and ends the iterator normally — `Agent.Resume(ctx, sess, Response{})` continues (nothing is being asked, so the Response is not read). A pause before a tool keeps that tool in the snapshot, so Resume runs it first.
  - **Membrane ask**: a tri-state ruling — `Sandbox.Allow` before a call runs, or `Sandbox.Emit` before a round's text is heard — yields the same StateWaiting + EventWaitInput pair (the question is the ruling's reason; an `Emit` ask withholds the draft inside the `Session`). Resolution is one ruling, never a string match inside the host's text: `Response{Deny: why}` refuses (the pending call gets `[sandbox-denied: why]` feedback), an empty `Deny` with a non-empty `Answer` approves — the pending call runs without re-gating, or the withheld draft is said as generated without another Think. The zero `Response` declines (`[sandbox-denied: declined]`), so failing to answer fails closed.
  - **Persistent**: `Session.Marshal()` / `UnmarshalSession` give versioned JSON persistence — a suspended or paused loop survives process restarts (alignment with mainstream checkpoint/resume).
  Timeouts are host-controlled (default deny); the wait is a suspension, never a synchronous block inside the Effector (a block would hold up the Close wait)
- **Structured tool feedback** — tool results flow back as `Prompt.ToolResults`
  (`ToolResult{ID, Name, Result, Err}`, single track; `call_xxx` IDs preserved);
  rendering (tool-role messages, `[tool_call_id=xxx]` markers, plain text) is the
  bundled brain's choice — the text track (`Context`) keeps host base + sandbox denials
- **Per-tool timeout & retry** — `Config.ToolTimeout` bounds each tool execution;
  `ToolMaxRetries` retries effector errors (business errors in `Effect.Err` are never retried)
- **Parallel tool batches (opt-in)** — `Config.ParallelActs` executes a round's
  multiple independent tool calls concurrently (serial gating → parallel Act → serial
  feedback in call order); events and hooks stay serial. Off by default; requires a
  concurrency-safe Effector. `MaxParallelActs` caps how many calls of a batch run at
  once. `Session.RemainingCalls()` exposes the pending calls of a
  suspension (empty when nothing is left to replay)
- **The stream is journalable** — `EncodeEvent`/`DecodeEvent` write one versioned JSON
  record per event (wire format v2), carry enums by name, restore framework errors by identity, refuse an enum its
  own name table cannot spell, and name anything that could not cross in the event's `Dropped` field; every event carries the `CellID` of the cell
  that produced it plus `Seq` (1-based, strictly increasing per cell across `Stimulate` and `Resume`)
  and `TS` (Unix milliseconds), so one log can hold events from several agents **and** still tell
  "this agent had nothing to say" from "a record went missing"
- **Tri-state rulings on both sides & reflection primitives** — `Sandbox.Allow` (before a tool
  runs) and `Sandbox.Emit` (before a round's text is heard) each return a
  `Verdict`: Deny (zero value, fail-closed), Allow, or Ask — a value outside those three named
  states denies the same way; an ask suspends via the same
  suspension-resume protocol as ask_user and takes effect only after the host approves
  (one audit chain closes with a terminal resolve record). `Hooks.OnCycleEnd(ctx, output,
  outcome)` classifies how every cycle ended (`CycleOutcome`: Done/Suspended/MaxRounds/
  Error/Aborted); `BeforeStimulate` may write a turn-scoped self-review note onto
  `Prompt.Reflection`, carried onto every brain prompt of the cycle
- **Host-managed history** (MemHop pattern) — context accumulation and memory injection are yours
- **Flat multi-agent model** — one `Agent` is one kernel, and a host that wants several
  builds several instances; sub-agents stay host tools (`spawn_agent`), never
  framework-level nesting
- **Resistance is feedback, not failure** — denied tools and tool errors flow back into the
  loop as `EventToolResult` feedback; the loop continues

## Upgrading

### v1.3.x — the current surface (four Breaking rounds since v1.2.0)

- **v1.3.8 — the inter-agent layer is gone (Breaking)**: `internal/synapse` (the synapse graph, the
  `Hebbian`/`STDP`/`Prune` learning rules) and the cell-side delegation pairing were deleted
  wholesale. Addressing, routing, delivery, capability discovery and synaptic weight belong to the
  host; one `Agent` is one kernel, and multi-agent means the host `New`s several instances.
- **v1.3.9 — identity and shape**: `Organs.ID` became required with no default (every event is
  attributed by it and every `Session` is checked against it); a suspension now names its own cause
  through `Session.Kind()`, and `Resume` takes one typed `Response{Answer, Deny}` for all four
  flavours; the answer/denial tracks were separated.
- **v1.3.10 — the brain is bundled (Breaking)**: `Organs.Thinker`, the `SlotThink` constant,
  `Replace("think", …)`, the blueprint's `P1` point and `FallbackThinker` are gone. Pass
  `BrainConfig{BaseURL, Key, Model, Stream, Mode}` on `Organs` instead — no host writes a Thinker,
  and the public surface has no `Thinker` type. A different model is a new `New`, not a swap.
- **v1.3.11 — wiring inspection narrowed (Breaking)**: `BuildGraph`, the `WiringGraph` type,
  `SlotsByTarget` and the `meowire.PauseGate` alias were removed. `Connectome()` /
  `ConnectomeNodes()` / `WiringDiagram(o)` / `RenderDiagram` / `RenderJSON` cover the same ground,
  and every edge carries its own `TargetID`.
- **v1.3.12 — a fact gets one carrier (Breaking)**: `Record` lost `CellID` (`MemoryQuery` already
  names the cell that is asking, so copying it onto every returned row left two carriers with no
  arbiter), `Record.Created` is Unix milliseconds like every other timestamp in the loop, and
  `Issue.ID` is gone because an empty `Issue.Wire` is what "assembly-level" already means. A host
  that stores how a cycle ended as a word keeps no vocabulary of its own now that `CycleOutcome` has
  `String()`; `RenderDiagram` prints each edge's `Target`, so neither face of the wiring graph drops
  the reading the blueprint takes.

### v1.2.0

- **Every wiring point is now required** — hooks H1–H8 must all be set
  (explicit no-op where no behavior is wanted); a missing callback fails
  `New`. `Sandbox` and `ContextBudget` were already required; now the loop
  never tolerates nil — `Bounds()` snapshot, `EventSandbox` audit verdicts
  and `Budget.Trimmer` runs are unconditional.
- **`Blueprint.Strict` removed** — with no warn level left there is nothing
  to promote; drop the field from `Blueprint` literals.
- **`ContextBudget` completeness enforced** — nil Trimmer or MaxTokens <= 0
  fails assembly (a budget that does not trim is not a budget); `TrimResults` was later
  brought under the same rule — each growing track needs its own trimmer.
- **`Replace` rejects nil/incomplete ports** — swapped organs must be
  complete.
- **`Sandbox` requires `Bounds() string`** — return the execution boundary
  description; the framework snapshots it once per `Stimulate`/`Resume` and surfaces
  it read-only to hooks and the brain via `Prompt.Bounds`:
  ```go
  func (s *MySandbox) Bounds() string { return "read-only /workspace" }
  ```
- **Missing-port errors are `errors.Join`-aggregated** — match with `errors.Is` (a joined error
  answers `Is` for each of its parts); never compare error strings.

## Architecture

```
meowire (module root)
  ├── api/                facade + composition root — the sole public surface
  ├── internal/brain      the bundled openai-go brain (the one provider package)
  ├── internal/cell       agent kernel (ID + ports + DecisionLoop)
  ├── internal/nerve      decision loop, ports (incl. memory), hooks, events, guards
  ├── internal/testutil   shared doubles for the six host ports, used by test/
  └── test/               integration suite and contract guards
```

Only `api/` is importable; the `internal/*` packages sit beside it at the module root, and
`internal/nerve` never imports `internal/brain` — the composition root in `api/` builds the
brain and hands it to the cell as the loop's Thinker.

| Concept | Where | Role |
|---|---|---|
| `Agent` / `New` / `Stimulate` / `Close` | `api/` | Facade: the entire public surface |
| `DecisionLoop.Cycle` | `internal/nerve/loop.go` | Pure orchestration: Think → Act → yield |
| `Cell` | `internal/cell/cell.go` | Minimal kernel: ID + ports + loop |
| `Brain` | `internal/brain` | The bundled organ: openai-go chat completions, constructed from `Organs.Brain` by the composition root |
| `Effector` / `Closer` | ports | Host-provided capabilities |
| `Hooks` | ports | BeforeStimulate / AfterStimulate / BeforeThink / AfterThink / BeforeAct / AfterAct / OnError / OnCycleEnd |
| `Sandbox` | guard | Permission membrane on both sides: `Allow` before each Act, `Emit` before a round's text reaches anyone; `Bounds()` surfaces the execution boundary to the brain via `Prompt.Bounds` |
| `ContextBudget` | guard | Trims the text `Context` and the structured `ToolResults` before each Think, same limit |
| `Memory` | port | Recalls this round's records into `Prompt.Memories`; takes the finished cycle's facts back |
| `Event` | events | Typed observation mirror of the loop |

## Installation

```sh
go get github.com/qyiun666/meowire@latest
```

Import the facade package — the sole public surface:

```go
import meowire "github.com/qyiun666/meowire/api"
```

For the full host-side integration contract — the brain parameters, the six
ports, field-by-field semantics, the event stream, and the pitfalls — see the
[Host Integration Guide](host-integration.en.md).

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"os"

	meowire "github.com/qyiun666/meowire/api"
)

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
	// Blueprint: define the wiring once, New many times — varying Organs.ID per
	// instance (flat-model multi-agent)
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-001",                                                             // required: events and resume handles are attributed to it
			Brain:   meowire.BrainConfig{Model: "gpt-5.2", Key: os.Getenv("OPENAI_API_KEY")}, // the bundled brain
			Act:     effector{},
			Closer:  closer{},
			Hooks:   meowire.FullHooks(meowire.Hooks{}), // all eight callbacks, explicit no-ops
			Sandbox: sandbox{},
			Budget: &meowire.ContextBudget{
				MaxTokens:   8192,
				Trimmer:     func(c []string, max int) []string { return c },
				TrimResults: func(rs []meowire.ToolResult, max int) []meowire.ToolResult { return rs },
			},
			Mem: memory{},
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
opaque `Session` — resume it via `Agent.Resume(ctx, sess, resp)` (see Suspension-resume above).
The two paths are mutually exclusive; a break-based `ask_user` would lose the suspended context
(the `Session` is opaque and cannot be rebuilt by hand).

### Unified suspension-resume

All four suspension flavours — tool-requested input (ask_user), a membrane ask on either side of the loop
(`VerdictAsk`), and host-requested pause — share one
mechanism: the loop yields a suspension event carrying an opaque `Session` snapshot and ends the
iterator normally; the host saves the Session (optionally persisting it via `Session.Marshal()` /
`UnmarshalSession` for cross-process recovery), then calls `Agent.Resume(ctx, sess, resp)` to
continue from the suspended point — no extra round, no budget during the wait.

What `resp` (a `Response`) means is decided by `Session.Kind()`, never by the text of the answer:

- **ask_user** (`WaitTool`): `Effect{WaitInput: question}` → `EventState(StateWaiting)` +
  `EventWaitInput`; `Response{Answer: text}` is injected as the pending tool's structured result,
  `Response{Deny: why}` records that call as failed — a refusal is a tool failure, not tool output.
- **Pause** (`WaitPause`): `Agent.Pause()` honored at gap points → `EventState(StatePaused)` +
  `EventPaused`; `Resume(sess, Response{})` continues without injecting anything (nothing was asked).
  A pause before a tool
  keeps that tool (and the calls after it) in `Session.remaining`, so Resume runs them first.
- **Membrane ask** (`WaitCallAsk` / `WaitUtterance`): `Sandbox.Allow` or `Sandbox.Emit` returning
  `VerdictAsk` → the same
  `EventState(StateWaiting)` + `EventWaitInput` pair (question from the ruling's reason, and an
  `Emit` ask carries no `Call`); a `Deny` refuses with that reason, a non-empty `Answer` with no
  `Deny` approves, and the zero `Response` declines — the answer is a ruling, not a string to match.
- Resuming a pause backs out that pause request, so the resumed loop does not suspend again at its
  first gap point; resuming any other suspension leaves a standing request alone. `Agent.Unpause()`
  only backs out a pause request that has not taken effect yet.
- The Session is single-use: resuming it twice re-executes the remaining tool calls (host
  responsibility). A pause never interrupts a running Think/Act: it is honored at a gap point.

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

Meowire is a **flat model**: one `Agent` = one kernel, and **everything between two
agents stays with the host**.

- Sub-agents: a `spawn_agent` host tool `New`s an instance, consumes its `Stimulate` stream and
  returns the result as `EventToolResult` feedback — the kernel never learns a second instance exists
- The kernel ships no inter-agent addressing, delivery, reply pairing, capability discovery or
  synaptic graph: `Organs` has no slot addressed to a neighbour and `Prompt` has no inbound track
  (`test/wiring_free_test.go` guards that boundary mechanically)
- To connect agents, do it in the host: feed A's output into B's `Stimulate`, or register B as a
  tool of A
- `Organs.ID` names one instance and has no default: `New` rejects an unnamed agent, because that
  is the identity every event carries and every `Session` is checked against
- `Agent.ID()` reads that name back from a built instance — the same value every `Event.CellID`
  carries, which is what a host keys event routing and log attribution on across several agents

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
| 中文 README | [README.zh-CN.md](README.zh-CN.md) |
| Host Integration Guide | [host-integration.en.md](host-integration.en.md) |
| Brain spec (openai-go) | [thinker-openai-go.md](thinker-openai-go.md) — the bundled brain's placement table and protocol reference |
| Reference Host (zh-CN) | [reference-host.md](reference-host.md) — step-by-step runnable AI host |
| Protocol Mapping Guide | [protocols.md](protocols.md) — MCP / A2A / AGENTS.md / Authority |
| Email | qyiun666@163.com |
