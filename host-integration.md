# meowire 宿主集成指南

> 阅读对象：集成 meowire 的宿主（Host）开发 AI/开发者。
> 本文档是集成契约的唯一权威说明，所有接口签名、字段语义、事件序列均与源码一致。
> 模块路径：`github.com/qyiun666/meowire`，对外包：`github.com/qyiun666/meowire/api`（别名 `meowire`）。

## 0. 一句话模型

meowire 是一个**纯编排内核**：宿主实现七端口（LLM、工具、清理、钩子、权限门、上下文裁剪、经验记忆），框架负责 Think → Act → yield 事件循环。**框架不管理历史、不管理多 agent、不提供任何默认实现**——七端口全部必填，缺一报错。

宿主对外只需接触三个方法：`New`（装配）、`Stimulate`（运行一轮）、`Close`（关闭）。

## 1. 集成流程总览（6 步）

```
实现七端口 → 组装 Organs → 配置 Config → 装配 Blueprint → New() 创建 → Stimulate() 消费事件流 → Close()
```

| 步骤 | 做什么 | 关键点 |
|------|--------|--------|
| 1 | 实现 Thinker/Effector/Closer/Hooks/Sandbox/ContextBudget/Memory | 七端口 + 八回调全部必填（显式 no-op） |
| 2 | 组装 `Organs` 结构体 | 注入端口 + 固定上下文；Hooks 用 `FullHooks` 补全 |
| 3 | 设置 `Config` | 零值即默认，无需显式填 |
| 4 | 组装 `Blueprint{Organs, Config}` 并 `New(bp)` | 缺端口/缺回调返回错误；不完整 Budget 也报错 |
| 5 | `for ev := range agent.Stimulate(ctx, text)` | 消费事件流 |
| 6 | `agent.Close()` | 幂等，关闭后 Stimulate/Resume 返回 `ErrCellClosed` |

---

## 2. 第 1 步：实现七端口（宿主能力）

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
| `Context` | 上下文切片：宿主常驻基底 + 框架追加的 sandbox 裁决（`[sandbox-denied: ...]`，产出即最终形式）；工具结果不再进文本轨 | 宿主基底 + 框架追加 |
| `ToolResults` | 结构化工具结果（`ToolResult{ID, Name, Result, Err}`）：循环内累积，ID 为 LLM 返回的 `call_xxx`，Result/Err 为截断后的原始输出；渲染（tool 角色消息、`[tool_call_id=xxx]` 标记等）归宿主 Thinker | 框架，循环内追加 |
| `Bounds` | 执行边界描述（`Sandbox.Bounds()` 快照，如"只能访问 /workspace"） | 框架，每次 Stimulate 一次 |
| `Input` | 本次刺激文本（Stimulate 入参） | 框架，每轮动态 |
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
| `Effect.Result` | 成功结果文本（框架截断后写入 `ToolResults.Result`，渲染归宿主） |
| `Effect.Err` | 工具自身错误文本（框架截断后写入 `ToolResults.Err`；`Err` 非空 = 调用失败） |
| `Effect.WaitInput` | 非空 = 请求外部输入（字段即问题文本）；框架产出 `EventWaitInput`（携带 Session）并**正常结束本轮迭代器**，宿主收集响应后调 `agent.Resume(sess, response)` 续跑（见 §6.4） |
| 返回 error | 执行层错误（同样写入 `ToolResults.Err`，循环继续） |

**宿主职责：**
- 工具注册表 + 分发器：按 `Name` 路由、反序列化 `Args`、序列化结果
- 多 agent 工具在这里实现：`spawn_agent`（New → Stimulate → 返回结果）、`send_message`（跨 agent 消息，见 §7.2）——扁平模型约定
- `ask_user` 类工具：**返回 `&Effect{WaitInput: question}` 而非同步阻塞**（阻塞会拖住 Close 的等待；挂起由框架表达，UI 可见"猫在等人回答"），见 §6.4
- 工具失败返回 `Effect{Err: ...}` 而非 error 也可，两者都会作为反馈继续循环（**阻力是反馈不是失败**）

### 2.3 Closer —— 清理器

```go
type Closer interface {
    Close() error
}
```

释放 LLM 客户端、HTTP 连接等宿主资源。框架保证 Agent.Close 幂等（CAS），重复调用无副作用。

### 2.4 Pause / Unpause —— 快照挂起（v1.3.2 起与 ask_user 统一为单一挂起-恢复机制）

```go
agent.Pause()     // 请求暂停：下一个间隙点生效（Think 前 / 工具执行前）
agent.Unpause()   // 清除未生效的暂停请求（Pause 后、间隙点前反悔用）
```

- **间隙生效**：暂停请求不打断正在执行的 Think/Act；循环在「每轮 Think 前」与「每个工具执行前」两个间隙点检查
- **快照挂起（v1.3.2 起不再阻塞）**：暂停生效时事件流产出 `EventState(StatePaused)` → `EventPaused`（携带 Session 快照），**迭代器正常结束**；宿主用 `agent.Resume(sess, "")` 续跑（response 传空，无 pending 工具可注入）——与 ask_user 共用同一条 Session/Resume 路径（LangGraph-interrupt 式单一挂起原语）
- **工具间隙暂停**：暂停点在工具执行前，快照会把「当前工具及其后调用」记入 Session.remaining，Resume 先执行它们再回到 Think（当前工具未执行，不会丢失）
- 暂停状态是 **Agent 级、跨 Stimulate 保留**：暂停中再次 `Stimulate`，入口处先挂起（StatePaused + EventPaused）
- `Pause` / `Unpause` **幂等、并发安全**；`Close` 后调用为 no-op
- **`Resume` 自动清除暂停请求**：Resume 即继续意图，恢复后的循环不会在首个间隙点再次挂起；`Unpause` 只用于「暂停尚未生效时反悔」
- 与 Step-Resume 的区别：Step-Resume 放弃本轮、无状态重来；Pause **保留循环内状态（Session 快照）原地挂起**，恢复不占轮次

### 2.5 Hooks —— 拦截回调（全部八个必填；不需要行为时传显式 no-op，缺席即装配错误）

```go
type Hooks struct {
    BeforeStimulate func(ctx context.Context, p *Prompt) error // 一次 Stimulate 开始；可改原型全部内容字段，回写后全轮生效；error 终止整次 Stimulate
    AfterStimulate  func(ctx context.Context, output string)     // 一次 Stimulate 结束（正常/错误/提前停止均恰好一次）
    BeforeThink     func(ctx context.Context, p *Prompt) error
    AfterThink      func(ctx context.Context, d *Decision) error
    BeforeAct       func(ctx context.Context, a *Action) error
    AfterAct        func(ctx context.Context, a *Action, e *Effect, err error) // err 非 nil = 执行器失败
    OnError         func(ctx context.Context, err error)
    OnCycleEnd      func(ctx context.Context, output string, outcome CycleOutcome)
}
```

| 字段 | 触发时机 | 宿主典型用途 |
|------|---------|-------------|
| `BeforeStimulate` | 每次 Stimulate 开始、首事件前 | 回合级记忆 Recall（一次注入，全轮生效：改原型内容字段会回写循环） |
| `AfterStimulate` | 每次 Stimulate 结束（三路径恰好一次） | 回合级记忆 Save、会话结算 |
| `BeforeThink` | 每次 Think 前 | 动态注入检索记忆、更新 Plan |
| `AfterThink` | Think 成功后 | 序列化 Decision 回存 Plan/记忆 |
| `BeforeAct` | 每个工具执行前 | 审批、改写工具参数 |
| `AfterAct` | 工具执行后 | 工具日志、失败降级（`err` 非 nil 即执行器失败） |
| `OnError` | 不可恢复错误时 | 告警上报 |
| `OnCycleEnd` | **每轮恰好一次**（正常/错误/消费者提前停止三条路径都触发） | 结算、持久化最终输出；`outcome` 分类本轮结束方式（Done/Suspended/MaxRounds/Error/Aborted，零值保留 = 提前弃用迭代器 → Aborted） |

**⚠️ `BeforeThink` 必须整体替换 `p.Context`（`p.Context = append(p.Context[:0], newCtx...)` 或直接赋新切片）——它与循环上下文共享底层数组，直接 append 会污染 循环内上下文；`p.ToolResults` 同理（与循环结构化轨共享底层数组），整体替换、禁止原地 append。**

### 2.6 Sandbox —— 权限门（安全红线落点，守循环两侧）

```go
type Sandbox interface {
    Allow(ctx context.Context, a Action) (verdict Verdict, reason string, err error)
    Emit(ctx context.Context, u Utterance) (verdict Verdict, reason string, err error)
    Bounds() string // 执行边界描述（宿主定义），每次 Stimulate 快照一次
}
```

`Utterance{CellID, Round, Text}` 是本轮 Think 生成的文本。两侧同一套三态语法、同一条审计通道（`EventSandbox`）。

- `Allow` 三态裁决（`Verdict`）：`VerdictAllow` 放行执行；`VerdictDeny`（零值，fail-closed）时框架生成 `[sandbox-denied: reason]` 反馈进 Context，**循环继续**（不终止）；`VerdictAsk` **挂起征询**——reason 即展示给外部的问题文本，循环按 §6.4 的挂起-恢复协议挂起，宿主解决后批准才执行
- `Emit` 在**任何人听到这段文本之前**裁决（消费者、累积输出、下一轮 Think 都算"听到"）：`Allow` 原样说出；`Deny` 以 `[sandbox-denied: reason]` 取代该轮文本，该文本同时进 Context（大脑下一轮读得到自己的话被拒）；`Ask` **扣住草稿**按 §6.4 挂起，草稿随 `Session` 走线，批复后原样说出或被拒文本取代——**不重跑该轮 Think**
- 两侧 `err != nil` 都按 Deny 处理（fail-closed），反馈落库为 `[sandbox-denied: sandbox error: ...]`（审计记录 Reason 保留 `sandbox error: ...` 内层形态）
- 审计记录以 `Call` 区分两侧：工具侧带被门禁的 `Call`，文本侧 `Call` 为零值；每一裁决恰好一条，ask 链再以终结记录闭合
- `Bounds()` 返回执行边界描述，每次 Stimulate 快照一次、经 `Prompt.Bounds` 透传给 LLM（让大脑感知限制，如"只能访问 /workspace 下文件"）
- 宿主实现安全策略：工具白名单/黑名单、人工确认（返回 `VerdictAsk` 即可，挂起与恢复由框架表达）、敏感操作拦截、出口内容审查（`Emit`）

### 2.7 ContextBudget —— 上下文裁剪器

```go
type ContextBudget struct {
    MaxTokens   int
    Trimmer     func(ctx []string, max int) []string
    TrimResults func(results []ToolResult, max int) []ToolResult
}
```

- 每次 Think 前**同点同额度**调用两个裁剪器：`Trimmer` 裁文本轨 `Context`，`TrimResults` 裁结构化反馈轨 `ToolResults`——一轮内只有这两条轨在累积，其余 Prompt 字段每轮整体替换
- 是裁剪器不是硬停：超预算只剪不报错
- 不想裁剪时返回入参原切片即可（但仍须提供该函数：只给 `Trimmer` 的 Budget 装配失败）

### 2.8 Memory —— 经验端口

```go
type Memory interface {
    Recall(ctx context.Context, q MemoryQuery) ([]Record, error) // 每次 Think 前
    Remember(ctx context.Context, facts CycleFacts) error        // 每次调用的终点，恰好一次
}
```

- 框架只拥有**两个调用时点**：`Recall` 在预算裁剪之后、`BeforeThink` 钩子之前；`Remember` 在 `OnCycleEnd` 之前，四条终态路径（done / error / 挂起 / 消费者中途放弃）各恰好一次
- `Recall` 的结果进 `Prompt.Memories`（结构化轨，与 H3 的文本轨 `p.Context` 分属两轨）：**每轮整体替换、不累积、不进 `Session` 快照**——恢复的那一轮重新召回
- `MemoryQuery` 只带框架知道的两件事：谁在问（`CellID`）与这轮被问了什么（`Cue`）；检索算法、排序、取几条、留多久全归器官
- `CycleFacts` 只带框架构造得出的事实：`CellID` / `Input` / `Output` / `Outcome`；写什么、写成什么形状不经该端口，**删除与遗忘更不经过它**（那是宿主直接对自己的后端做的事）
- `Recall` 失败 → 该轮 Think 以 error 结束（器官是装配的一部分，框架不替宿主决定"少一半上下文也想"）；`Remember` 失败 → 不改写已定的终态，经 `OnError` 报告

---

## 3. 第 2 步：组装 `Organs`（装配根，唯一组装点）

```go
o := meowire.Organs{
    ID:      "agent-001",                       // 唯一标识，空 = "agent"
    Think:   myThinker,                         // 必填
    Act:     myEffector,                        // 必填
    Closer:  myCloser,                          // 必填
    Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: ...}), // 必填：全部八回调（工具函数填充缺失回调）
    Sandbox: mySandbox,                         // 必填
    Budget:  &meowire.ContextBudget{...},       // 必填
    Mem:     myMemory,                         // 必填

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
| `Hooks` | *Hooks | 拦截回调（全部八回调必填） | **是** |
| `Sandbox` | Sandbox | 权限门（`Allow` 守工具执行、`Emit` 守该轮文本出口） | **是** |
| `Budget` | *ContextBudget | 令牌调节器（文本轨 + 结构化反馈轨） | **是** |
| `Mem` | Memory | 经验端口（`Recall` 每轮 Think 前，`Remember` 每次调用终点） | **是** |
| `System` | string | 系统指令，进 Prompt.System | 否 |
| `Identity` | string | 身份描述文本（宿主自拼），进 Prompt.Identity | 否 |
| `Methods` | []MethodSpec | 内置能力描述（基因投影，仅描述不执行）；`MethodSpec{Name, Desc, Input, Output}`，进 Prompt.Methods | 否 |
| `Tools` | []ToolSpec | 工具清单，进 Prompt.Tools；`ToolSpec{Name, Desc, Input, Output}`，`Input` 为 JSON Schema（Thinker 据此生成 ToolCall.Args） | 否 |
| `Context` | []string | 常驻上下文基底（历史记忆在此注入，MemHop）；进 Prompt.Context 初始值 | 否 |

**注意：`New` 的必填校验逐回调检查——`Hooks` 的 8 个字段（H1–H8）任一为 nil 即装配失败（显式 no-op，而非缺席）。宿主可用 `FullHooks` 辅助（见下）快速补全。**

---

## 4. 第 3 步：`Config`（零值即默认，全部可选）

```go
type Config struct {
    MaxRounds      int
    MaxToolOutput  int
    MaxRetries     int
    ToolTimeout    time.Duration
    ToolMaxRetries int
    ParallelActs   bool
}
```

| 字段 | 语义 | 零值默认 |
|------|------|---------|
| `MaxRounds` | 硬性轮数上限；**最后一轮仍有工具调用时以 `ErrMaxRounds` 结束**（该轮工具结果不会被下一轮 Think 消化），配合 Step-Resume 防死循环 | 8 |
| `MaxToolOutput` | 工具反馈截断长度（按 UTF-8 安全截断，超长附 `[truncated, N bytes total]`） | 不截断 |
| `MaxRetries` | Think 重试次数（仅重试 Think；工具失败防护在宿主侧 Effector/AfterAct） | 不重试 |
| `ToolTimeout` | 单个工具执行超时（每次尝试独立计时；超时错误不重试，写入 `ToolResults.Err` 继续循环） | 无超时 |
| `ToolMaxRetries` | 工具执行失败重试次数（**仅执行器 error**；`Effect.Err` 不重试，防重复副作用） | 不重试 |
| `ParallelActs` | **同轮多工具批次并行（v1.3.3，opt-in）**：串行门控（逐条事件/沙箱裁决/BeforeAct）→ 并行 Act（含超时/重试）→ 串行反馈（按调用序，与完成顺序无关）。单调用恒走串行路径；**前提：Effector 实现并发安全** | 严格串行 |

**运行期热更新（v1.3.0）**：

```go
cfg := agent.GetConfig()  // 读当前值
cfg.MaxRounds = 20        // 只改想改的字段
agent.UpdateConfig(cfg)   // 整体替换：下一次 Stimulate/Resume 生效（飞行中的循环不受影响）
```

- 零值语义与 `New` 完全一致：**整体替换**，未写字段会回落到默认值——务必读-改-写（如上）
- 与 `Replace` 分工：`Replace` 换端口（能力），`UpdateConfig` 调标量（参数）

---

## 5. 第 4 步：`New` 装配校验

```go
type Blueprint struct {
    Organs Organs   // 七端口 + 固定上下文（全部必填）
    Config Config   // 零值即默认
}

bp := meowire.Blueprint{Organs: organs, Config: cfg}
agent, err := meowire.New(bp)
```

- **Blueprint 是一次定义、多次装配**：同一 `bp` 可 `New` 出多个独立 Agent 实例（flat model 多 agent 场景）
- `New` 基于**装配图**校验（蓝图 = 数据对象节点 + 槽位边），只有两档：
  - `error` 级（必填端口缺失 / 回调缺失 / Budget 不完整）：**恒阻断**，返回 `meow: required port X not injected`（X ∈ Think/Act/Closer/Hooks/Sandbox/Budget/H1–H8），多缺联合报错
  - `info` 级（空 Identity/Tools/Context、默认轮数）：**永不阻断**，用 `meowire.Validate(organs, cfg)` 显式查看
  - 不再有 `warn` 级：每个接线点都是必填，缺失即缺失器官，没有“半配放行”
- 装配后宿主调用 `Stimulate` / `Resume` / `Pause` / `Unpause` / `Close`，并可经 `Replace` 运行时换端口、经 `AgentCard` 导出能力卡（见下）
- 七端口 + 八回调均须由宿主实现，**没有 stub、没有默认实现、没有"最小可运行"路径**；宿主可用 `meowire.FullHooks(...)` 把不需要的钩子声明为显式 no-op

### 5.1 动态接线：`Replace`（运行时换器官）

```go
oldThink, err := agent.Replace(meowire.SlotThink, myOtherLLM) // 下次 Stimulate 生效
```

- 可换槽位：`SlotThink` / `SlotAct` / `SlotSandbox` / `SlotBudget` / `SlotMem` / `SlotHooks`（槽名单一事实源是蓝图 `WirePoint.Slot`，`Connectome()`/`SwappableSlots()` 可枚举，常量与之由测试钉死）；`Closer`（资源绑定）与 `PauseGate`（框架接线）不可换
- 语义：每次 `Stimulate` 快照端口构造全新 LoopContext——**飞行中的 Stimulate 不受影响**，替换只在下次生效；返回被换下的端口（它持有的资源何时释放由宿主决定，框架不代关）
- 并发安全；`Close` 后为 no-op；**拒绝 nil/不完整端口**（Budget 需 Trimmer+TrimResults+MaxTokens、Hooks 需八回调）；槽位或端口类型错误返回 error
- **审计事件（v1.3.0）**：每次成功替换记录一条 `ReplaceAudit{CellID, Slot, Old, New}`，在**下一次 Stimulate/Resume 开头（生效时刻）**以 `EventReplace` 产出（与 `EventSandbox` 同级可持久化审计）；失败替换不记录；无替换零产出。宿主模型切换审计闭环：从事件流更新 activeModel，不再手工维护状态机

### 5.2 能力卡：`AgentCard`（A2A 风格）

```go
card, _ := meowire.AgentCard(organs) // JSON：name/description/skills
```

由装配（`ID`/`Identity`/`Methods`）投影得到机器可读能力声明，宿主发布到 `/.well-known/agent-card.json` 即被其他 agent 发现。详见 [protocols.md](protocols.md) §2。

### 5.3 合成视图：`BuildComposite`（静态装配 × 动态突触，一张图）

```go
colony := meowire.NewDirect(resolver) // 宿主域 Synapse（可塑突触图）
// ...运行中 Link/Fire/Reinforce...

text, _ := meowire.RenderComposite(ctx, organs, colony) // ASCII：内部节点/插槽边 + 外部 agent/突触边
snap, _ := meowire.RenderCompositeJSON(ctx, organs, colony) // JSON：机器可读快照
```

- 内部子图：静态装配（12 数据节点 + 18 插槽边），外部子图：实时突触边（权重 + 传递计数）
- 弱突触（权重 < 0.3）标记 `! weak`，供宿主周期性 `Prune` 修剪审查
- 视图统一、数据分离：内部装配与外部连接各自存储，渲染时才合成
- 宿主把 `RenderCompositeJSON` 快照持久化，即得整个 agent 群体的统一可观测性视图

---

## 6. 第 5 步：`Stimulate` 消费事件流

`Stimulate(ctx context.Context, text string) iter.Seq[Event]`，宿主用 `for ev := range agent.Stimulate(...)` 消费。

### 6.1 事件序列

**无工具单轮（5 个事件）：**

```
EventState(thinking) → [EventUsage(可选)] → EventSandbox(文本裁决) → EventText → EventState(done) → EventDone(累计输出)
```

**带工具调用（每轮插入，可多轮循环）：**

```
EventState(thinking) → [EventUsage] → EventSandbox(文本裁决) → EventText → EventState(acting)
  → (EventToolCall → EventSandbox(工具裁决) → EventToolResult) × N → 回到 EventState(thinking) → …
```

**暂停路径（间隙点生效，快照挂起：迭代器正常结束，Resume 续跑）：**

```
… → EventState(paused) → EventPaused(Session 快照) → 迭代器正常结束（无 Done/Error）
→ 宿主调 Resume(sess, "") → 剩余工具先执行 → 回到 EventState(thinking) → 继续原序列
```

**挂起路径（工具返回 `Effect.WaitInput`，迭代器正常结束；见 §6.4）：**

```
… → EventState(acting) → EventToolCall → EventSandbox → EventState(waiting)
  → EventWaitInput(工具名 + 问题 + Session) → 迭代器正常结束（无 Done/Error）
```

**文本征询挂起路径（`Sandbox.Emit` 返回 `VerdictAsk`，草稿从未产出；见 §6.4）：**

```
EventState(thinking) → [EventUsage] → EventSandbox(ask) → EventState(waiting)
  → EventWaitInput(问题 + 扣住的草稿在 Session 内，Call 零值) → 迭代器正常结束
→ 宿主 Resume(sess, 批复) → EventSandbox(终结) → EventText(原稿或被拒文本)
  → [该轮工具照常执行 → 回到 EventState(thinking)] 或 [无事可做 → EventState(done) → EventDone]
```

**端口替换审计（下一次 Stimulate/Resume 开头、首个事件前，多条按序）：**

```
EventReplace(slot/old/new) → 后续正常序列
```

**错误路径：**

```
EventState(error) → EventError(Err)
```

**关闭后 Stimulate/Resume：** 直接产出 `EventError(ErrCellClosed)`。

### 6.2 `Event` 字段（按 Kind 生效，其余为零值）

| Kind | 有效字段 | 内容 |
|------|---------|------|
| `EventText` | `Text` | 该轮文本，**已过出口膜**（`Emit` 拒绝时为 `[sandbox-denied: reason]`） |
| `EventToolCall` | `ToolCall *ToolCall` | LLM 决定调用的工具 |
| `EventToolResult` | `Effect *Effect`, `ToolCall *ToolCall` | 工具执行结果（含被 Sandbox 拒绝：`Effect.Err = "[sandbox-denied: reason]"`）；`ToolCall` 回显调用，用于 ID 关联 |
| `EventSandbox` | `Verdict *SandboxVerdict` | 膜的裁决审计记录（裁决 Ruling：allow/deny/**ask**、被门禁的工具或零值=文本侧、策略原因、征询问题、评估错误）；循环两侧每一裁决恰好一条，ask 链以终结的第二条记录闭合 |
| `EventState` | `State LoopState` | 循环状态（idle/thinking/acting/paused/**waiting**/done/error） |
| `EventDone` | `Output` | 整轮累计文本输出 |
| `EventError` | `Err` | 不可恢复错误（含 `ErrMaxRounds`、`ErrCellClosed`） |
| `EventUsage` | `Usage *Usage` | 最近一次 Think 的 token 用量 |
| `EventWaitInput` | `Wait *WaitInput` | 循环等外部输入：`WaitInput{CellID, Call, Question, Session}`——三种成因（工具自请求 `Call`+`Question`、执行前征询 `Call`+问题、文本征询 `Call` 零值+问题）；宿主**保存 Session**、展示问题，取得响应后调 `agent.Resume(sess, response)` |
| `EventPaused` | `Wait *WaitInput` | 暂停请求生效：`WaitInput{CellID, Session}`（Call 零值、Question 空）——宿主保存 Session，调 `agent.Resume(sess, "")` 续跑（与其他挂起同一条通道） |
| `EventReplace` | `Replace *ReplaceAudit` | 端口替换审计：`ReplaceAudit{CellID, Slot, Old, New}`；下一次 Stimulate/Resume 开头（生效时刻）按序产出，可持久化 |
| `EventConfig` | `Config *ConfigAudit` | 配置整包替换审计：`ConfigAudit{CellID, Old LoopConfig, New LoopConfig}`；下一次 Stimulate/Resume 开头在 EventReplace 之后按序产出，可持久化 |

`SandboxVerdict{CellID, Call, Ruling Verdict, Reason, Question, Err}`：膜在循环两侧的每一次裁决各产出一条（`Ruling` 三态：Deny 为零值，fail-closed），`Call` 为工具侧被门禁的调用、零值即文本侧。ask 裁决再产出终结记录闭合审计链（拒绝含拒绝文本，批准 Ruling=allow 且 Reason 空）。宿主持久化事件流即得到审计日志（谁、代表谁、何时、做了什么、为什么被允许）。详见 [protocols.md](protocols.md) §4 Authority。

### 6.3 宿主必须掌握的两个语义

1. **提前停止（Step-Resume 基础，宿主主动接管）**：`for range` 中 break/return 即放弃本轮——停止点及之后的工具**不会执行**，本轮累积状态全部丢弃。宿主主动接管（人工审批、异步任务、`ErrMaxRounds` 续跑）用此路径：自行保存进度，再调用 `Stimulate` 继续。**注意：工具请求外部输入（ask_user）的唯一形式是 §6.4 的 `WaitInput` + `Resume` 协议——禁止用 break + `Stimulate` 模拟**（`Session` 不透明，挂起上下文无法手工重建）。
2. **无状态 step**：每次 `Stimulate` 是一次无状态 step，无跨调用状态残留。宿主自己保存历史，下次通过 `Organs.Context`（或 `Hooks.BeforeThink`）放回去。

**两条路径的边界**：工具请求输入（ask_user）只能走 §6.4 的挂起-恢复——框架保留 `Session`、不占轮次，宿主保存会话、展示问题，响应到达后 `Resume` 即可；宿主主动接管（人工审批、异步任务、`ErrMaxRounds` 续跑）才用 break + `Stimulate`——本轮状态丢弃，进度由宿主自行保存并重新注入。两者不可互相替代：break 只是 Go 迭代器的标准消费语义（停止点之后的工具不会执行），不是另一套实现，用 break 模拟 ask_user 会丢失挂起上下文。

### 6.4 挂起-恢复协议（工具请求输入与膜的两侧征询）

工具请求外部输入、`Sandbox.Allow` 或 `Sandbox.Emit` 返回 `VerdictAsk` 征询确认时的框架级协议——**不阻塞、不丢轮**，替代宿主在 Effector 内同步阻塞的做法：

```go
// ① 工具侧（Effector）：声明挂起，绝不阻塞
func (e *effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
    if a.Call.Name == "ask_user" {
        return &meowire.Effect{WaitInput: "可以删除这个文件吗？"}, nil
    }
    ...
}
```

```go
// ② 宿主侧：保存 Session，展示问题，收集响应后 Resume（与 Stimulate 同构消费）
var sess meowire.Session
for ev := range agent.Stimulate(ctx, "整理桌面") {
    switch ev.Kind {
    case meowire.EventWaitInput:
        sess = ev.Wait.Session            // 保存会话句柄
        ui.Show(ev.Wait.Question)         // “猫在等人回答”
        go func() {                       // 宿主自行控制超时（默认拒绝）
            select {
            case ans := <-ui.Answer():
                answerCh <- ans
            case <-time.After(60 * time.Second):
                answerCh <- "[denied: timeout]"
            }
        }()
    }
}
ans := <-answerCh
for ev := range agent.Resume(ctx, sess, ans) {  // 事件流与 Stimulate 同构
    ...
}
```

**语义：**

- **挂起 = 迭代器正常结束**：yield `EventState(StateWaiting)` → `EventWaitInput` 后结束，无 `EventDone`/`EventError`；`OnCycleEnd`/`AfterStimulate` 照常恰好一次，outcome 为 `OutcomeSuspended`（宿主凭 StateWaiting/outcome 区分挂起收尾，**勿沉淀未完成轮次**）
- **执行前征询（`Allow` = Ask）= 同一挂起机制**：循环产出相同的 `EventState(StateWaiting)` + `EventWaitInput`（问题文本来自裁决 reason），且事先产出一条 ask 型 `EventSandbox` 审计记录；宿主用与 ask_user 完全相同的方式保存 Session、展示问题、Resume 解决；**批准的调用不再重新过门禁**（无状态膜会无限重问），同轮其余调用重放时照常过门禁；终结的 resolve 记录闭合审计链
- **文本征询（`Emit` = Ask）= 同一挂起机制**：该轮草稿被扣住，事件流里读不到它（`EventWaitInput.Call` 为零值，问题文本来自裁决 reason），草稿随 `Session` 走线；批复后按原稿说出（**不重跑该轮 Think**）或以 `[sandbox-denied: ...]` 取代并进 Context；该轮已声明的工具调用排在草稿之后照常执行，若该轮别无待办则直接收尾 Done
- **暂停（v1.3.2）= 同一挂起机制**：yield `EventState(StatePaused)` → `EventPaused`（Session 快照，Call 零值）后迭代器正常结束；`agent.Resume(sess, "")` 续跑，不注入任何结果（无 pending 工具）；暂停点在工具执行前时，当前工具记入 Session.remaining，Resume 先执行
- **`Session` 是不透明值对象**（`round`/Context/剩余工具调用/累积输出快照）：宿主只保存、传回，不碰内部；**归属明确**——`Resume` 拒绝别的 cell 交回的句柄（`ErrForeignSession`，重放等于拿 B 的器官跑 A 的那一轮）；**单次消费**——重复 Resume 会重复执行剩余工具（副作用重复，宿主责任）
- **`sess.RemainingCalls()`**：唯一授权的只读探测——返回挂起点尚未执行的调用克隆（无则空）：暂停快照保留整批未执行调用（Resume 重放）；`ParallelActs` 批内 WaitInput 挂起则为空（整批已执行完，Resume 只注入响应，绝不重放）
- **持久化**：`sess.Marshal()` 产出 JSON 字节（version 3：句柄归属的 cell、挂起成因、被扣住的草稿都在其上），宿主存盘；重启后 `meowire.UnmarshalSession(data)` 还原句柄再 Resume——挂起/暂停跨进程可恢复；版本或挂起成因不认识的名字拒绝还原（防止旧/新格式误重放）
- **不占轮次**：恢复后从挂起轮继续，消化响应的 Think 使用挂起轮的配额（`MaxRounds` 不额外扣减）
- **不触发 budget**：等待期间无 Think，两个裁剪器都不调用；恢复后下一轮 Think 前才执行
- **响应语法（膜的两侧按三态裁决，ask_user 原样注入）**：**空串** = 拒绝（执行前征询落 `[sandbox-denied: declined]` 反馈、文本征询以该文本取代草稿）；**`[denied:` 前缀** = 以该文本拒绝（超时配方如上 `[denied: timeout]`，落库为 `[sandbox-denied: ...]` 规范形式）；**其余任何响应** = 批准——执行前征询放行 pending 调用执行（不再过门禁），文本征询按原稿说出。**ask_user** 的响应原样作为挂起工具的结构化结果写入恢复后的 `ToolResults`（`ID` 为该调用的 `call_xxx`，进入恢复后的第一次 Think；空串即空结果；拒绝语义由宿主在响应文本中表达，超时配方 `[denied: timeout]` 作为工具反馈被模型读到）。超时由宿主控制（默认拒绝）
- **剩余工具**：挂起发生在多工具轮中间时，恢复后先执行该轮剩余工具，再进入 Think
- **Resume 的 hooks 与 Stimulate 完全一致**（`BeforeStimulate` 照常触发，宿主 append 语义下 `Session.Context` 与检索结果自然合并）；`Close` 后 Resume 产出 `ErrCellClosed`；`Session` 为内存态句柄，宿主重启后失效（按超时拒绝处理）
- **与 Say 注入分工**：`Say`/`BeforeThink` 注入的是新消息（`p.Input`），`Resume` 注入的是挂起响应（作为挂起工具的结构化结果进 `ToolResults`）——两者无重叠

---

## 7. 第 6 步：`Close` 与可选扩展

### 7.1 `Close() error`

幂等（CAS 保证）；依次关闭 cell + 宿主 Closer，错误用 `errors.Join` 聚合；关闭后 Stimulate/Resume 返回 `ErrCellClosed`。宿主应 `defer agent.Close()`。

### 7.2 可选扩展

**Synapse（多 agent 消息参考实现，1.1.1 起为可塑突触图）**：

```go
type Edge struct {
    From   string
    To     string
    Weight float64 // 突触强度（宿主学习规则读写）
    Fired  int64   // 累计成功传递次数（宿主统计）
}

type Synapse interface {
    Link(ctx, from, to string, weight float64) error // 突触发生（幂等覆盖，clamp ≥ 0）
    Unlink(ctx, from, to string) error               // 突触消除（不存在 → ErrNotLinked）
    Reinforce(ctx, from, to string, delta float64) error // LTP/LTD（结果 clamp ≥ 0）
    Fire(ctx, sig Signal) error                      // 信号传递（成功投递累计 Fired）
    Edges(ctx, from string) ([]Edge, error)          // 出边/全图快照（持久化原语）
}
```

- `SignalKind`：`KindStimulus`（宿主发任务）/`KindResponse`（agent 回复）/`KindNotice`（旁路通知，不进决策循环）
- 真正路由（channel/HTTP/Redis/gRPC）宿主自选；宿主工具 `send_message` 经 `synapse.Fire` 发送，阻力（目标忙/未知 agent）以 `EventToolResult` 反馈回循环，不硬停
- synapse 错误（`ErrNoTarget`/`ErrNotLinked`/`ErrTargetBusy`）在 api 层 re-export；`Edge` 同步 re-export
- **持久化往返**：运行期 `Edges(ctx, "")` 导出全图 → 宿主序列化存盘；下次启动反序列化 → `NewDirect(resolver, restored...)` 注入初始边。学习规则（赫布/STDP）宿主实现，框架只存状态不决策

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
		AfterAct: func(ctx context.Context, a *meowire.Action, e *meowire.Effect, err error) {
			hub.updateTask(a.CellID, TaskStatus{State: "acting", LastTool: a.Call.Name})
		},
		OnCycleEnd: func(ctx context.Context, output string, outcome meowire.CycleOutcome) {
			switch outcome {
			case meowire.OutcomeDone:
				hub.updateTask(id, TaskStatus{State: "done", Output: output})
			case meowire.OutcomeSuspended:
				hub.updateTask(id, TaskStatus{State: "needs-input"})
			default:
				hub.updateTask(id, TaskStatus{State: "failed"})
			}
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
	// 同一 hub 注入子 agent；Blueprint 一次定义多处 New（flat model）
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      args.ID,
			Think:   &streamingThinker{inner: t.inner, id: args.ID, hub: hub}, // 同一 hub
			Act:     sameEffector,
			Hooks:   hooksFor(args.ID), // 同一 hub 的闭包
			Closer:  closerStub,
			Sandbox: sandboxStub,
			Budget:  passBudget, // 直通裁剪器：Trimmer + TrimResults + MaxTokens>0 三者齐备
			Tools:   subTools,
		},
		Config: subCfg,
	}
	sub, _ := meowire.New(bp)
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
	msgs := buildMessages(p) // System/Identity/Methods/Tools/Context/Bounds/Input/Plan → messages
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

// ③ 组装 + 运行（Blueprint 一次定义，多实例复用）
func main() {
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-001",
			Think:   &llmThinker{},
			Act:     &effector{registry: toolRegistry},
			Closer:  &closer{},
			Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: injectMemory, OnCycleEnd: persistOutput}),
			Sandbox: &sandbox{},
			Budget:  &meowire.ContextBudget{MaxTokens: 4000, Trimmer: trim, TrimResults: trimResults},
			System:  "你是 meow agent，用中文回答",
			Tools: []meowire.ToolSpec{
				{Name: "calc", Desc: "计算器", Input: `{"type":"object","properties":{"expr":{"type":"string"}}}`, Output: "数值"},
			},
			Context:  loadHistory(ctx), // 宿主管理的历史（MemHop）
			Identity: "你叫 meow，角色 assistant，语气温暖",
			Methods:  []meowire.MethodSpec{{Name: "spawn_agent", Desc: "派生一个子 agent"}},
		},
		Config: meowire.Config{MaxRounds: 8, MaxToolOutput: 2000, MaxRetries: 2},
	}
	agent, err := meowire.New(bp)
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

1. **`Hooks.BeforeThink` 必须整体替换 `p.Context`**——append 会因共享底层数组污染循环内上下文（§2.5）
2. **端口并发安全**：同一 Agent 并发 Stimulate 时 Thinker/Effector 被并发调用
3. **宿主端口必须响应 ctx 取消**；`ask_user` 不得在 Effector 内同步阻塞（框架级挂起协议见 §6.4），宿主侧等待超时自行控制（默认拒绝 `[denied: timeout]`）
4. **`Organs.Context` 是切片**：每轮 Think 前记得经 `BeforeThink` 更新为最新历史（MemHop）
5. **`ErrMaxRounds` 不是 bug**：最后一轮有工具调用且轮数耗尽时抛出，配合 Step-Resume 让宿主续跑是预期用法
6. **事件流是观察镜像**：宿主不能往打开中的迭代器回喂数据；数据回喂走下一次 `Stimulate`
7. **流式 UX 在 Thinker 内做**：`EventText` 永远整段，token 增量不进事件流
8. **错误处理**：宿主端口返回的错误会被框架包装（`nerve.hookBeforeThink: ...` 等）后经 `EventError` 透出；判断错误类型用 `errors.Is`（如 `meowire.ErrMaxRounds`）
9. **暂停不打断执行中的工具**：Pause 在间隙点生效（v1.3.2 起为快照挂起，`EventPaused` 后迭代器结束、`Resume(sess, "")` 续跑）；如需中断正在执行的工具，用 ctx 取消（§2.4）
10. **hub 生命周期归宿主**（§7.3）：流式 channel 满会阻塞 agent 循环（Thinker 转发是同步的），宿主必须管理背压（buffer 大小）与回收（agent 结束后 close/删除 channel）；框架不参与
11. **子 agent 必须注入与主 agent 同一个 hub 实例**（§7.3）：换实例即失联，统一状态视图靠共享实例实现

## 10. 相关文档

| 文档 | 位置 | 内容 |
|------|------|------|
| 宿主参考实现 | [reference-host.md](reference-host.md) | 按步骤从零实现一个 AI 宿主（LLM 对接/工具/权限/记忆/多 agent/持久化） |
| README | [README.md](README.md) | 英文快速入门 + 概念总览 |
| api 模块上下文 | [api/agent.md](api/agent.md) | api 包长期上下文、关键 决策、陷阱 |
| 决策循环实现 | [internal/nerve/loop.go](internal/nerve/loop.go) | 循环 编排源码（Think→Act→yield） |
| 集成测试 | [test/](test/) | 端到端行为验证（生命周期、端口注入、事件序 列） |
