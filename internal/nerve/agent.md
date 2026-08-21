# MeowAgent nerve module context (capability layer: decision loop + ports)

> Package lives at internal/nerve — sealed from external import; hosts use only the api package API.

## Purpose

- Capability layer: decision loop orchestration, host ports (Thinker/Effector/Closer), hooks, events
- Guard ports: Sandbox (execution boundary), ContextBudget (context size limit)
- Depends on nothing above cell/synapse/root (memory is standalone, not referenced)

## Dependencies

- Go standard library only (context/fmt/unicode/utf8)

## Interface Contract

- `DecisionLoop{}.Cycle(ctx, *LoopContext, yield)`: pure orchestration — Think → Act → yield events
- `LoopState`: StateIdle/StateThinking/StateActing/StatePaused(reserved)/StateDone/StateError
- `LoopContext{CellID, Identity string, Methods []MethodSpec, Think, Act, Hooks, Sandbox, Budget, MaxRounds, MaxToolOutput, MaxRetries, State, Input, Plan, Context, System, Tools}`: all ports injected via fields
- `Prompt{System, Identity string, Methods []MethodSpec, Tools, Context, Input, State, Plan}`: sole data package delivered to Thinker; Context = host-injected base + framework-appended tool feedback within cycle
- `Decision{Text, ToolCalls, Usage}`: Thinker output; non-empty ToolCalls triggers Act phase; Usage (nil = skip) is yielded as EventUsage
- `Action{CellID, Call}` / `Effect{Result, Err}`: tool execution pair
- `Identity string`: identity description text, host composed (no structure enforced)
- `MethodSpec{Name, Desc, Input, Output}`: built-in capability description (gene projection, describes only)
- `ToolSpec{Name, Desc, Input, Output}`: host-defined tool specification
- `Usage{Prompt, Completion, Total}`: token accounting; host accumulates via EventUsage events
- `Event{Kind, Text, ToolCall, Effect, State, Err, Output, Usage}`: typed event from each loop iteration
- `EventKind`: EventText/EventToolCall/EventToolResult/EventState/EventDone/EventError/EventUsage
- `Hooks{BeforeStimulate, AfterStimulate, BeforeThink, AfterThink, BeforeAct, AfterAct, OnError, OnCycleEnd}`: interception points (all optional, nil = skip); BeforeStimulate fires once before any event with a Prompt prototype — content fields (System/Identity/Methods/Tools/Context/Input/Plan) are written back to LoopContext and apply to every round, State is not written back, error aborts the whole Stimulate; AfterStimulate fires exactly once at cycle end (all paths); AfterAct receives the tool execution error (err non-nil = effector failure)
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
- Stimulate boundary hooks: BeforeStimulate/AfterStimulate fire exactly once per Stimulate — BeforeStimulate receives a Prompt prototype (Context is a shallow copy, so in-place edits or an early hook error never leak into lc) and its content fields are written back to LoopContext (one-shot injection applies to all rounds; State is loop-managed and not written back; error terminates via emitError); AfterStimulate runs in a nested defer after OnCycleEnd (normal/error/abort, survives an OnCycleEnd panic). BeforeStimulate runs before the ctx-cancellation check, so it fires exactly once even on a canceled context. Injected Context is subject to Budget.Trimmer like all loop context — hosts should size injections within MaxTokens.
- AfterAct receives the raw tool err (nil on success); tool failure feedback still flows through toolFeedback into Context
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
