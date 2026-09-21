# meowire 宿主集成指南

> 阅读对象：集成 meowire 的宿主（Host）开发 AI/开发者。
> 本文档是集成契约的唯一权威说明，所有接口签名、字段语义、事件序列均与源码一致。
> 模块路径：`github.com/qyiun666/meowire`，对外包：`github.com/qyiun666/meowire/api`（别名 `meowire`）。

## 0. 一句话模型

meowire 是一个**自带大脑的编排内核**：大脑内置（openai-go，宿主经 `Organs.Brain` 传 BaseURL/Key/Model/Stream/Mode 五个参数即得，指向任意 OpenAI 兼容端点；`Mode` 选 wire——`BrainModeChat`（默认，零值同 1）走 chat completions，`BrainModeResponses` 走 Responses API，两根 wire 渲染同一份无状态 Prompt、折回同一个 Decision），宿主实现六端口（工具、清理、钩子、权限门、上下文裁剪、经验记忆），框架负责 Think → Act → yield 事件循环。**框架不管理历史、不管理多 agent**——六端口 + Brain 参数全部必填，缺一报错。

最小闭环只需三个入口：`New`（装配）、`Stimulate`（运行一轮）、`Close`（关闭）；要挂起-恢复、暂停、运行时换器官或调参数，用 `Resume` / `Pause` / `Unpause` / `Replace` / `UpdateConfig`（见 §2.4、§5.1、§6.4）。

## 1. 集成流程总览（6 步）

```
填 Brain 参数 → 实现六端口 → 组装 Organs → 配置 Config → 装配 Blueprint → New() 创建 → Stimulate() 消费事件流 → Close()
```

| 步骤 | 做什么 | 关键点 |
|------|--------|--------|
| 1 | 填 `Brain` 参数；实现 Effector/Closer/Hooks/Sandbox/ContextBudget/Memory | 大脑传参即得；六端口 + 八回调全部必填（显式 no-op） |
| 2 | 组装 `Organs` 结构体 | 注入端口 + 固定上下文；Hooks 用 `FullHooks` 补全 |
| 3 | 设置 `Config` | 零值即默认，无需显式填 |
| 4 | 组装 `Blueprint{Organs, Config}` 并 `New(bp)` | 缺端口/缺回调返回错误；不完整 Budget 也报错 |
| 5 | `for ev := range agent.Stimulate(ctx, text)` | 消费事件流 |
| 6 | `agent.Close()` | 幂等，关闭后 Stimulate/Resume 产出 `EventError`（`ErrCellClosed`） |

---

## 2. 第 1 步：填 Brain 参数、实现六端口（宿主能力）

### 2.1 Brain —— 内置大脑（传参即得，宿主不实现）

大脑是框架唯一内置的器官：宿主在 `Organs.Brain` 填五个参数，组合根构造并注入，循环以 Thinker 身份调用它。宿主不写 Thinker、不 import 任何 SDK。

```go
type BrainConfig struct {
    BaseURL string     // OpenAI 兼容端点（空 = 官方端点）
    Key     string     // 凭证（必填）
    Model   string     // 模型 id（必填）
    Mode    BrainMode  // 线协议：BrainModeChat（默认，零值亦是）/ BrainModeResponses
    Stream  bool       // true = SSE 传输
}
```

流式增量走 `meowire.WithSink(ctx, sink)`：把 Sink 挂在交给 `Stimulate`/`Resume` 的 context 上，大脑的文本 delta 在出口膜裁决**之前**送达（推流 + 更正策略：`EventText` 整段到达时替换已推内容）；事件流只承载整段 Text。重试单层：传输侧重试关闭，预算全归 `Config.MaxRetries`。无工具的轮（`Prompt.Tools` 为空）不发送 `tools` 字段——两根 wire 都省略整个字段而非发空数组（部分 OpenAI 兼容端点会直接拒绝空数组）。

**进脑的数据包 `Prompt`（框架组装；`BeforeStimulate`/`BeforeThink` 钩子可改写）字段：**

| 字段 | 内容 | 注入方 |
|------|------|--------|
| `System` | 系统指令（"你是客服助手…"） | 宿主，构造时固定 |
| `Identity` | 身份描述文本（宿主自拼，如“你叫 meow，角色 assistant，语气温暖”） | 宿主，构造时固定 |
| `Methods` | 内置能力描述（只描述、框架不消费；`MethodSpec{Name, Desc, Input, Output}`） | 宿主，构造时固定 |
| `Tools` | 可用工具清单（function schema） | 宿主，构造时固定 |
| `Context` | 上下文切片：宿主常驻基底 + 框架追加的 sandbox 裁决（`[sandbox-denied: ...]`，产出即最终形式）；工具结果不进文本轨 | 宿主基底 + 框架追加 |
| `ToolResults` | 结构化工具结果（`ToolResult{ID, Name, Result, Err}`）：循环内累积，ID 为 LLM 返回的 `call_xxx`，Result/Err 为截断后的原始输出；渲染（tool 角色消息、`[tool_call_id=xxx]` 标记等）归内置大脑 | 框架，循环内追加 |
| `Bounds` | 执行边界描述（`Sandbox.Bounds()` 快照，如"只能访问 /workspace"） | 框架，每次 Stimulate/Resume 一次 |
| `Reflection` | 上一轮的自检笔记（ Reflexion 槽位，逐字透传） | 宿主（`BeforeStimulate` 写回） |
| `Memories` | 本轮召回的经验记录（`Memory.Recall` 输出；整轮替换，不累积、不进快照） | 框架，每次 Think 前 |
| `Input` | 本次刺激文本（Stimulate 入参） | 框架，每轮动态 |
| `Plan` | 任务计划/进度文本 | 宿主经 `Hooks.BeforeThink` 写 `p.Plan`（指针可改，同轮紧随的 Think 即读到）；配合宿主 `update_plan` 工具形成闭环（§7.3 ③，工具写进宿主 hub 的值在下一轮被钩子读出） |

**出脑的裁决 `Decision` 字段（`AfterThink` 钩子可观察）：**

| 字段 | 内容 | 约束 |
|------|------|------|
| `Text` | LLM 文本输出（整段） | 框架以 EventText 透出，**token 增量不进事件流** |
| `ToolCalls` | 本轮工具调用列表 `{ID, Name, Args}` | `Args` 为 JSON 字符串；**空 = 循环结束** |
| `Usage` | token 用量 `{Prompt, Completion, Total}` | **nil = 跳过 EventUsage 计费事件** |

以上渲染与传输的逐字段说明（assistant/tool 配对、重试取舍、流式与出口膜的时序）写在
[thinker-openai-go.md](thinker-openai-go.md)——它是内置大脑的实现规格与协议参考，也是校验 `Brain` 行为的文档。

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
| `Effect.Result` | 成功结果文本（框架截断后写入 `ToolResults.Result`，渲染归内置大脑） |
| `Effect.Err` | 工具自身错误文本（框架截断后写入 `ToolResults.Err`；`Err` 非空 = 调用失败） |
| `Effect.WaitInput` | 非空 = 请求外部输入（字段即问题文本）；框架产出 `EventWaitInput`（携带 Session）并**正常结束本轮迭代器**，宿主收集答复后调 `agent.Resume(sess, meowire.Response{Answer: 文本})` 续跑（见 §6.4） |
| 返回 error | 执行层错误（同样写入 `ToolResults.Err`，循环继续） |

**宿主职责：**
- 工具注册表 + 分发器：按 `Name` 路由、反序列化 `Args`、序列化结果
- 多 agent 工具在这里实现：`spawn_agent` 这个工具自己 `New` 一个 `Agent`、消费它的 `Stimulate` 事件流、把结果作为普通 `Effect.Result` 交回主循环（见 §7.2）——内核不知道有第二个实例
- `ask_user` 类工具：**返回 `&Effect{WaitInput: question}` 而非同步阻塞**（阻塞会拖住 Close 的等待；挂起由框架表达，UI 可见"猫在等人回答"），见 §6.4
- 工具失败返回 `Effect{Err: ...}` 而非 error 也可，两者都会作为反馈继续循环（**阻力是反馈不是失败**）

### 2.3 Closer —— 清理器

```go
type Closer interface {
    Close() error
}
```

释放 LLM 客户端、HTTP 连接等宿主资源。框架保证 Agent.Close 幂等（CAS），重复调用无副作用。

### 2.4 Pause / Unpause —— 快照挂起（与 ask_user 统一为单一挂起-恢复机制）

```go
agent.Pause()     // 请求暂停：下一个间隙点生效（Think 前 / 工具执行前）
agent.Unpause()   // 清除未生效的暂停请求（Pause 后、间隙点前反悔用）
```

- **间隙生效**：暂停请求不打断正在执行的 Think/Act；循环在「每轮 Think 前」与「每个工具执行前」两个间隙点检查（开启 `ParallelActs` 的并行批次只在**整批之前**留一个间隙点，命中的快照带上整批未执行的调用）
- **快照挂起（不阻塞）**：暂停生效时事件流产出 `EventState(StatePaused)` → `EventPaused`（携带 Session 快照），**迭代器正常结束**；宿主用 `agent.Resume(sess, meowire.Response{})` 续跑（pause 什么都没被问，Response 两个字段都不读）——与 ask_user 共用同一条 Session/Resume 路径（LangGraph-interrupt 式单一挂起原语）
- **工具间隙暂停**：暂停点在工具执行前，快照会把「当前工具及其后调用」记入 Session.remaining，Resume 先执行它们再回到 Think（当前工具未执行，不会丢失）
- 暂停状态是 **Agent 级、跨 Stimulate 保留**：暂停中再次 `Stimulate`，入口处先挂起（StatePaused + EventPaused）
- `Pause` / `Unpause` **幂等、并发安全**；`Close` 后调用为 no-op
- **`Resume` 只清除它所恢复那次暂停的请求**：恢复 pause 挂起即继续意图，循环不会再在首个间隙点重复挂起；恢复其余三种成因（工具请求输入、两侧征询）时**不动**暂停请求——那个请求属于当下正在跑的循环，被无关的应答吃掉就是静默丢失。`Unpause` 只用于「暂停尚未生效时反悔」
- 与 Step-Resume 的区别：Step-Resume 放弃本轮、无状态重来；Pause **保留循环内状态（Session 快照）原地挂起**，恢复不占轮次

### 2.5 Hooks —— 拦截回调（全部八个必填；不需要行为时传显式 no-op，缺席即装配错误）

```go
type Hooks struct {
    BeforeStimulate func(ctx context.Context, p *Prompt) error // 一次 Stimulate/Resume 开始；可改原型全部内容字段（`Bounds` 只读——框架快照、不回写），回写后全轮生效；error 终止整次运行
    AfterStimulate  func(ctx context.Context, output string)     // 一次 Stimulate/Resume 结束（正常/错误/挂起/提前停止均恰好一次）
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
| `BeforeStimulate` | 每次 Stimulate/Resume 开始、首事件前 | 整轮静态注入（人设、检索前缀；一次改写原型内容字段即全轮生效）。经验召回已归 `Memory.Recall`（§2.8），别再在这里读一遍 |
| `AfterStimulate` | 每次 Stimulate/Resume 结束（四臂恰好一次） | 会话结算。经验落盘已归 `Memory.Remember`（§2.8，在 `OnCycleEnd` 之前） |
| `BeforeThink` | 每次 Think 前 | 动态注入检索记忆、更新 Plan |
| `AfterThink` | Think 成功后 | 序列化 Decision 回存 Plan/记忆 |
| `BeforeAct` | 每个工具执行前 | 审批、改写工具参数 |
| `AfterAct` | 工具执行后 | 工具日志、失败降级（`err` 非 nil 即执行器失败） |
| `OnError` | 不可恢复错误时 | 告警上报 |
| `OnCycleEnd` | **每次 Stimulate/Resume（一个 Cycle）恰好一次**（正常/错误/挂起/消费者提前停止四臂都触发） | 结算、持久化最终输出；`outcome` 分类本轮结束方式（Done/Suspended/MaxRounds/Error/Aborted，零值保留 = 提前弃用迭代器 → Aborted） |

**⚠️ `BeforeThink` 必须整体替换 `p.Context`（`p.Context = append(p.Context[:0], newCtx...)` 或直接赋新切片）——它与循环上下文共享底层数组，直接 append 会污染 循环内上下文；`p.ToolResults` 同理（与循环结构化轨共享底层数组），整体替换、禁止原地 append。**

### 2.6 Sandbox —— 权限门（安全红线落点，守循环两侧）

```go
type Sandbox interface {
    Allow(ctx context.Context, a Action) (verdict Verdict, reason string, err error)
    Emit(ctx context.Context, u Utterance) (verdict Verdict, reason string, err error)
    Bounds() string // 执行边界描述（宿主定义），每次 Stimulate/Resume 快照一次
}
```

`Utterance{CellID, Round, Text}` 是本轮 Think 生成的文本。两侧同一套三态语法、同一条审计通道（`EventSandbox`）。

- `Allow` 三态裁决（`Verdict`）：`VerdictAllow` 放行执行；`VerdictDeny`（零值，fail-closed）时框架生成 `[sandbox-denied: reason]` 反馈进 Context，**循环继续**（不终止）；`VerdictAsk` **挂起征询**——reason 即展示给外部的问题文本，循环按 §6.4 的挂起-恢复协议挂起，宿主解决后批准才执行
- `Emit` 在**任何人听到这段文本之前**裁决（消费者、累积输出、下一轮 Think 都算"听到"）：`Allow` 原样说出；`Deny` 以 `[sandbox-denied: reason]` 取代该轮文本，该文本同时进 Context（大脑下一轮读得到自己的话被拒）；`Ask` **扣住草稿**按 §6.4 挂起，草稿随 `Session` 走线，批复后原样说出或被拒文本取代——**不重跑该轮 Think**
- 两侧 `err != nil` 都按 Deny 处理（fail-closed），反馈落库为 `[sandbox-denied: sandbox error: ...]`（审计记录 Reason 保留 `sandbox error: ...` 内层形态）
- 返回三态之外的 `Verdict` 值（构造出来的整数）也按 Deny 处理，拒绝文本写明 `sandbox returned an unknown ruling <n>`——不认识的值不可能"允许"任何东西
- 审计记录以 `Call` 区分两侧：工具侧带被门禁的 `Call`，文本侧 `Call` 为零值；每一裁决恰好一条，ask 链再以终结记录闭合
- `Bounds()` 返回执行边界描述，每次 Stimulate/Resume 快照一次、经 `Prompt.Bounds` 透传给 LLM（让大脑感知限制，如"只能访问 /workspace 下文件"）
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

### 2.9 器官组合：膜栈、降级链与可选的启动时点

一个端口后面放多个实现——循环只看得到一个器官，不知道有过选择：

```go
o.Sandbox = meowire.GuardStack(工作区策略, 网络策略)   // 一层膜，两个裁决者
o.Act     = meowire.FallbackEffector(本地工具, 远端工具) // 一双手，两处执行
```

大脑没有组合器：它是内置器官，背后没有可以降级的候选——换模型是换 `Brain` 参数重新 `New`。

- 组合器**返回的就是端口类型本身**，所以组合器官与单个器官的接法完全一致：没有新增插槽、事件或配置字段，`Replace` 接一个栈与接一个器官没有区别
- `GuardStack` 按严重程度裁决，不按层次先后：第一个 Deny 即结束（后面更宽松的层没有投票权），第一个 Ask 压过 Allow，某层返回 error 就是端口契约本身定义的 fail-closed Deny；`Bounds()` 取第一个非空边界。空栈一律 Deny——没有层的膜不守卫任何东西
- `FallbackEffector` 取第一个**执行未报错**的成员；全部失败时把各成员的错误一起返回（最后一个错不比第一个错更能解释为什么）。工具跑完了并说"不"（`Effect.Err`）是结果，不是器官坏了，因此降级链不会继续往下走
- `Bootable{Boot(ctx) error}` 是唯一可选的生命周期时点：任何端口都可以声明，实例每次加入一次装配时被调用一次（`New` 一次、每次被 `Replace` 接入时一次），顺序就是蓝图列出必填端口的顺序（`meowire.PortOrder()`），发生在 agent 存在之前。框架从不关闭器官，所以声明 `Boot` 的端口属于单个装配、按 agent 新建，不跨 `New` 复用。`Replace` 在槽位接受该端口之后、提交之前启动它，因此一个起不来的替代品不会在轮次中途生效，写错的槽名也不会白白消耗掉这一次启动
- `Boot` 失败即中止装配，本次尝试已经打开的东西经宿主 `Closer` 释放：框架从不关闭任何器官，`Close` 仍是唯一清理通道；被换下的旧器官同样不会被框架关闭——进行中的 Stimulate 可能还持有它，何时退场归宿主

---

## 3. 第 2 步：组装 `Organs`（装配根，唯一组装点）

```go
o := meowire.Organs{
    ID:      "agent-001",                       // 必填，每个在跑的 agent 唯一，无默认值
    Brain:   meowire.BrainConfig{               // 必填（参数）：内置大脑
        BaseURL: "https://api.deepseek.com/v1", // 空 = 官方 OpenAI 端点
        Key:     os.Getenv("LLM_KEY"),
        Model:   "deepseek-chat",
    },
    Act:     myEffector,                        // 必填
    Closer:  myCloser,                          // 必填
    Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: ...}), // 必填：全部八回调（工具函数填充缺失回调）
    Sandbox: mySandbox,                         // 必填
    Budget:  &meowire.ContextBudget{...},       // 必填
    Mem:     myMemory,                         // 必填

    System:   "你是 meow agent，用中文回答",      // 固定系统指令
    Identity: "你叫 meow，角色 assistant，语气温暖", // 身份描述文本（宿主自拼）
    Methods:  []meowire.MethodSpec{...},        // 内置能力描述（只描述，框架不消费）
    Tools:    []meowire.ToolSpec{...},          // 工具清单
    Context:  []string{"[记忆] 用户偏好：简洁"},  // 常驻上下文基底（历史记忆入口）
}
```

**字段明细：**

| 字段 | 类型 | 内容 | 必填 |
|------|------|------|------|
| `ID` | string | agent 唯一标识，**必填无默认**——同一份 `Blueprint` `New` 出多个实例时每个实例各自命名；框架按它给事件署名（`Event.CellID`）并核对挂起句柄归属，宿主拿它区分自己手里的多个实例 | **是** |
| `Brain` | BrainConfig | 内置大脑参数（BaseURL 空 = 官方端点；Key/Model 必填；`Mode` 选 wire：`BrainModeChat`（默认，零值同 1）= chat completions，`BrainModeResponses` = Responses API，名表之外的值装配即拒） | **必填（参数）** |
| `Act` | Effector | 工具执行 | **是** |
| `Closer` | Closer | 资源清理 | **是** |
| `Hooks` | *Hooks | 拦截回调（全部八回调必填） | **是** |
| `Sandbox` | Sandbox | 权限门（`Allow` 守工具执行、`Emit` 守该轮文本出口） | **是** |
| `Budget` | *ContextBudget | 令牌调节器（文本轨 + 结构化反馈轨） | **是** |
| `Mem` | Memory | 经验端口（`Recall` 每轮 Think 前，`Remember` 每次调用终点） | **是** |
| `System` | string | 系统指令，进 Prompt.System | 否 |
| `Identity` | string | 身份描述文本（宿主自拼），进 Prompt.Identity | 否 |
| `Methods` | []MethodSpec | 内置能力描述（只描述、框架不消费）；`MethodSpec{Name, Desc, Input, Output}`，进 Prompt.Methods | 否 |
| `Tools` | []ToolSpec | 工具清单，进 Prompt.Tools；`ToolSpec{Name, Desc, Input, Output}`，`Input` 为 JSON Schema（大脑据此收工具定义） | 否 |
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
    MaxParallelActs int
}
```

| 字段 | 语义 | 零值默认 |
|------|------|---------|
| `MaxRounds` | 硬性轮数上限；**最后一轮仍有工具调用时以 `ErrMaxRounds` 结束**（该轮工具结果不会被下一轮 Think 消化），配合 Step-Resume 防死循环 | 8 |
| `MaxToolOutput` | 工具反馈截断长度（按 UTF-8 安全截断，超长附 `[truncated, N bytes total]`） | 不截断 |
| `MaxRetries` | Think 重试次数（仅重试 Think；工具失败防护在宿主侧 Effector/AfterAct） | 不重试 |
| `ToolTimeout` | 单个工具执行超时（每次尝试独立计时；超时错误不重试，写入 `ToolResults.Err` 继续循环） | 无超时 |
| `ToolMaxRetries` | 工具执行失败重试次数（**仅执行器 error**；`Effect.Err` 不重试，防重复副作用） | 不重试 |
| `ParallelActs` | **同轮多工具批次并行（opt-in）**：串行门控（逐条事件/沙箱裁决/BeforeAct）→ 并行 Act（含超时/重试）→ 串行反馈（按调用序，与完成顺序无关）。单调用恒走串行路径；**前提：Effector 实现并发安全** | 严格串行 |
| `MaxParallelActs` | 一个批次同时执行的工具调用上限。只收窄 `ParallelActs`——跑哪些调用、反馈顺序都不变——给一个「能并发但不能一起上」的宿主（别处有限流、连接池） | 整批一起跑 |

**运行期热更新**：

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
    Organs Organs   // Brain 参数 + 六端口 + 固定上下文（全部必填）
    Config Config   // 零值即默认
}

bp := meowire.Blueprint{Organs: organs, Config: cfg}
agent, err := meowire.New(bp)
```

- **Blueprint 是一次定义、多次装配**：同一 `bp` 可 `New` 出多个独立 Agent 实例（flat model 多 agent 场景），但 `Organs.ID` 是实例身份，多次 `New` 必须各自换名
- `New` 基于**装配图**校验（蓝图 = 数据对象节点 + 槽位边），只有两档：
  - `error` 级（必填端口缺失 / 回调缺失 / Budget 不完整 / `Organs.ID` 为空）：**恒阻断**，返回 `meow: required port X not injected`（X ∈ Act/Closer/Hooks/Sandbox/Budget/Mem ＋ 缺失回调的名字本身——`BeforeStimulate`…`OnCycleEnd`，即 H1–H8 的 `Name`；`Issue.Wire` 才是 P4/H4 这类蓝图编号），多缺联合报错；大脑参数（Model/Key/Mode）与 ID 为空另有专属错误文案
  - `info` 级共六条（空 Identity、空 Tools、空 Context、`MaxRounds<=0` 取默认 8、`ParallelActs` 开着——提示 Effector **必须**并发安全、`MaxParallelActs` 设了而 `ParallelActs` 关着——上限绑不到任何东西）：**永不阻断**，用 `meowire.Validate(organs, cfg)` 显式查看；框架不检测 Effector 是否真的并发安全，那条只是提醒
  - 没有 `warn` 级：对宿主而言每个接线点都是必填，缺失即缺失器官，没有“半配放行”（随父端口隐含的子槽 `P3b`/`P5b`/`P5c`/`P6b`/`P7b` 与 api 注入的 `G1` 不单独检查——换父槽即换整只端口）
- `New` 依次做三件事：按图**校验**蓝图、逐个**启动**声明了 `Bootable` 的器官（见 §2.9）、**构造** cell。启动失败即中止，后面的步骤不再执行，本次尝试打开的东西经宿主 `Closer` 释放
- 装配后宿主调用 `Stimulate` / `Resume` / `Pause` / `Unpause` / `Close`，并可经 `Replace` 运行时换端口（见下）
- Brain 参数 + 六端口 + 八回调均须齐备，**没有 stub、没有可选端口、没有"最小可运行"路径**；大脑由框架提供（其余器官不提供任何实现），宿主可用 `meowire.FullHooks(...)` 把不需要的钩子声明为显式 no-op

### 5.1 动态接线：`Replace`（运行时换器官）

```go
oldPort, err := agent.Replace(meowire.SlotSandbox, stricterMembrane) // 下次 Stimulate 生效
```

- 可换槽位：`SlotAct` / `SlotSandbox` / `SlotBudget` / `SlotMem` / `SlotHooks`（槽名单一事实源是蓝图 `WirePoint.Slot`，`Connectome()` 过滤 `WirePoint.Slot != ""` 即可枚举——`WiringDiagram(o)` 另给每条边的 `Filled` 状态，`SwappableSlots` 只是 internal 侧的派生函数、不在公开面——常量与蓝图由测试钉死）；`Closer`（资源绑定）与暂停门（框架内部接线）不可换；**大脑没有插槽**——换模型是换 `Brain` 参数重新 `New`
- 语义：每次 `Stimulate` 快照端口构造全新 LoopContext——**飞行中的 Stimulate 不受影响**，替换只在下次生效；返回被换下的端口（它持有的资源何时释放由宿主决定，框架不代关）；新器官若声明了 `Bootable`，则先由槽位断言接受、再启动、最后提交，启动失败或槽位不合时接线都保持原样
- 并发安全；`Close` 后拒绝交换并返回 `ErrCellClosed`（换入的端口不会被启动）；**拒绝 nil/不完整端口**（Budget 需 Trimmer+TrimResults+MaxTokens、Hooks 需八回调）；槽位或端口类型错误返回 error
- **审计事件**：每次成功替换记录一条 `ReplaceAudit{CellID, Slot, OldType, NewType}`（被换的端口按 Go 类型名记录、不作持有：审计在替换之后才产出，必须可序列化；被换下的端口由 `Replace` 的返回值交给宿主），在**下一次 Stimulate/Resume 开头（生效时刻）**以 `EventReplace` 产出（与 `EventSandbox` 同级可持久化审计）；失败替换不记录；无替换零产出。宿主策略切换审计闭环：从事件流更新 activePolicy，不再手工维护状态机

### 5.2 装配自检：装配图与它的读口

```go
for _, is := range meowire.Validate(o, cfg) { // error 级 New 也会拒；info 级只有这里看得见
    fmt.Println(is.Level, is.Wire, is.Msg)
}
for _, sl := range meowire.WiringDiagram(o) { // 一条蓝图边 + 这次装配有没有填上
    fmt.Println(sl.Wire.ID, sl.Wire.TargetID, sl.Filled)
}
fmt.Println(meowire.RenderDiagram(o))          // 同一张图的 ASCII 形态
fmt.Println(meowire.PortOrder())               // 启动顺序：Act, Closer, Hooks, Sandbox, Budget, Mem
```

- `Connectome()`（边）与 `ConnectomeNodes()`（数据对象节点）**就是**蓝图本身；`WiringDiagram(o)`
  是蓝图 × 这次装配，每个 `Slot` 带整条 `WirePoint` 加 `Filled`；`RenderDiagram`/`RenderJSON`
  渲染同一张图（人读 / 机器读），`Validate` 按它出发现
- 节点数与边 ID 只存在于代码里——运行时从 `Connectome()` 现取，别把数字抄进文档，那是一条必腐项
- 框架内置项（`F1` 工具反馈、`F2` 大脑、`G1` 暂停门）恒读作「已填」：它们不是宿主的槽

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
（串行路径的形状。开启 `ParallelActs` 且一批多于一个调用时改为三阶段：整批 `EventToolCall` 先依次公布 → 整批门控 `EventSandbox` → 按调用序落 `EventToolResult`；间隙点也只有整批前那一次，见 §2.4。）

**暂停路径（间隙点生效，快照挂起：迭代器正常结束，Resume 续跑）：**

```
… → EventState(paused) → EventPaused(Session 快照) → 迭代器正常结束（无 Done/Error）
→ 宿主调 Resume(sess, Response{}) → 剩余工具先执行 → 回到 EventState(thinking) → 继续原序列
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
→ 宿主 Resume(sess, 批复 Response) → EventSandbox(终结) → EventText(原稿或被拒文本)
  → [该轮工具照常执行 → 回到 EventState(thinking)] 或 [无事可做 → EventState(done) → EventDone]
```

**接线/配置审计（下一次 Stimulate/Resume 开头、首个业务事件前，两类按序）：**

```
EventReplace(slot/old/new) × N → EventConfig(old/new) × M → 后续正常序列
```

未送达的审计不会丢：本轮在前置阶段就死掉（`BeforeStimulate` 拒绝、ctx 取消、消费者中途停止）时，cell 把没送出的那段重新排队，下一次调用依旧领头——审计的恰好一次不因一次失败的序言而破（`TestAbandonedStreamRequeuesItsAudits`、`TestCellReplaceAuditSurvivesFailedPrelude`）。

**错误路径：**

```
EventState(error) → EventError(Err)
```

**关闭后 Stimulate/Resume：** 直接产出 `EventError(ErrCellClosed)`。

### 6.2 `Event` 字段（按 Kind 生效，其余为零值）

有三类信息与 Kind 无关：`CellID` 说出这条事件出自哪个 cell、`Seq` 给出它在**该 cell 内**的发出序号（1 起，跨 Stimulate/Resume 连续单调，随进程重启而重置）、`TS` 是发出时刻（Unix 毫秒）——三者都在 cell 边界上逐条盖章，连关闭后的错误事件也带；序号与时刻合起来让一份日志能区分「这个 agent 没话说」和「这条记录丢了」。`Dropped` 列出没能跨过事件流的值（见 §6.5）——循环自己从不写它，所以非空就意味着「这条是从日志里还原出来的」。

| Kind | 有效字段 | 内容 |
|------|---------|------|
| `EventText` | `Text` | 该轮文本，**已过出口膜**（`Emit` 拒绝时为 `[sandbox-denied: reason]`） |
| `EventToolCall` | `ToolCall *ToolCall` | LLM 决定调用的工具 |
| `EventToolResult` | `Effect *Effect`, `ToolCall *ToolCall` | 工具执行结果（含被 Sandbox 拒绝：`Effect.Err = "[sandbox-denied: reason]"`）；`ToolCall` 回显调用，用于 ID 关联 |
| `EventSandbox` | `Verdict *SandboxVerdict` | 膜的裁决审计记录（裁决 Ruling：allow/deny/**ask**、被门禁的工具或零值=文本侧、策略原因、征询问题、评估错误）；循环两侧每一裁决恰好一条，ask 链以终结的第二条记录闭合 |
| `EventState` | `State LoopState` | 循环状态（idle/thinking/acting/paused/**waiting**/done/error） |
| `EventDone` | `Output` | 整轮累计文本输出 |
| `EventError` | `Err` | 不可恢复错误：框架哨兵 `ErrMaxRounds`/`ErrCellClosed`/`ErrForeignSession` 可 `errors.Is` 判；`Resume` 交回零值句柄时是 `nerve: resume: invalid session`（非哨兵，按文本判；重复恢复同一句柄不被框架检测——剩余工具调用会重放，见 §6.4）；此外还有钩子拒绝、Think 重试耗尽、`Recall` 失败与 ctx 取消 |
| `EventUsage` | `Usage *Usage` | 最近一次 Think 的 token 用量 |
| `EventWaitInput` | `Wait *WaitInput` | 循环等外部输入：`WaitInput{CellID, Call, Question, Session}`——四种成因由 `Session.Kind()` 点名（`WaitTool` 工具自请求、`WaitCallAsk` 执行前征询、`WaitUtterance` 文本征询、`WaitPause` 暂停），`Call`+`Question` 只说明问的是谁，不区分前两种；宿主**保存 Session**、展示问题，取得答复后调 `agent.Resume(sess, resp)` |
| `EventPaused` | `Wait *WaitInput` | 暂停请求生效：`WaitInput{CellID, Session}`（Call 零值、Question 空，`Session.Kind() == WaitPause`）——宿主保存 Session，调 `agent.Resume(sess, meowire.Response{})` 续跑（与其他挂起同一条通道） |
| `EventReplace` | `Replace *ReplaceAudit` | 端口替换审计：`ReplaceAudit{CellID, Slot, OldType, NewType}`（端口按 Go 类型名记录，不持有值）；下一次 Stimulate/Resume 开头（生效时刻）按序产出，可持久化 |
| `EventConfig` | `Config *ConfigAudit` | 配置整包替换审计：`ConfigAudit{CellID, Old LoopConfig, New LoopConfig}`；下一次 Stimulate/Resume 开头在 EventReplace 之后按序产出，可持久化 |

`SandboxVerdict{CellID, Call, Ruling Verdict, Reason, Question, Err}`：膜在循环两侧的每一次裁决各产出一条（`Ruling` 三态：Deny 为零值，fail-closed），`Call` 为工具侧被门禁的调用、零值即文本侧。ask 裁决再产出终结记录闭合审计链（`Reason` 存宿主的批复本身：拒绝落为 `[sandbox-denied: ...]`，批准则原样保留答复文本）。宿主持久化事件流即得到审计日志（谁、代表谁、何时、做了什么、为什么被允许）。详见 [protocols.md](protocols.md) §4 Authority。

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
// ② 宿主侧：保存 Session，展示问题，收集答复后 Resume（与 Stimulate 同构消费）
var sess meowire.Session
for ev := range agent.Stimulate(ctx, "整理桌面") {
    switch ev.Kind {
    case meowire.EventWaitInput:
        sess = ev.Wait.Session            // 保存会话句柄
        ui.Show(ev.Wait.Question)         // “猫在等人回答”
        go func() {                       // 宿主自行控制超时（默认拒绝）
            select {
            case ans := <-ui.Answer():
                answerCh <- meowire.Response{Answer: ans}
            case <-time.After(60 * time.Second):
                answerCh <- meowire.Response{Deny: "timeout"}
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
- **暂停= 同一挂起机制**：yield `EventState(StatePaused)` → `EventPaused`（Session 快照，Call 零值）后迭代器正常结束；`agent.Resume(sess, meowire.Response{})` 续跑，不注入任何结果（pause 没有被问的东西，Response 不被读取）；暂停点在工具执行前时，当前工具记入 Session.remaining，Resume 先执行
- **`Session` 是不透明值对象**（`round`/Context/剩余工具调用/累积输出快照）：宿主只保存、传回，不碰内部；**归属明确**——`Resume` 拒绝别的 cell 交回的句柄（`ErrForeignSession`，重放等于拿 B 的器官跑 A 的那一轮）；**单次消费**——重复 Resume 会重复执行剩余工具（副作用重复，宿主责任）
- **`sess.Kind()`**：报告这次挂起的成因——它决定 `Resume` 会怎么读 `Response`（`WaitTool` 要内容、两种征询要裁决、`WaitPause` 什么都不要）
- **`sess.RemainingCalls()`**：授权的只读探测之一——返回挂起点尚未执行的调用克隆（无则空）：暂停快照保留整批未执行调用（Resume 重放）；`ParallelActs` 批内 WaitInput 挂起则为空（整批已执行完，Resume 只注入答复，绝不重放）
- **持久化**：`sess.Marshal()` 产出带版本号的 JSON 字节（句柄归属的 cell、挂起成因、被扣住的草稿都在其上），宿主存盘；重启后 `meowire.UnmarshalSession(data)` 还原句柄再 Resume——挂起/暂停跨进程可恢复；版本或挂起成因不认识的名字拒绝还原（防止旧/新格式误重放）
- **不占轮次**：恢复后从挂起轮继续，消化响应的 Think 使用挂起轮的配额（`MaxRounds` 不额外扣减）
- **不触发 budget**：等待期间无 Think，两个裁剪器都不调用；恢复后下一轮 Think 前才执行
- **答复语法（`Response` 两个字段，读哪个由 `Session.Kind()` 决定）**：**`Deny` 非空** = 拒绝，并以该原因落库——执行前征询落 `[sandbox-denied: 原因]` 反馈、文本征询以该文本取代草稿、工具请求输入把该调用记为 `ToolResults.Err`（拒绝是一次工具失败，不是工具输出）；**`Deny` 为空且 `Answer` 非空** = 批准——执行前征询放行 pending 调用执行（不再过门禁），文本征询按原稿说出；**零值 `Response`** = 拒绝（`[sandbox-denied: declined]`），没答就是 fail-closed。**ask_user** 的 `Answer` 原样作为挂起工具的结构化结果写入恢复后的 `ToolResults`（`ID` 为该调用的 `call_xxx`，进入恢复后的第一次 Think；空即空结果）。答复文本永不被解析成指令：宿主写进 `Answer` 的内容哪怕以 `[denied:` 开头也只是文本。超时由宿主控制（默认拒绝：`Response{Deny: "timeout"}`）
- **一轮只有一个挂起名额**：批内已有调用挂起后，后面兄弟调用再返回 `WaitInput` 会被框架在下发前拒成一条工具反馈（`one wait per round: <name> already waits`）——快照里没有第二次挂起额度可花，宁可让它以反馈形式回到模型，也不要一个永远没人答的挂起
- **剩余工具**：挂起发生在多工具轮中间时，恢复后先执行该轮剩余工具，再进入 Think
- **Resume 的 hooks 与 Stimulate 完全一致**（`BeforeStimulate` 照常触发，宿主 append 语义下 `Session.Context` 与检索结果自然合并）；`Close` 后 Resume 产出 `ErrCellClosed`；`Session` 为内存态句柄——跨进程存活靠 `Marshal()` 落盘与 `UnmarshalSession()` 还原（见「持久化」），没存过盘的句柄重启后按超时拒绝处理
- **与 `BeforeThink` 的注入分工**：`BeforeThink` 改写的是本轮 Prompt（整体替换 `p.Context` 等字段），`Resume` 注入的是挂起响应（作为挂起工具的结构化结果进 `ToolResults`）——两者无重叠

### 6.5 事件流落盘：`EncodeEvent` / `DecodeEvent`

```go
for ev := range agent.Stimulate(ctx, text) {
    line, err := meowire.EncodeEvent(ev) // 一条事件一条 JSON 记录：追加进日志即可
    ...
}

// 稍后，或在另一个进程里
ev, err := meowire.DecodeEvent(line)
```

- 每条记录带 `WireEvent.Version`（**当前 v2**；v2 起才携带 `Seq`/`TS`），版本不符直接拒读，不会“差不多就当能读”——v1 那份缺序号与时刻的记录一律拒绝，不做降级解释；Kind、状态、膜的裁决**按名字上线**，所以新版本重排枚举不会悄悄改写上周那份日志
- `EventError` 的身份：框架自己的哨兵（`ErrMaxRounds`、`ErrForeignSession`、`ErrCellClosed`）还原后仍是同一个值——被包装时外层文本也保住，`errors.Is` 照旧成立。其余错误（宿主自己的 `rate limited`）只回来**同样的文本**，事件会在 `Dropped` 里写 `err.identity`：丢了什么要说出来，而不是让一次比较静默地返回 false
- `EventWaitInput` / `EventPaused` 里的恢复句柄以它自己的序列化形式内嵌，因此 `Session` 的版本闸门照常生效：过期的挂起由句柄拒绝，不是由更宽松的事件流放行
- `Event.CellID` 说出这条事件出自哪个 cell，所以宿主把多个实例写进一份日志也分得清谁说的；`Dropped` 说明这条记录是否完整到达
- **别**直接 `json.Marshal` 一个 `Event`：它把 `Err` 字段写成 `{}`（文本没了）、把枚举写成数字，而且什么都不报告

---

## 7. 第 6 步：`Close` 与多实例组合

### 7.1 `Close() error`

幂等（CAS 保证）：只有完成 open→closed 跳转的那一次调用运行宿主 `Closer`，其错误包为 `meow: closer: ...`；cell 本身不产生错误，故 Close 无从聚合多个错误。关闭后 `Stimulate`/`Resume` 产出 `EventError`（错误链含 `ErrCellClosed`，`errors.Is` 可判），`Replace` 则直接返回该错误。宿主应 `defer agent.Close()`。

### 7.2 多 agent = 宿主组合多个实例

**没有 agent 间这一层**：内核不寻址、不投递、不配对回程，也不提供突触图、能力卡与全群视图——
`Organs` 里没有面向邻居的槽，`Prompt` 里没有入站信号轨。这条边界由 `test/wiring_free_test.go`
机械守住（宿主侧文件出现 channel/goroutine/接收，或 `Organs` 多出一个邻居槽，即红）。

**扁平模型**：一个 `Agent` = 一个内核，宿主拥有全部实例。子 agent 在宿主 Effector 工具内
`New(bp) → Stimulate → 收事件流`，对主循环完全透明；要 A 等 B 的结果，就把 B 做成 A 的一个工具。
同一份 `Blueprint` 可以 `New` 多次，但 `Organs.ID` 是实例身份（事件署名与挂起句柄归属都按它判定），
且每个实例的 `Organs.Context` 切片与 `Hooks` 闭包是共享的，多实例要各自覆写（见 [reference-host.md](reference-host.md) Step 7）。

**回读身份**：`agent.ID()` 返回装配时声明的 `Organs.ID`，与每条事件的 `Event.CellID`、每个 `Session` 的归属核对同源——宿主拿多个实例做事件路由或日志署名时，它是唯一的公开读取口。

### 7.3 多 agent 状态可见性：宿主侧三件套

> 框架事件流是「谁消费谁可见」：主循环看不到子 agent 的事件（扁平模型）。主/子 agent 状态统一可见由宿主侧三件套实现，**框架零改动**：StreamHub（共享状态容器）+ `WithSink` 流式注入（可选）+ Hooks/工具（状态与 plan，必选）。

**StreamHub —— 共享状态容器（按 `Organs.ID` 分流）**

```go
type StreamHub struct {
	mu      sync.RWMutex
	streams map[string]chan meowire.Event // 流式输出（可选注入；无 channel = 静默）
	tasks   map[string]*taskView          // 任务状态（必选，Hooks 写入）
	plans   map[string]string             // plan 树（update_plan 工具写入）
}
```

```go
// taskView 是宿主自己的任务视图：框架从不维护跨 agent 的任务生命周期，状态完全由宿主的 Hooks 与工具写进来。
type taskView struct {
	State    string
	LastText string
	LastTool string
	Output   string
}
```

宿主创建**唯一实例**，所有 agent（主/子）创建时注入同一个 hub；UI 只读 hub 即可获得全部 agent 的实时视图。

**① 流式（可选）—— `WithSink` 注入**

框架的 `EventText` 永远是整段文本（每轮 Think 的 `Decision.Text`）；token 级增量走内置大脑的 Sink 通道——挂在交给 `Stimulate` 的 context 上，delta 在出口膜裁决之前送达，宿主把每个 delta 推入 `hub.streams[id]`：

```go
func streamingCtx(ctx context.Context, id string, hub *StreamHub) context.Context {
	ch := hub.channel(id) // 未注入 channel → nil → 静默
	if ch == nil {
		return ctx
	}
	return meowire.WithSink(ctx, func(delta string) {
		select {
		case ch <- meowire.Event{Kind: meowire.EventText, Text: delta}:
		case <-ctx.Done():
		}
	})
}
```

静默执行（子 agent 后台跑、不推流）：不包 `WithSink` 即可，delta 无处可去自然不推。`EventText` 整段到达时替换已推内容（推流 + 更正）。

**② 状态同步（必选）—— Hooks 回调写入**

宿主在 `Organs.Hooks` 里把状态写进 hub，框架在节点必然调用。id 通过闭包捕获（`AfterAct` 也可用 `a.CellID`）：

```go
hooksFor := func(id string) *meowire.Hooks {
	return meowire.FullHooks(meowire.Hooks{
		AfterThink: func(ctx context.Context, d *meowire.Decision) error {
			hub.updateTask(id, taskView{State: "thinking", LastText: d.Text})
			return nil
		},
		AfterAct: func(ctx context.Context, a *meowire.Action, e *meowire.Effect, err error) {
			hub.updateTask(a.CellID, taskView{State: "acting", LastTool: a.Call.Name})
		},
		OnCycleEnd: func(ctx context.Context, output string, outcome meowire.CycleOutcome) {
			switch outcome {
			case meowire.OutcomeDone:
				hub.updateTask(id, taskView{State: "done", Output: output})
			case meowire.OutcomeSuspended:
				hub.updateTask(id, taskView{State: "needs-input"})
			default:
				hub.updateTask(id, taskView{State: "failed"})
			}
		},
	})
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

// Hooks.BeforeThink：回灌 p.Plan（指针可改，本轮 Think 即读到）
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
			Brain:  baseBrain, // 同一份参数（或按子任务换 Model）
			Act:    sameEffector,
			Hooks:   hooksFor(args.ID), // 同一 hub 的闭包
			Closer:  closerStub,
			Sandbox: sandboxStub,
			Budget:  passBudget, // 直通裁剪器：Trimmer + TrimResults + MaxTokens>0 三者齐备
			Mem:     sameMemory, // 必填端口：Recall + Remember（§8 骨架同此）
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
	"os"

	meowire "github.com/qyiun666/meowire/api"
)

// ① 大脑：传参即得（BaseURL/Key/Model/Stream/Mode——Mode 选 wire，默认 chat），无需实现

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
	ctx := context.Background()
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-001",
			Brain:   meowire.BrainConfig{Model: "gpt-5.2", Key: os.Getenv("OPENAI_API_KEY")},
			Act:     &effector{registry: toolRegistry},
			Closer:  &closer{},
			Hooks:   meowire.FullHooks(meowire.Hooks{BeforeThink: injectMemory, OnCycleEnd: persistOutput}),
			Sandbox: &sandbox{},
			Budget:  &meowire.ContextBudget{MaxTokens: 4000, Trimmer: trim, TrimResults: trimResults},
			Mem:     &memory{}, // 必填端口：Recall + Remember
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
2. **端口并发安全**：同一 Agent 并发 Stimulate 时 Effector 被并发调用（大脑无状态，天然并发安全）
3. **宿主端口必须响应 ctx 取消**；`ask_user` 不得在 Effector 内同步阻塞（框架级挂起协议见 §6.4），宿主侧等待超时自行控制（默认拒绝：`Resume(sess, Response{Deny: "timeout"})`）
4. **`Organs.Context` 是切片**：每轮 Think 前记得经 `BeforeThink` 更新为最新历史（MemHop）
5. **`ErrMaxRounds` 不是 bug**：最后一轮有工具调用且轮数耗尽时抛出，配合 Step-Resume 让宿主续跑是预期用法
6. **事件流是观察镜像**：宿主不能往打开中的迭代器回喂数据；数据回喂走下一次 `Stimulate`
7. **流式 UX 走 `WithSink`**：`EventText` 永远整段已过膜，token 增量不进事件流、在膜裁决前送达 Sink
8. **错误分三条通道，不是一条**：钩子返回的错与 Think 重试耗尽经 `EventError` 透出（用 `errors.Is` 判 `meowire.ErrMaxRounds` 等哨兵）；`Sandbox` 返回的 err **永不**进 `EventError`——按 fail-closed 折成 `[sandbox-denied: sandbox error: ...]` 反馈；`Effector` 返回的 err 落 `ToolResults.Err` 且循环继续；`Memory.Remember` 的 err 只送到 `Hooks.OnError`、不改本轮终态（`Recall` 失败才是 `EventError`）
9. **暂停不打断执行中的工具**：Pause 在间隙点生效（快照挂起，`EventPaused` 后迭代器结束、`Resume(sess, Response{})` 续跑）；如需中断正在执行的工具，用 ctx 取消（§2.4）
10. **hub 生命周期归宿主**（§7.3）：流式 channel 满会阻塞 agent 循环（Sink 转发是同步的），宿主必须管理背压（buffer 大小）与回收（agent 结束后 close/删除 channel）；框架不参与
11. **子 agent 必须注入与主 agent 同一个 hub 实例**（§7.3）：换实例即失联，统一状态视图靠共享实例实现

## 10. 相关文档

| 文档 | 位置 | 内容 |
|------|------|------|
| 宿主参考实现 | [reference-host.md](reference-host.md) | 按步骤从零实现一个 AI 宿主（LLM 对接/工具/权限/记忆/多 agent/持久化） |
| 大脑规格 | [thinker-openai-go.md](thinker-openai-go.md) | 内置大脑的逐字段落点表与协议参考（`openai-go/v3`） |
| 协议映射指南 | [protocols.md](protocols.md) | MCP / A2A / AGENTS.md / Authority 的宿主侧映射 |
| 中文快速入门 | [README.zh-CN.md](README.zh-CN.md) | 中文 README：特性总览 + 完整装配示例 |
| English README | [README.md](README.md) | 英文快速入门 + 概念总览 |
| api 模块上下文 | [api/agent.md](api/agent.md) | api 包长期上下文、关键 决策、陷阱 |
| 决策循环实现 | [internal/nerve/loop.go](internal/nerve/loop.go) | 循环 编排源码（Think→Act→yield） |
| 集成测试 | [test/](test/) | 端到端行为验证（生命周期、端口注入、事件序列） |
