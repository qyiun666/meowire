# meowire 宿主集成指南

> 阅读对象：集成 meowire 的宿主（Host）开发 AI/开发者。
> 本文档是集成契约的唯一权威说明，所有接口签名、字段语义、事件序列均与源码一致。
> 模块路径：`github.com/qyiun666/meowire`，对外包：`github.com/qyiun666/meowire/api`（别名 `meowire`）。

## 0. 一句话模型

meowire 是一个**纯编排内核**：宿主实现六端口（LLM、工具、清理、钩子、权限门、上下文裁剪），框架负责 Think → Act → yield 事件循环。**框架不管理历史、不管理多 agent、不提供任何默认实现**——六端口全部必填，缺一报错。

宿主对外只需接触三个方法：`New`（装配）、`Stimulate`（运行一轮）、`Close`（关闭）。

## 1. 集成流程总览（6 步）

```
实现六端口 → 组装 Organs → 配置 Config → New() 创建 → Stimulate() 消费事件流 → Close()
```

| 步骤 | 做什么 | 关键点 |
|------|--------|--------|
| 1 | 实现 Thinker/Effector/Closer/Hooks/Sandbox/ContextBudget | 六端口全部必填 |
| 2 | 组装 `Organs` 结构体 | 注入端口 + 固定上下文 |
| 3 | 设置 `Config` | 零值即默认，无需显式填 |
| 4 | `New(o Organs, cfg Config) (*Agent, error)` | 缺端口返回错误 |
| 5 | `for ev := range agent.Stimulate(ctx, text)` | 消费事件流 |
| 6 | `agent.Close()` | 幂等，关闭后 Stimulate 返回 `ErrCellClosed` |

---

## 2. 第 1 步：实现六端口（宿主能力）

### 2.1 Thinker —— LLM 封装（大脑）

```go
type Thinker interface {
    Think(ctx context.Context, p *Prompt) (*Decision, error)
}
```

**入参 `Prompt`（框架组装，Thinker 只读）字段：**

| 字段 | 内容 | 注入方 |
|------|------|--------|
| `System` | 系统指令（"你是客服助手…"） | 宿主，构造时固定 |
| `Identity` | 身份描述文本（宿主自拼，如“你叫 meow，角色 assistant，语气温暖”） | 宿主，构造时固定 |
| `Methods` | 内置能力描述（基因投影，仅描述不执行；`MethodSpec{Name, Desc, Input, Output}`） | 宿主，构造时固定 |
| `Tools` | 可用工具清单（function schema） | 宿主，构造时固定 |
| `Context` | 上下文切片：宿主常驻基底 + 框架循环内追加的工具反馈 | 宿主基底 + 框架追加 |
| `Input` | 本次刺激文本（Stimulate 入参） | 框架，每轮动态 |
| `State` | 当前循环状态字符串（`"thinking"`/`"acting"`/…） | 框架自动更新 |
| `Plan` | 任务计划/进度文本 | 宿主经 `Hooks.BeforeThink` 写 `p.Plan`（指针可改，下一轮 Think 生效）；配合宿主 `update_plan` 工具形成闭环（§7.3 ③） |

**返回值 `Decision` 字段：**

| 字段 | 内容 | 约束 |
|------|------|------|
| `Text` | LLM 文本输出（整段） | 框架以 EventText 透出，**token 增量不进事件流** |
| `ToolCalls` | 本轮工具调用列表 `{ID, Name, Args}` | `Args` 为 JSON 字符串；**空 = 循环结束** |
| `Usage` | token 用量 `{Prompt, Completion, Total}` | **nil = 跳过 EventUsage 计费事件** |

**宿主职责：**
- 将 Prompt 渲染为 messages 调用 LLM API，解析工具调用
- 流式输出在 Thinker 内部自行消费（如推给 WebSocket channel）；事件流只承载整段 Text
- **必须监控 `ctx.Done`**（长请求可被框架取消）
- 同一 Agent 并发 Stimulate 时 Thinker 必须并发安全

### 2.2 Effector —— 工具执行器（手）

```go
type Effector interface {
    Act(ctx context.Context, a Action) (*Effect, error)
}
```

**字段：**

| 字段 | 内容 |
|------|------|
| `Action.CellID` | 触发工具的 agent 标识（= Organs.ID） |
| `Action.Call` | 工具调用 `{ID, Name, Args}`，`Args` 为 JSON 字符串 |
| `Effect.Result` | 成功结果文本（框架格式化为 `[工具名] 结果` 追加进 Context） |
| `Effect.Err` | 工具自身错误文本（框架格式化为 `[工具名] error: ...`） |
| 返回 error | 执行层错误（同样被格式化进反馈） |

**宿主职责：**
- 工具注册表 + 分发器：按 `Name` 路由、反序列化 `Args`、序列化结果
- 多 agent 工具在这里实现：`spawn_agent`（New → Stimulate → 返回结果）、`send_message`（跨 agent 消息，见 §7.2）——扁平模型约定
- `ask_user` 类工具可在此**同步阻塞**等待人工输入，必须监听 `ctx.Done` 以便取消时退出
- 工具失败返回 `Effect{Err: ...}` 而非 error 也可，两者都会作为反馈继续循环（**阻力是反馈不是失败**）

### 2.3 Closer —— 清理器

```go
type Closer interface {
    Close() error
}
```

释放 LLM 客户端、HTTP 连接等宿主资源。框架保证 Agent.Close 幂等（CAS），重复调用无副作用。

### 2.4 Hooks —— 拦截回调（全部可选，nil 字段跳过；但 Organs.Hooks 指针本身必填）

```go
type Hooks struct {
    BeforeThink func(ctx context.Context, p *Prompt) error
    AfterThink  func(ctx context.Context, d *Decision) error
    BeforeAct   func(ctx context.Context, a *Action) error
    AfterAct    func(ctx context.Context, a *Action, e *Effect)
    OnError     func(ctx context.Context, err error)
    OnCycleEnd  func(ctx context.Context, output string)
}
```

| 字段 | 触发时机 | 宿主典型用途 |
|------|---------|-------------|
| `BeforeThink` | 每次 Think 前 | 动态注入检索记忆、更新 Plan |
| `AfterThink` | Think 成功后 | 序列化 Decision 回存 Plan/记忆 |
| `BeforeAct` | 每个工具执行前 | 审批、改写工具参数 |
| `AfterAct` | 工具执行后 | 工具日志、失败降级 |
| `OnError` | 不可恢复错误时 | 告警上报 |
| `OnCycleEnd` | **每轮恰好一次**（正常/错误/消费者提前停止三条路径都触发） | 结算、持久化最终输出 |

**⚠️ `BeforeThink` 必须整体替换 `p.Context`（`p.Context = append(p.Context[:0], newCtx...)` 或直接赋新切片）——它与循环上下文共享底层数组，直接 append 会污染循环内上下文。**

### 2.5 Sandbox —— 权限门（安全红线落点）

```go
type Sandbox interface {
    Allow(ctx context.Context, a Action) (allowed bool, reason string, err error)
}
```

- 每次工具执行前调用；`allowed=false` 时框架生成 `[denied: reason]` 反馈进 Context，**循环继续**（不终止）
- `Allow` 返回 err 时按 `[sandbox error: ...]` 拒绝
- 宿主实现安全策略：工具白名单/黑名单、人工确认、敏感操作拦截

### 2.6 ContextBudget —— 上下文裁剪器

```go
type ContextBudget struct {
    MaxTokens int
    Trimmer   func(ctx []string, max int) []string
}
```

- 每次 Think 前调用 `Trimmer`，把 Context 裁剪到 `MaxTokens` 内
- 是裁剪器不是硬停：超预算只剪不报错
- 不想裁剪时返回入参原切片即可

---

## 3. 第 2 步：组装 `Organs`（装配根，唯一组装点）

```go
o := meowire.Organs{
    ID:      "agent-001",                       // 唯一标识，空 = "agent"
    Think:   myThinker,                         // 必填
    Act:     myEffector,                        // 必填
    Closer:  myCloser,                          // 必填
    Hooks:   &meowire.Hooks{BeforeThink: ...},  // 必填（指针，内部字段可全空）
    Sandbox: mySandbox,                         // 必填
    Budget:  &meowire.ContextBudget{...},       // 必填

    System:   "你是 meow agent，用中文回答",      // 固定系统指令
    Identity: "你叫 meow，角色 assistant，语气温暖", // 身份描述文本（宿主自拼）
    Methods:  []meowire.MethodSpec{...},        // 内置能力描述（仅投影）
    Tools:    []meowire.ToolSpec{...},          // 工具清单
    Context:  []string{"[记忆] 用户偏好：简洁"},  // 常驻上下文基底（历史记忆入口）
}
```

**字段明细：**

| 字段 | 类型 | 内容 | 必填 |
|------|------|------|------|
| `ID` | string | agent 唯一标识；空 = `"agent"`。扁平多 agent 时用于 synapse 路由和日志 | 否 |
| `Think` | Thinker | LLM 封装 | **是** |
| `Act` | Effector | 工具执行 | **是** |
| `Closer` | Closer | 资源清理 | **是** |
| `Hooks` | *Hooks | 拦截回调 | **是** |
| `Sandbox` | Sandbox | 权限门 | **是** |
| `Budget` | *ContextBudget | 上下文裁剪 | **是** |
| `System` | string | 系统指令，进 Prompt.System | 否 |
| `Identity` | string | 身份描述文本（宿主自拼），进 Prompt.Identity | 否 |
| `Methods` | []MethodSpec | 内置能力描述（基因投影，仅描述不执行）；`MethodSpec{Name, Desc, Input, Output}`，进 Prompt.Methods | 否 |
| `Tools` | []ToolSpec | 工具清单，进 Prompt.Tools；`ToolSpec{Name, Desc, Input, Output}`，`Input` 为 JSON Schema（Thinker 据此生成 ToolCall.Args） | 否 |
| `Context` | []string | 常驻上下文基底（历史记忆在此注入，MemHop）；进 Prompt.Context 初始值 | 否 |

**注意：`New` 的必填校验只查指针/接口非 nil，`Hooks` 传 `&meowire.Hooks{}` 即可满足必填。**

---

## 4. 第 3 步：`Config`（零值即默认，全部可选）

```go
type Config struct {
    MaxRounds     int
    MaxToolOutput int
    MaxRetries    int
}
```

| 字段 | 语义 | 零值默认 |
|------|------|---------|
| `MaxRounds` | 硬性轮数上限；**最后一轮仍有工具调用时以 `ErrMaxRounds` 结束**（该轮工具结果不会被下一轮 Think 消化），配合 Step-Resume 防死循环 | 8 |
| `MaxToolOutput` | 工具反馈截断长度（按 UTF-8 安全截断，超长附 `[truncated, N bytes total]`） | 不截断 |
| `MaxRetries` | Think 重试次数（仅重试 Think；工具失败防护在宿主侧 Effector/AfterAct） | 不重试 |

---

## 5. 第 4 步：`New` 装配校验

`New(o Organs, cfg Config) (*Agent, error)`：

- 遍历校验六端口，缺失返回 `meow: required port X not injected`（X ∈ Think/Act/Closer/Hooks/Sandbox/Budget）
- 装配后宿主**只能**调用 `Stimulate` / `Close`，不能触碰内部 cell
- 六端口均须由宿主实现，**没有 stub、没有默认实现、没有"最小可运行"路径**

---

## 6. 第 5 步：`Stimulate` 消费事件流

`Stimulate(ctx context.Context, text string) iter.Seq[Event]`，宿主用 `for ev := range agent.Stimulate(...)` 消费。

### 6.1 事件序列

**无工具单轮（4 个事件）：**

```
EventState(thinking) → EventText → [EventUsage(可选)] → EventState(done) → EventDone(累计输出)
```

**带工具调用（每轮插入，可多轮循环）：**

```
EventState(thinking) → EventText → [EventUsage] → EventState(acting)
  → (EventToolCall → EventToolResult) × N → 回到 EventState(thinking) → …
```

**错误路径：**

```
EventState(error) → EventError(Err)
```

**关闭后 Stimulate：** 直接产出 `EventError(ErrCellClosed)`。

### 6.2 `Event` 字段（按 Kind 生效，其余为零值）

| Kind | 有效字段 | 内容 |
|------|---------|------|
| `EventText` | `Text` | LLM 整段文本输出 |
| `EventToolCall` | `ToolCall *ToolCall` | LLM 决定调用的工具 |
| `EventToolResult` | `Effect *Effect` | 工具执行结果（含被 Sandbox 拒绝：`Effect.Err = "[denied: reason]"`） |
| `EventState` | `State LoopState` | 循环状态（idle/thinking/acting/paused/done/error） |
| `EventDone` | `Output` | 整轮累计文本输出 |
| `EventError` | `Err` | 不可恢复错误（含 `ErrMaxRounds`、`ErrCellClosed`） |
| `EventUsage` | `Usage *Usage` | 最近一次 Think 的 token 用量 |

### 6.3 宿主必须掌握的两个语义

1. **提前停止（Step-Resume 基础）**：`for range` 中 break/return 即放弃本轮——停止点及之后的工具**不会执行**，本轮累积状态全部丢弃。宿主可中断事件流做人工审批/异步任务，再调用 `Stimulate` 继续。
2. **无状态 step**：每次 `Stimulate` 是一次无状态 step，无跨调用状态残留。宿主自己保存历史，下次通过 `Organs.Context`（或 `Hooks.BeforeThink`）放回去。

---

## 7. 第 6 步：`Close` 与可选扩展

### 7.1 `Close() error`

幂等（CAS 保证）；依次关闭 cell + 宿主 Closer，错误用 `errors.Join` 聚合；关闭后 Stimulate 返回 `ErrCellClosed`。宿主应 `defer agent.Close()`。

### 7.2 可选扩展

**Memory（记忆后端，宿主自建）**：`internal/memory` 是参考契约，框架**不消费**：

```go
type Record struct { Key, CellID, Kind string; Content []byte; Created int64 }  // Created 为 Unix 秒
type Query  struct { CellID, Prefix, Kind string; Limit int }                  // 空 CellID 匹配全部；Limit<=0 不限
type Memory interface {
    Save(ctx, Record) error
    Recall(ctx, Query) ([]Record, error)
    Forget(ctx, key, cellID string) error
}
```

宿主实现后端，经 `Organs.Context` + `Hooks.BeforeThink` 注入循环（MemHop 模式）。

**Synapse（多 agent 消息参考实现）**：

```go
type Synapse interface {
    Link(ctx, from, to string) error
    Fire(ctx, sig Signal) error   // Signal{ID, From, To, Kind, Payload []byte, ErrPayload bool}
}
```

- `SignalKind`：`KindStimulus`（宿主发任务）/`KindResponse`（agent 回复）/`KindNotice`（旁路通知，不进决策循环）
- 真正路由（channel/HTTP/Redis/gRPC）宿主自选；宿主工具 `send_message` 经 `synapse.Fire` 发送，阻力（目标忙/未知 agent）以 `EventToolResult` 反馈回循环，不硬停
- synapse 错误（`ErrNoTarget`/`ErrNotLinked`/`ErrTargetBusy`）在 api 层 re-export

**扁平多 agent 模型**：一个 Agent = 一个内核；宿主管理多个实例。子 agent 在宿主 Effector 工具内 `New → Stimulate`，对主循环透明。

### 7.3 多 agent 状态可见性：宿主侧三件套

> 框架事件流是「谁消费谁可见」：主循环看不到子 agent 的事件（扁平模型）。主/子 agent 状态统一可见由宿主侧三件套实现，**框架零改动**：StreamHub（共享状态容器）+ Thinker 包装（流式，可选）+ Hooks/工具（状态与 plan，必选）。

**StreamHub —— 共享状态容器（按 `Organs.ID` 分流）**

```go
type StreamHub struct {
	mu      sync.RWMutex
	streams map[string]chan meowire.Event // 流式输出（可选注入；无 channel = 静默）
	tasks   map[string]*TaskStatus        // 任务状态（必选，Hooks 写入）
	plans   map[string]string             // plan 树（update_plan 工具写入）
}
```

宿主创建**唯一实例**，所有 agent（主/子）创建时注入同一个 hub；UI 只读 hub 即可获得全部 agent 的实时视图。

**① 流式（可选）—— Thinker 包装内转发**

框架的 `EventText` 永远是整段文本（每轮 Think 的 `Decision.Text`），**token 级流式只能由宿主的 Thinker 实现产生**。宿主包装 Thinker，把 LLM 流式 API 的每个 token 同时推入 `hub.streams[id]`：

```go
type streamingThinker struct {
	inner llm.Client // 宿主的 LLM 流式客户端
	id    string     // = Organs.ID
	hub   *StreamHub
}

func (t *streamingThinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	ch := t.hub.channel(t.id) // 未注入 channel → nil → 静默
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
	return &meowire.Decision{Text: sb.String()}, nil // 完整文本返回框架
}
```

静默执行（子 agent 后台跑、不推流）：创建该 agent 时不注册 channel，`channel(id)` 返回 nil 自然不推。

**② 状态同步（必选）—— Hooks 回调写入**

宿主在 `Organs.Hooks` 里把状态写进 hub，框架在节点必然调用。id 通过闭包捕获（`AfterAct` 也可用 `a.CellID`）：

```go
hooksFor := func(id string) *meowire.Hooks {
	return &meowire.Hooks{
		AfterThink: func(ctx context.Context, d *meowire.Decision) error {
			hub.updateTask(id, TaskStatus{State: "thinking", LastText: d.Text})
			return nil
		},
		AfterAct: func(ctx context.Context, a *meowire.Action, e *meowire.Effect) {
			hub.updateTask(a.CellID, TaskStatus{State: "acting", LastTool: a.Call.Name})
		},
		OnCycleEnd: func(ctx context.Context, output string) {
			hub.updateTask(id, TaskStatus{State: "done", Output: output})
		},
	}
}
```

**③ plan 树 —— `update_plan` 工具 + `BeforeThink` 回灌**

计划内容由 LLM 生成（brain 决策），写入通道是宿主工具（host tool 模式）：

```
LLM 调 update_plan → 宿主 Effector 写 hub.plans[id] → UI 实时显示
              ↑                                          ↓
Hooks.BeforeThink 把最新 plan 写回 p.Plan ← LLM 下一轮看到自己的计划
```

```go
// 工具清单（LLM 可选调用）
{Name: "update_plan", Desc: "更新任务计划树(JSON)",
	Input: `{"type":"object","properties":{"nodes":{"type":"array"}}}`, Output: "ok"}

// Effector 分发：写 hub（宿主自己的 plan 树）
case "update_plan":
	hub.updatePlan(a.CellID, a.Call.Args)
	return &meowire.Effect{Result: "ok"}, nil

// Hooks.BeforeThink：回灌 p.Plan（指针可改，下一轮 Think 生效）
BeforeThink: func(ctx context.Context, p *meowire.Prompt) error {
	p.Plan = hub.plan(id) // 闭包捕获的 id
	return nil
},
```

**子 agent 统一视图**：宿主在 Effector 工具内 `New` 子 agent 时注入**同一个 hub 实例**，主/子状态自动汇聚：

```go
case "spawn_agent":
	sub, _ := meowire.New(meowire.Organs{
		ID:      args.ID,
		Think:   &streamingThinker{inner: t.inner, id: args.ID, hub: hub}, // 同一 hub
		Act:     sameEffector,
		Hooks:   hooksFor(args.ID), // 同一 hub 的闭包
		Closer:  closerStub,
		Sandbox: sandboxStub,
		Budget:  &meowire.ContextBudget{},
		Tools:   subTools,
	}, subCfg)
	// … 消费 sub 的事件流，或异步运行
```

---

## 8. 完整代码骨架

```go
package main

import (
	"context"
	"fmt"

	meowire "github.com/qyiun666/meowire/api"
)

// ① Thinker：封装 LLM
type llmThinker struct{ /* LLM client */ }

func (t *llmThinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msgs := buildMessages(p) // System/Identity/Methods/Tools/Context/Input/State/Plan → messages
	resp := t.client.Chat(ctx, msgs, toolSchemas(p.Tools))
	return &meowire.Decision{
		Text:      resp.Text,
		ToolCalls: resp.Tools,
		Usage:     resp.Usage,
	}, nil
}

// ② Effector：工具分发
type effector struct{ registry map[string]func(ctx context.Context, args string) (*meowire.Effect, error) }

func (e *effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
	fn, ok := e.registry[a.Call.Name]
	if !ok {
		return &meowire.Effect{Err: "unknown tool: " + a.Call.Name}, nil
	}
	return fn(ctx, a.Call.Args)
}

// ③ 组装 + 运行
func main() {
	agent, err := meowire.New(meowire.Organs{
		ID:      "agent-001",
		Think:   &llmThinker{},
		Act:     &effector{registry: toolRegistry},
		Closer:  &closer{},
		Hooks:   &meowire.Hooks{BeforeThink: injectMemory, OnCycleEnd: persistOutput},
		Sandbox: &sandbox{},
		Budget:  &meowire.ContextBudget{MaxTokens: 4000, Trimmer: trim},
		System:  "你是 meow agent，用中文回答",
		Tools: []meowire.ToolSpec{
			{Name: "calc", Desc: "计算器", Input: `{"type":"object","properties":{"expr":{"type":"string"}}}`, Output: "数值"},
		},
		Context:  loadHistory(ctx), // 宿主管理的历史（MemHop）
		Identity: "你叫 meow，角色 assistant，语气温暖",
		Methods:  []meowire.MethodSpec{{Name: "spawn_agent", Desc: "派生一个子 agent"}},
	}, meowire.Config{MaxRounds: 8, MaxToolOutput: 2000, MaxRetries: 2})
	if err != nil {
		panic(err) // 装配失败：缺端口
	}
	defer agent.Close()

	for ev := range agent.Stimulate(ctx, "帮我查一下订单") {
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
			handleError(ev.Err) // 含 ErrMaxRounds → 可 Step-Resume 续跑
		}
	}
}
```

---

## 9. 陷阱清单（集成前必读）

1. **`Hooks.BeforeThink` 必须整体替换 `p.Context`**——append 会因共享底层数组污染循环内上下文（§2.4）
2. **端口并发安全**：同一 Agent 并发 Stimulate 时 Thinker/Effector 被并发调用
3. **宿主端口必须响应 ctx 取消**，尤其 `ask_user` 阻塞场景（否则取消无法中断）
4. **`Organs.Context` 是切片**：每轮 Think 前记得经 `BeforeThink` 更新为最新历史（MemHop）
5. **`ErrMaxRounds` 不是 bug**：最后一轮有工具调用且轮数耗尽时抛出，配合 Step-Resume 让宿主续跑是预期用法
6. **事件流是观察镜像**：宿主不能往打开中的迭代器回喂数据；数据回喂走下一次 `Stimulate`
7. **流式 UX 在 Thinker 内做**：`EventText` 永远整段，token 增量不进事件流
8. **错误处理**：宿主端口返回的错误会被框架包装（`nerve.hookBeforeThink: ...` 等）后经 `EventError` 透出；判断错误类型用 `errors.Is`（如 `meowire.ErrMaxRounds`）
9. **hub 生命周期归宿主**（§7.3）：流式 channel 满会阻塞 agent 循环（Thinker 转发是同步的），宿主必须管理背压（buffer 大小）与回收（agent 结束后 close/删除 channel）；框架不参与
10. **子 agent 必须注入与主 agent 同一个 hub 实例**（§7.3）：换实例即失联，统一状态视图靠共享实例实现

## 10. 相关文档

| 文档 | 位置 | 内容 |
|------|------|------|
| README | [README.md](../README.md) | 英文快速入门 + 概念总览 |
| api 模块上下文 | [api/agent.md](../api/agent.md) | api 包长期上下文、关键决策、陷阱 |
| 决策循环实现 | [internal/nerve/loop.go](../internal/nerve/loop.go) | 循环编排源码（Think→Act→yield） |
| 集成测试 | [test/](../test/) | 端到端行为验证（生命周期、端口注入、事件序列） |
