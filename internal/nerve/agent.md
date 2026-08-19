# MeowAgent nerve module context (capability layer: decision loop + ports)

> Package lives at internal/nerve — sealed from external import; hosts use only the root package API.

## Purpose

- Capability layer: decision loop orchestration, host ports (Thinker/Effector/Closer), hooks, events
- Guard ports: Sandbox (execution boundary), ContextBudget (context size limit)
- Depends on nothing above cell/synapse/root (memory is standalone, not referenced)

## Dependencies

- Go standard library only (context/fmt/unicode/utf8)

## Interface Contract

- `DecisionLoop{}.Cycle(ctx, *LoopContext, yield)`: pure orchestration — Think → Act → yield events
- `LoopState`: StateIdle/StateThinking/StateActing/StatePaused(reserved)/StateDone/StateError
- `LoopContext{CellID, Identity, Think, Act, Hooks, Sandbox, Budget, MaxRounds, MaxToolOutput, MaxRetries, State, Input, Plan, Context, System, Tools}`: all ports injected via fields
- `Prompt{System, Identity, Tools, Context, Input, State, Plan}`: sole data package delivered to Thinker; Context = host-injected base + framework-appended tool feedback within cycle
- `Decision{Text, ToolCalls, Usage}`: Thinker output; non-empty ToolCalls triggers Act phase; Usage (nil = skip) is yielded as EventUsage
- `Action{CellID, Call}` / `Effect{Result, Err}`: tool execution pair
- `Identity{Name, Role, Traits, Methods}` / `MethodSpec{Name, Desc, Input, Output}`: gene projection
- `ToolSpec{Name, Desc, Input, Output}`: host-defined tool specification
- `Usage{Prompt, Completion, Total}`: token accounting; host accumulates via EventUsage events
- `Event{Kind, Text, ToolCall, Effect, State, Err, Output, Usage}`: typed event from each loop iteration
- `EventKind`: EventText/EventToolCall/EventToolResult/EventState/EventDone/EventError/EventUsage
- `Hooks{BeforeThink, AfterThink, BeforeAct, AfterAct, OnError, OnCycleEnd}`: interception points (all optional, nil = skip)
- `Sandbox` interface: `Allow(ctx, Action) (bool, string, error)` — execution boundary
- `ContextBudget{MaxTokens, Trimmer}`: context size limit, called before each Think
- `Thinker` / `Effector` / `Closer`: host port interfaces (no stubs — host must provide all)
- `Signal{ID, From, To, Kind, Payload, ErrPayload}`: inter-individual message carrier (host-level type, framework does not consume)
- `SignalKind`: KindStimulus/KindResponse/KindNotice (host-side routing semantics)

## Key Decisions

- DecisionLoop is pure orchestration — no default Think/Act implementations, no stubs
- MaxRounds<=0 uses DefaultMaxRounds=8; round exhaustion with remaining ToolCalls yields ErrMaxRounds via emitError
- Think retry: MaxRetries attempts, ctx cancellation returns immediately
- Error paths are unified through emitError: State(StateError) → EventError → OnError (only on normal delivery); OnCycleEnd is guaranteed exactly once via Cycle defer (normal/error/abort)
- Tool error feedback: Act err/Effect.Err/nil effect converted to `[name] error: ...` text, fed back as context for next round (does not terminate loop)
- Sandbox check before each tool execution: denied → feedback `[denied: reason]`, continues loop
- MaxToolOutput truncates tool feedback length (<=0 = no truncation); truncation keeps UTF-8 rune boundaries
- All errors wrapped with `fmt.Errorf("...: %w", err)`

## Pitfalls

- Host ports must respect ctx: long operations must monitor ctx.Done (otherwise Close cannot interrupt)
- No default Memory/LLM implementations inside the loop — memory is host-managed via LoopContext.Context
- All optional hooks/guards are nil-skipped (defensive; host always provides them via root New)
- History/context accumulation happens within the loop only (Context field grows with tool feedback)
- Emit/yield returns false to stop early — callers control termination; abort discards the round, tools after the stop point do not execute, OnCycleEnd still runs (defer)
- StatePaused is a placeholder; no pause/resume API in the current version
