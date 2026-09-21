# Meowire

Go 语言仿生 agent 编排基座 —— **自带大脑的骨架。**

Meowire 是一个用于构建 agent 宿主的极简决策循环内核。它负责编排（Think → Act → 产出事件），
大脑随框架出厂（一个传参数就位、不用自己实现的 openai-go 客户端），其余交给你：
工具、记忆、安全策略。框架不绑架你的技术栈 —— 只有一个干净、值得信赖的循环。

> 需要 Go 1.27+（事件流是 `iter.Seq`，并行工具批用 `sync.WaitGroup.Go`——分别自 1.23、1.25 起；门槛值以 `go.mod` 为准）。

## 为什么选择 Meowire

- **大脑以参数到位。** `Organs.Brain{BaseURL, Key, Model, Stream, Mode}` 一填，组合根就把
  内置的 openai-go 大脑构造好注入 —— 你不写 Thinker、不 import 任何 SDK；`Mode` 选 wire：
  默认（零值同 1）走 chat completions，`BrainModeResponses` 走 Responses API，两根 wire 渲染同一份
  无状态 Prompt、折回同一个 Decision；内核（循环、事件、端口）依旧纯标准库，provider 词汇止步于唯一一个内部包。
- **其余由你掌控。** Meowire 不提供工具框架或记忆后端 —— 它注入六个宿主端口，
  期待你来实现它们。框架从不掩盖 agent 实际做了什么。
- **一枚直接依赖，锚死版本。** `github.com/openai/openai-go/v3`，锁 v3.61.0（选它是决策，
  升它也是决策，从不自动跟随）；内核自身只用标准库，`go.mod` 里另外四枚 `tidwall/*` 是该 SDK 带进来的 indirect。
- **小巧可读。** 生产码约 4700 行（口径：`api/` + `internal/`，不含 `*_test.go` 与 `internal/testutil` 测试桩；全仓 `*_test.go` 另计约 9600 行）。决策循环按关注点分文件（`internal/nerve/`：loop、gate、pause、retry、feedback、parallel）。
- **内部完全封闭。** 所有实现位于 `internal/` 下 —— Go 编译器保证唯一可 import 的对外表面是
  `api/` 包（`New` / `Stimulate` / `Close` + 契约类型）。

## 特性

- **Think→Act 决策循环**，支持每轮重试与硬性轮数上限
- **类型化事件流** —— `Stimulate` 返回 `iter.Seq[Event]`，宿主观察
  `EventText`、`EventToolCall`、`EventToolResult`、`EventState`、`EventDone`、`EventError`、`EventUsage`、
  `EventSandbox`（膜裁决审计记录，两侧同通道）、`EventWaitInput`（循环挂起等待外部输入）、
  `EventPaused`（暂停请求生效 —— 快照 + 恢复句柄）、`EventReplace`（端口替换审计记录）、
  `EventConfig`（配置替换审计记录）
- **膜审计轨迹** —— `Sandbox` 的每次裁决（允许/拒绝/征询，循环两侧同一通道）产出
  一条 `EventSandbox`（被门禁的工具，文本侧裁决则该字段为零值；加原因与错误）；
  持久化事件流即得完整"谁/做了什么/为什么被允许"审计，符合 Authority 安全模型
- **接线图检视** —— `Connectome`（边）/ `ConnectomeNodes`（数据对象节点）/ `WiringDiagram`（蓝图 × 本次装配的填充状态）/ `Validate` / `RenderDiagram` / `RenderJSON` 把装配视为图（数据对象节点 + 插槽边），供人或机器渲染
- **动态接线（运行期换器官）** —— `Agent.Replace(slot, port)` 运行时替换
  `Act`/`Sandbox`/`Budget`/`Mem`/`Hooks`；下次 `Stimulate` 或 `Resume` 生效，飞行中的那一轮保留启动时的端口；
  每次成功替换以 `EventReplace` 在下次 Stimulate/Resume 开头审计产出。大脑没有插槽：
  换模型是换参数重新 `New`，不是运行期换件
- **运行期配置热更新** —— `Agent.UpdateConfig(cfg)` / `Agent.GetConfig()` 热调
  `MaxRounds` 等标量限制，免整 Agent 重建
- **内置大脑 + 六个宿主端口，全部必填**（无 stub、无可选器官——内核不认识「邻居」，跨实例的事归宿主）：
  大脑是 `Organs.Brain`（BaseURL/Key/Model/Stream/Mode，指向任意 OpenAI 兼容端点；Mode 默认 chat completions、可选 Responses API），其余
  `Effector`（工具）、`Closer`（清理）、`Hooks`（拦截 ——
  全部八个回调 H1–H8 必填：显式 no-op，而非缺席）、`Sandbox`（权限膜）、
  `ContextBudget`（令牌调节器 —— 覆盖两条累积轨，必须有 Trimmer、TrimResults 与 MaxTokens）、
  `Memory`（经验端口 —— 每轮 Think 前 `Recall`，每次调用终点 `Remember`）。
  流式增量走 `meowire.WithSink(ctx, sink)` —— 在出口膜裁决之前送达宿主，`EventText`
  （整段、已裁决）到达时整段替换已推内容
- **一个端口后面放多个器官** —— `GuardStack` / `FallbackEffector` 把若干实现组合成循环看到的唯一一个器官，返回的**就是端口类型本身**：组合器官与单个器官接法一致，蓝图没有多出任何插槽
- **器官可以在被使用之前先启动** —— 任何端口都可声明 `Bootable`；`New` 按 `PortOrder()`（蓝图自己蕴含的顺序，不是在旁边另抄一份）对每个声明者调用一次，`Replace` 在提交替换之前先启动。启动失败即中止装配，本次尝试打开的东西经宿主 `Closer` 释放 —— 框架依旧不关任何器官
- **事件流可落盘** —— `EncodeEvent`/`DecodeEvent` 一条事件一条带版本的 JSON 记录（线格式当前 v2），枚举按名字上线，框架错误按身份还原，名字表里拼不出来的枚举值编码时直接拒绝，过不去的值在事件的 `Dropped` 里点名；每条事件都带着产出它的 `CellID`、它在该 cell 内的发出序号 `Seq`（1 起，跨 `Stimulate`/`Resume` 连续单调）与发射时刻 `TS`（Unix 毫秒），所以一份日志既分得清谁说的，也分得清「这个 agent 没话说」和「这条记录丢了」
- **Step-Resume** —— 每次 `Stimulate` 是一个无状态步骤；停止迭代器，在宿主侧处理
  （异步任务、人工接管、`ErrMaxRounds` 续跑），再 `Stimulate` 继续。工具请求输入（ask_user）
  不在此列，走下面的统一挂起-恢复协议（唯一形式）
- **统一挂起-恢复** —— 四种挂起共用同一条快照 + 恢复路径，答复统一用一个类型化的 `Response{Answer, Deny}`，由 `Session.Kind()` 决定这条句柄读哪个字段：
  - **ask_user**：工具返回 `Effect{WaitInput: 问题}`，循环产出 `EventState(StateWaiting)` +
    `EventWaitInput`（工具、问题、不透明 `Session`）后**迭代器正常结束** ——不阻塞、不占轮次、
    等待期间不触发 budget。`Agent.Resume(ctx, sess, Response{Answer: 文本})` 续跑：答复以挂起工具的结构化
    结果进入循环（`Prompt.ToolResults` 条目，ID 保留），`Response{Deny: 原因}` 则把该调用记为失败（拒绝是工具失败，不是工具输出），先执行剩余工具，再从挂起轮继续。
  - **Pause**：`Agent.Pause()` 在间隙点（每轮 Think 前 / 每个工具执行前）生效，循环产出
    `EventState(StatePaused)` + `EventPaused`（Session 快照）后迭代器正常结束 ——
    `Agent.Resume(ctx, sess, Response{})` 续跑（什么都没被问，Response 不被读取）。暂停点在工具执行前时，
    当前工具计入快照，Resume 先执行它。
  - **膜征询**：三态裁决——`Sandbox.Allow`（调用执行前）或 `Sandbox.Emit`（一轮文本被听到前）返回 `VerdictAsk`，产出同一对 `EventState(StateWaiting)` + `EventWaitInput`（问题即该裁决的 reason；`Emit` 的征询把草稿扣在 `Session` 里）。裁决是一次裁定，绝不去宿主文本里匹配字符串：`Response{Deny: 原因}` 拒绝（待执行调用拿到 `[sandbox-denied: 原因]` 反馈），`Deny` 为空且 `Answer` 非空即批准——待执行调用不再过膜直接执行，或被扣的草稿原样说出且不进下一次 Think。零值 `Response` 按拒绝处理（`[sandbox-denied: declined]`），没答上就 fail closed。
  - **可持久化**：`Session.Marshal()` / `UnmarshalSession` 提供带版本号的 JSON 持久化 ——
    挂起或暂停的循环可跨进程存活（对齐主流 checkpoint/resume）。
  超时由宿主控制（默认拒绝）；等待以挂起表达，从不在 Effector 内同步阻塞（阻塞会拖住 Close 的等待）
- **两侧三态裁决与反思原语** —— `Sandbox.Allow`（工具执行前）与 `Sandbox.Emit`（该轮文本
  被听到前）各返回 `Verdict`：`VerdictDeny`（零值，fail-closed）/ `VerdictAllow` / `VerdictAsk`，
  三态之外的值同样按 Deny 处置——本构建叫不出名字的值不可能"允许"什么；ask 经与 ask_user
  相同的挂起-恢复协议征询确认，批准后才生效。`OnCycleEnd(ctx, output,
  outcome)` 以 `CycleOutcome`（Done/Suspended/MaxRounds/Error/Aborted）分类每次 Cycle 的结束方式；
  `BeforeStimulate` 可写轮级反思便签到 `Prompt.Reflection`，全轮大脑可见
- **结构化工具反馈** —— 工具结果以 `Prompt.ToolResults` 回流（`ToolResult{ID, Name, Result, Err}`，
  单一轨道；`call_xxx` ID 保留）；渲染（tool 角色消息、`[tool_call_id=xxx]` 标记、纯文本）归内置
  大脑 —— 文本轨（`Context`）只保留宿主基底与 sandbox 裁决
- **工具级超时与重试** —— `Config.ToolTimeout` 约束每次工具执行；
  `ToolMaxRetries` 重试执行器错误（`Effect.Err` 业务错误永不重试）
- **并行工具批（可选）** —— `Config.ParallelActs` 并发执行一轮的多个独立工具调用
  （串行门禁 → 并行 Act → 按调用序串行反馈）；事件与钩子保持串行。默认关闭；要求
  并发安全的 Effector。`MaxParallelActs` 限制同批同时执行的调用数；
  `Session.RemainingCalls()` 暴露挂起时尚未执行的调用（无剩余时为空）
- **历史由宿主管理**（MemHop 模式）—— 上下文累积与记忆注入都是你的职责
- **扁平多 agent 模型** —— 一个 `Agent` 就是一个内核；多 agent 是宿主 `New` 出多个实例，子 agent 由宿主
  工具（`spawn_agent`）产生并消费其事件流。内核不做 agent 间通信，也绝不做框架级嵌套
  - 每个实例必须有自己的 `Organs.ID`（必填无默认）：`New` 拒绝无名 agent——它是每个事件的署名、每个
    `Session` 归属核对的依据
  - `Agent.ID()` 从已构造的实例把这个名字读回来，与每条 `Event.CellID` 同源——宿主同时跑多个 agent 时，
    事件路由与日志署名就按它分
- **阻力是反馈，不是失败** —— 被拒绝的工具与工具错误都以 `EventToolResult`
  反馈回流循环，循环继续

## 升级

### v1.3.x —— 当前表面（v1.2.0 之后三次 Breaking）

- **v1.3.8 —— agent 间那一整套从公开面净删除（Breaking）**：`internal/synapse` 整包（突触图、
  `Hebbian`/`STDP`/`Prune` 学习规则）与 cell 侧的委托/应答配对簿记一并删除。寻址、路由、投递、能力
  发现、突触权重全归宿主；一个 `Agent` 就是一个内核，多 agent 是宿主 `New` 多个实例。
- **v1.3.9 —— 身份与形状收口**：`Organs.ID` 必填且无默认（每个事件按它署名、每个 `Session` 按它核对）；
  挂起点名自己的成因（`Session.Kind()`），`Resume` 用同一个 `Response{Answer, Deny}` 应答四种挂起；
  应答与拒绝分轨。
- **v1.3.10 —— 官方大脑内置（Breaking）**：`Organs.Thinker`、`SlotThink`、`Replace("think", …)`、
  蓝图 `P1` 接线点与 `FallbackThinker` 全部删除。改为在 `Organs` 上填
  `BrainConfig{BaseURL, Key, Model, Stream, Mode}`——宿主不写 Thinker，公开面也没有 `Thinker` 类型；
  换模型是换参数重新 `New`，不是运行期换件。
- **v1.3.11 —— 接线检视面收窄（Breaking）**：删除 `BuildGraph`、`WiringGraph` 类型、`SlotsByTarget`
  与 `meowire.PauseGate` 别名。同一张图由 `Connectome()` / `ConnectomeNodes()` / `WiringDiagram(o)` /
  `RenderDiagram` / `RenderJSON` 覆盖，每条边自带 `TargetID`。

### v1.2.0

- **每个接线点现在都是必填** —— hooks H1–H8 必须全部设置（不需要行为的地方传显式
  no-op）；缺失回调 `New` 直接失败。`Sandbox` 与 `ContextBudget` 本就必填；现在
  循环层彻底不再容忍 nil —— `Bounds()` 快照、`EventSandbox` 审计判定与
  `Budget.Trimmer` 调用都是无条件的。
- **`Blueprint.Strict` 已移除** —— 不再有 warn 级可提升，删除 `Blueprint` 字面量中的该字段。
- **`ContextBudget` 完整性强制** —— Trimmer 为 nil 或 MaxTokens <= 0 装配失败
  （不裁剪的预算不是预算）；`TrimResults` 后来并入同一条规则 —— 两条累积轨各要一个裁剪器。
- **`Replace` 拒绝 nil/不完整端口** —— 换入的器官必须完整。
- **`Sandbox` 必须实现 `Bounds() string`** —— 返回执行边界描述；
  框架每次 `Stimulate`/`Resume` 快照一次，通过 `Prompt.Bounds` 以只读方式提供给 hook 与大脑：
  ```go
  func (s *MySandbox) Bounds() string { return "read-only /workspace" }
  ```
- **缺失端口错误为 `errors.Join` 聚合** —— 用 `errors.Is` 判（被连接的错误对它的每一部分都答 `Is`）；
  不要比较错误字符串。

## 架构

```
meowire (模块根)
  ├── api/                门面 + 组合根 —— 唯一可 import 的对外表面
  ├── internal/brain      内置大脑（全仓唯一 provider 包，openai-go）
  ├── internal/cell       agent 内核（ID + 端口 + DecisionLoop）
  ├── internal/nerve      决策循环、端口（含记忆）、钩子、事件、守卫
  ├── internal/testutil   六个宿主端口的共享测试桩，供 test/ 使用
  └── test/               集成测试与契约守卫
```

只有 `api/` 可被 import；`internal/*` 与它并列在模块根下，且 `internal/nerve` 从不 import
`internal/brain` —— 由 `api/` 的组合根构造大脑，再作为循环的 Thinker 交给 cell。

| 概念 | 位置 | 职责 |
|---|---|---|
| `Agent` / `New` / `Stimulate` / `Close` | `api/` | 门面：整个对外表面 |
| `DecisionLoop.Cycle` | `internal/nerve/loop.go` | 纯编排：Think → Act → 产出事件 |
| `Cell` | `internal/cell/cell.go` | 极简内核：ID + 端口 + 循环 |
| `Brain` | `internal/brain` | 内置器官：openai-go chat completions，由组合根按 `Organs.Brain` 构造 |
| `Effector` / `Closer` | 端口 | 宿主提供的能力 |
| `Hooks` | 端口 | BeforeStimulate / AfterStimulate / BeforeThink / AfterThink / BeforeAct / AfterAct / OnError / OnCycleEnd |
| `Sandbox` | 守卫 | 循环两侧的权限膜：每次 Act 前 `Allow`、该轮文本被听到前 `Emit`；`Bounds()` 经 `Prompt.Bounds` 把执行边界透传给大脑 |
| `ContextBudget` | 守卫 | 每次 Think 前裁剪文本轨 `Context` 与结构化反馈轨 `ToolResults`（同一额度） |
| `Memory` | 端口 | 把本轮召回写进 `Prompt.Memories`，并把结束那一轮的事实收回去 |
| `Event` | 事件 | 循环的类型化观察镜像 |

## 安装

```sh
go get github.com/qyiun666/meowire@latest
```

导入门面包 —— 唯一对外表面：

```go
import meowire "github.com/qyiun666/meowire/api"
```

宿主侧完整集成契约 —— 大脑参数、六端口、逐字段语义、事件流与陷阱清单 —— 见
[宿主集成指南](host-integration.md)。

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"os"

	meowire "github.com/qyiun666/meowire/api"
)

// effector 实现 meowire.Effector —— 工具执行端口。
type effector struct{}

func (effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
	return &meowire.Effect{Result: "ran " + a.Call.Name}, nil
}

// closer 实现 meowire.Closer —— 宿主资源清理。
type closer struct{}

func (closer) Close() error { return nil }

// sandbox 实现 meowire.Sandbox —— 守循环两侧的权限膜。
type sandbox struct{}

func (sandbox) Allow(ctx context.Context, a meowire.Action) (meowire.Verdict, string, error) {
	return meowire.VerdictAllow, "", nil
}

func (sandbox) Emit(context.Context, meowire.Utterance) (meowire.Verdict, string, error) {
	return meowire.VerdictAllow, "", nil
}

func (sandbox) Bounds() string { return "read-only /workspace" }

// memory —— meowire.Memory 的惰性实现（召回空、回写不存）。
type memory struct{}

func (memory) Recall(context.Context, meowire.MemoryQuery) ([]meowire.Record, error) {
	return nil, nil
}

func (memory) Remember(context.Context, meowire.CycleFacts) error { return nil }

func main() {
	// Blueprint：接线一次定义，多次 New —— 每个实例换自己的 Organs.ID
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-001",                                                             // 必填：事件署名与挂起句柄归属都按它判定
			Brain:   meowire.BrainConfig{Model: "gpt-5.2", Key: os.Getenv("OPENAI_API_KEY")}, // 内置大脑
			Act:     effector{},
			Closer:  closer{},
			Hooks:   meowire.FullHooks(meowire.Hooks{}), // 八个回调，显式 no-op
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

## 核心概念

### 事件流是观察镜像

`Stimulate` 运行循环的一个步骤并返回 `iter.Seq[Event]`。工具执行发生在循环**内部**
（通过你的 `Effector`），结果以结构化条目进入下一轮 `Prompt.ToolResults`。宿主观察
（`EventToolCall` / `EventToolResult`），但绝不向打开的迭代器回灌数据。

停止消费（yield 返回 false）**放弃本轮**：停止点及其后的工具**不会**执行，
本轮累积的所有状态被丢弃。`OnCycleEnd` 保证每次 `Cycle` 恰好执行一次 —— 正常、错误、中止三条路径都覆盖，并以 outcome 参数报告结束方式（`CycleOutcome`：Done/Suspended/MaxRounds/Error/Aborted）。

### Step-Resume（宿主主动接管）

停止迭代器，在宿主侧处理（异步工具、人工接管、外部服务），自行保存进度，然后再次
`Stimulate`。每次 `Stimulate` 都是无状态步骤 —— 这是宿主主动接管、长任务和重试的实现方式。
工具请求输入（ask_user）**不在此列**：工具返回 `Effect{WaitInput: 问题}`，循环携带不透明
`Session` 挂起，宿主经 `Agent.Resume(ctx, sess, resp)` 续跑（见下文“统一挂起-恢复”）。
两条路径互斥；用 break + `Stimulate` 模拟 ask_user 会丢失挂起上下文（`Session` 不透明，
无法手工重建）。

### 统一挂起-恢复

四种挂起 —— 工具请求输入（ask_user）、执行前征询、文本征询（两侧都是 `VerdictAsk`）与宿主请求暂停（Pause）—— 共用同一机制：
循环产出携带不透明 `Session` 快照的挂起事件后**迭代器正常结束**；宿主保存 Session
（可选经 `Session.Marshal()` / `UnmarshalSession` 跨进程持久化恢复），然后调用
`Agent.Resume(ctx, sess, resp)` 从挂起点继续 —— 不占轮次、等待期间不触发 budget。

`resp`（一个 `Response`）的含义由 `Session.Kind()` 决定，与答复文本长什么样无关：

- **ask_user**（`WaitTool`）：`Effect{WaitInput: 问题}` → `EventState(StateWaiting)` + `EventWaitInput`；
  `Response{Answer: 文本}` 以挂起工具的结构化结果注入，`Response{Deny: 原因}` 把该调用记为失败。
- **Pause**（`WaitPause`）：`Agent.Pause()` 在间隙点生效 → `EventState(StatePaused)` + `EventPaused`；
  `Resume(sess, Response{})` 续跑，不注入任何内容（什么都没被问）。暂停点在工具执行前时，
  当前工具（及其后的调用）计入 `Session.remaining`，Resume 先执行它们。
- **膜征询**（`WaitCallAsk` / `WaitUtterance`）：`Sandbox.Allow`（工具执行前）或 `Sandbox.Emit`（该轮文本被听到前）返回 `VerdictAsk` → 同样的 `EventState(StateWaiting)` +
  `EventWaitInput`（问题来自裁决 reason；文本征询的 `Call` 为零值，草稿扣在 `Session` 里）；`Deny` 按该原因拒绝（反馈落库为
  `[sandbox-denied: ...]` 规范形式），`Deny` 为空且 `Answer` 非空即批准：放行 pending
  调用（不再重新过门禁）或按原稿说出被扣住的草稿（不重跑该轮 Think）；零值 `Response` 记为拒绝
  （`[sandbox-denied: declined]`）—— 答复是一个裁决，不是拿去匹配的字符串。
- 恢复 pause 挂起会清掉那一次暂停请求，恢复后的循环不会在首个间隙点再次挂起；恢复其余成因的挂起不动暂停请求。`Agent.Unpause()` 只能撤销尚未生效的暂停请求。
- Session 单次使用：重复恢复会重放剩余工具调用（宿主责任）。
  暂停从不打断执行中的 Think/Act：它只在间隙点被兑现。

### 轮数上限

`Config.MaxRounds`（默认 8）是硬上限。如果最后一轮仍有未决的工具调用，循环以
`ErrMaxRounds` 结束 —— 该轮的工具结果未被再次思考。用 Step-Resume 从循环停止处继续。

### 记忆：时点归框架，存储归宿主

框架从不存储任何东西，`Memory` 端口只规定经验何时流动：每轮 Think 前 `Recall`
（进 `Prompt.Memories`，整轮替换）、每次调用终点 `Remember` 一次（带回 `CycleFacts`）。
存什么、怎么检索、何时删除仍由你决定（MemHop）。文本轨不变：`Organs.Context` 与
`Hooks.BeforeThink` 照旧喂 `Prompt.Context`。

## 多 agent

Meowire 是**扁平模型**：一个 `Agent` = 一个内核，**跨 agent 的事全部归宿主**。

- 子 agent：宿主的 `spawn_agent` 工具里 `New` 一个实例、消费它的 `Stimulate` 事件流，结果以
  `EventToolResult` 反馈回主循环——内核不知道有第二个实例存在
- 内核不提供 agent 间寻址、投递、回程配对、能力发现与突触图：`Organs` 上没有面向邻居的槽，
  `Prompt` 里没有入站信号轨（`test/wiring_free_test.go` 机械守住这条边界）
- 要互通就在宿主里做：把 A 的输出喂进 B 的 `Stimulate`，或把 B 注册成 A 的一个工具

## 开发

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
| English README | [README.md](README.md) |
| 宿主集成指南 | [host-integration.md](host-integration.md) |
| 英文宿主集成指南 | [host-integration.en.md](host-integration.en.md) |
| 大脑规格（openai-go） | [thinker-openai-go.md](thinker-openai-go.md) — 内置大脑的字段落点表与协议参考 |
| 宿主参考实现 | [reference-host.md](reference-host.md) — 按步骤从零实现一个 AI 宿主 |
| 协议映射指南 | [protocols.md](protocols.md) — MCP / A2A / AGENTS.md / Authority |
| Email | qyiun666@163.com |
