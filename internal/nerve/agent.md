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
- `DecisionLoop{}.Resume(ctx, *LoopContext, sess Session, response string, yield)`: continues a suspended loop from a Session — response injected as the pending tool's structured result, remaining tools run, round loop resumes from the suspended round (no extra round, no budget during the wait); stream/hooks/guarantees isomorphic with Cycle
- `LoopState`: StateIdle/StateThinking/StateActing/StatePaused/StateWaiting/StateDone/StateError (StatePaused yielded by the pause gate at gap points; StateWaiting + EventWaitInput yielded when a tool suspends)
- `LoopConfig{MaxRounds, MaxToolOutput, MaxRetries, ToolTimeout, ToolMaxRetries}`: scalar loop config (single source; api.Config aliases it); UpdateConfig swaps it wholesale — next Stimulate/Resume snapshots the new values
- `LoopContext{CellID, Identity string, Methods []MethodSpec, Think, Act, Hooks, Sandbox, Budget, Pause, MaxRounds, MaxToolOutput, MaxRetries, ToolTimeout, ToolMaxRetries, State, Input, Plan, Context, Bounds, ToolResults, System, Tools}`: all ports injected via fields
- `PauseGate{IsPaused, ResumeCh}`: optional pause gate (nil = unsupported); the loop checks it at gap points (before each Think, before each tool execution) — a pending pause yields EventState(StatePaused) and blocks until ResumeCh closes or ctx cancels (error path)
- `Prompt{System, Identity string, Methods []MethodSpec, Tools, Context, Bounds, Input, State, Plan, ToolResults}`: sole data package delivered to Thinker; Context = host-injected base + framework-appended sandbox denials (`[denied: ...]`) — tool results no longer enter the text track; Bounds = Sandbox.Bounds() snapshot taken once per Stimulate; ToolResults = single structured track (ToolResult{ID, Name, Result, Err} — ID echoes the LLM call id, Result/Err carry the truncated raw output, Err non-empty = failed; not trimmed; sandbox denials never appear; rendering is the host's decision)
- `Decision{Text, ToolCalls, Usage}`: Thinker output; non-empty ToolCalls triggers Act phase; Usage (nil = skip) is yielded as EventUsage
- `Action{CellID, Call}` / `Effect{Result, Err, WaitInput}`: tool execution pair; WaitInput non-empty = suspend the loop (request external input; the field is the question text) — the loop yields EventWaitInput and ends the iterator normally, the host resumes via Resume(sess, response); WaitInput wins over err (explicit intent), a nil effect never suspends
- `WaitInput{CellID, Call, Question, Session}`: EventWaitInput payload — which tool suspended, what it asked, and the resume handle
- `Session` (opaque): snapshot at the suspension point (round/input/plan/context/output/pending/remaining/toolResults — all unexported); produced by the framework, consumed by Resume; hosts only save and pass it back; single-use (resuming twice re-executes remaining tools — host responsibility); valid iff round >= 1 (zero value rejected)
- `Identity string`: identity description text, host composed (no structure enforced)
- `MethodSpec{Name, Desc, Input, Output}`: built-in capability description (gene projection, describes only)
- `ToolSpec{Name, Desc, Input, Output}`: host-defined tool specification
- `Usage{Prompt, Completion, Total}`: token accounting; host accumulates via EventUsage events
- `Event{Kind, Text, ToolCall, Effect, State, Err, Output, Usage, Verdict, Wait, Replace}`: typed event from each loop iteration
- `EventKind`: EventText/EventToolCall/EventToolResult/EventState/EventDone/EventError/EventUsage/EventSandbox/EventWaitInput/EventReplace
- `ReplaceAudit{CellID, Slot, Old, New}`: audit record carried by EventReplace — one per successful Replace, emitted at the start of the next Stimulate/Resume (the moment the swap takes effect), before any other event; failed swaps record nothing
- `SandboxVerdict{CellID string, Call ToolCall, Allowed bool, Reason string, Err error}`: audit record carried by EventSandbox — one verdict per tool execution (the membrane is required, so every execution is audited)
- `Hooks{BeforeStimulate, AfterStimulate, BeforeThink, AfterThink, BeforeAct, AfterAct, OnError, OnCycleEnd}`: interception points (**all eight required since 1.2.0 — explicit no-op, not absence; a nil callback fails assembly**); BeforeStimulate fires once before any event with a Prompt prototype — content fields (System/Identity/Methods/Tools/Context/Input/Plan) are written back to LoopContext and apply to every round, State is not written back, error aborts the whole Stimulate; AfterStimulate fires exactly once at cycle end (all paths); AfterAct receives the tool execution error (err non-nil = effector failure)
- `Sandbox` interface: `Allow(ctx, Action) (bool, string, error)` + `Bounds() string` — **required membrane (since 1.2.0 the loop never tolerates nil)**: every tool execution passes Allow (denied → `[denied: reason]` feedback, loop continues), every decision first yields EventSandbox (allowed or denied) as the audit record; Bounds snapshotted once per Stimulate before the BeforeStimulate hook and carried read-only on the Prompt prototype (hooks may read it but cannot override it — not written back)
- `ContextBudget{MaxTokens, Trimmer}`: **required regulator**: Trimmer called before each Think with MaxTokens; a nil Trimmer or MaxTokens <= 0 fails assembly (a budget that does not trim is not a budget)
- `Thinker` / `Effector` / `Closer`: host port interfaces (no stubs — host must provide all)
- `Signal{ID, From, To, Kind, Status, Payload, ErrPayload}`: inter-individual message carrier (host-level type, framework does not consume)
- `SignalKind`: KindStimulus/KindResponse/KindNotice (host-side routing semantics)
- `TaskStatus`: A2A-style task lifecycle states (TaskSubmitted/TaskWorking/TaskNeedsInput/TaskCompleted/TaskFailed/TaskCancelled) carried by `Signal.Status`; "" = not tracked

## Contract Change Checklist

Changes to public contracts (EventKind / Event fields / port interfaces Thinker/Effector/Sandbox/ContextBudget / Signal) must sync every one of:

- `api/types.go` aliases (constants and types)
- This document's Interface Contract enumerations
- `host-integration.md` + `host-integration.en.md` (6.2 event table, 6.1 sequences, port tables)
- `reference-host.md` host example switch (only when a new event needs host handling — the switch is intentionally non-exhaustive)
- `CHANGELOG.md` (Keep a Changelog entry)
- `.qoder/repowiki` event-system doc

`test/contract_sync_test.go` automates all of the above except `reference-host.md`; a missed sync fails `go test ./...`.

## Key Decisions

- DecisionLoop is pure orchestration — no default Think/Act implementations, no stubs
- Suspension (v1.3.0): a tool returning Effect.WaitInput suspends the loop — snapshot a Session (round/Context/ToolResults/remaining calls/output), yield StateWaiting + EventWaitInput, end the iterator normally (no Done/Error); OnCycleEnd/AfterStimulate still fire exactly once (finalOutput "" on the suspension path, like the error path); Resume reloads the session and appends the response as the pending tool's structured result (ID = sess.pending.ID); runs the remaining calls of the suspended round, then re-enters the round loop from the suspended round — the digesting Think uses the suspended round's quota (no extra round); budget Trimmer runs only before each Think (none during the wait); timeouts are host-controlled (resume with "[denied: timeout]")
- MaxRounds<=0 uses DefaultMaxRounds=8; round exhaustion with remaining ToolCalls yields ErrMaxRounds via emitError
- Think retry: MaxRetries attempts, ctx cancellation returns immediately
- Tool execution: actWithRetry applies per-attempt ToolTimeout (<=0 = none) and ToolMaxRetries (<=0 = none) — retry on effector err only (Effect.Err never retried, avoids duplicate side effects); timeout/cancel errors not retried; the final error still flows through ToolResults.Err (resistance is feedback)
- Error paths are unified through emitError: State(StateError) → EventError → OnError (only on normal delivery); OnCycleEnd is guaranteed exactly once via Cycle defer (normal/error/abort)
- Stimulate boundary hooks: BeforeStimulate/AfterStimulate fire exactly once per Stimulate — BeforeStimulate receives a Prompt prototype (Context is a shallow copy, so in-place edits or an early hook error never leak into lc) and its content fields are written back to LoopContext (one-shot injection applies to all rounds; State is loop-managed and not written back; error terminates via emitError); AfterStimulate runs in a nested defer after OnCycleEnd (normal/error/abort, survives an OnCycleEnd panic). BeforeStimulate runs before the ctx-cancellation check, so it fires exactly once even on a canceled context. Injected Context is subject to Budget.Trimmer like all loop context — hosts should size injections within MaxTokens.
- AfterAct receives the raw tool err (nil on success); tool failure text still flows through ToolResults.Err
- Tool error feedback: Act err/Effect.Err/nil effect populate ToolResults.Err (truncated), loop continues — no text track formatting since v1.3.1 (rendering is the host's decision)
- Structured tool feedback (v1.3.1): tool results enter Prompt.ToolResults as ToolResult{ID: tc.ID, Name: tc.Name, Result/Err: truncated raw output} — the LLM call id is preserved (was dropped) and Context no longer carries tool results (single track); sandbox denials stay text-only ([denied: ...] is a verdict, not a result); Resume appends the external response as the pending tool's entry; Session snapshot carries the accumulated ToolResults; EventToolResult echoes the call (Event.ToolCall) for ID association
- Sandbox check before each tool execution: denied → feedback `[denied: reason]`, continues loop; every decision of the membrane first yields EventSandbox (allowed or denied) as the audit record — hosts persist it for the action-level audit trail
- MaxToolOutput truncates ToolResults.Result/Err fields (<=0 = no truncation); truncation keeps UTF-8 rune boundaries
- All errors wrapped with `fmt.Errorf("...: %w", err)`

## Pitfalls

- Host ports must respect ctx: long operations must monitor ctx.Done (otherwise Close cannot interrupt)
- No default Memory/LLM implementations inside the loop — memory is host-managed via LoopContext.Context
- Hooks/Sandbox/Budget are required organs: internal tests fill no-op defaults where they do not target these ports; the api assembly (New/Validate) is where requiredness is enforced
- History/context accumulation happens within the loop only (Context grows with sandbox denials; tool feedback accumulates in ToolResults) — BeforeThink shares the backing array with lc.ToolResults too: replace the slice wholesale, never append in place
- Emit/yield returns false to stop early — callers control termination; abort discards the round, tools after the stop point do not execute, OnCycleEnd still runs (defer)
- StatePaused is produced by the pause gate at gap points (before each Think / tool execution); pause blocks until ResumeCh closes or ctx cancels (emitError); a consumer abort during the pause exits without blocking
- StateWaiting is produced by a tool suspension (Effect.WaitInput) — distinct from StatePaused (gap-point pause): the iterator ends normally, the host resumes via Resume(sess, response); Session is single-use and never mutated by the framework
- PendingReplace (LoopContext field) is drained by the cell snapshot; the loop emits them before any other event — hosts must not inspect them mid-cycle
