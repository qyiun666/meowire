# Meowire

Go 语言仿生 agent 编排基座 —— **纯装配，零默认实现。**

Meowire 是一个用于构建 agent 宿主的极简决策循环内核。它负责编排（Think → Act → 产出事件），
其余全部交给你：LLM、工具、记忆、安全策略。框架不绑架你的技术栈 —— 只有一个干净、零依赖、
值得信赖的循环。

> 需要 Go 1.27+（使用 `iter.Seq`）。

## 为什么选择 Meowire

- **智能由你掌控。** Meowire 不提供 LLM 适配器、工具框架或记忆后端 —— 它注入六个宿主端口，
  期待你来实现它们。框架从不掩盖 agent 实际做了什么。
- **零依赖。** 仅标准库。没有需要审计的传递依赖树。
- **小巧可读。** 源码约 1900 行。整个循环只有一个文件（`internal/nerve/loop.go`，含测试约 530 行）。
- **内部完全封闭。** 所有实现位于 `internal/` 下 —— Go 编译器保证唯一可 import 的对外表面是
  `api/` 包（`New` / `Stimulate` / `Close` + 契约类型）。

## 特性

- **Think→Act 决策循环**，支持每轮重试与硬性轮数上限
- **类型化事件流** —— `Stimulate` 返回 `iter.Seq[Event]`，宿主观察
  `EventText`、`EventToolCall`、`EventToolResult`、`EventState`、`EventDone`、`EventError`、`EventUsage`、
  `EventSandbox`（动作级审计记录）、`EventWaitInput`（循环挂起等待外部输入）、
  `EventPaused`（暂停请求生效 —— 快照 + 恢复句柄）、`EventReplace`（端口替换审计记录）、
  `EventConfig`（配置替换审计记录）
- **动作级审计轨迹** —— 每个 Sandbox 决策（允许/拒绝）产出 `EventSandbox`
  判定（工具、原因、错误）；持久化事件流即得完整"谁/做了什么/为什么被允许"审计，
  符合 Authority 安全模型
- **接线图检视** —— `Connectome`/`Validate`/`RenderDiagram`/`RenderJSON` 把装配视为
  图（数据对象节点 + 插槽边），供人或机器渲染
- **动态接线（突触可塑性）** —— `Agent.Replace(slot, port)` 运行时替换
  `Think`/`Act`/`Sandbox`/`Budget`/`Hooks`；下次 `Stimulate` 生效，飞行中的 `Stimulate` 保留原端口；
  每次成功替换以 `EventReplace` 在下次 Stimulate/Resume 开头审计产出
- **运行期配置热更新** —— `Agent.UpdateConfig(cfg)` / `Agent.GetConfig()` 热调
  `MaxRounds` 等标量限制，免整 Agent 重建
- **Agent Card（A2A 就绪）** —— `AgentCard(Organs)` 把装配渲染为机器可读能力卡（JSON）；
  发布到 `/.well-known/agent-card.json` 即可被其他 agent 发现
- **A2A 风格任务状态** —— `Signal.Status` 携带六个任务生命周期状态
  （submitted/working/needs-input/completed/failed/cancelled），端到端追踪跨 agent 任务
- **可塑突触图** —— `Synapse` 五方法契约（Link 带权重/Unlink/Reinforce/Edges）+
  参考学习规则 `Hebbian`/`STDP`/`Prune`（宿主侧；框架只存状态，从不决定何时学习）；
  持久化往返：`Edges` 导出 + `NewDirect` 恢复
- **统一合成视图** —— `BuildComposite`/`RenderComposite`/`RenderCompositeJSON` 把
  静态装配子图与实时突触图合并为一张图（视图统一、数据分离）
- **六个宿主注入端口，全部器官必填**（无 stub、无可选接线）：
  `Thinker`（LLM）、`Effector`（工具）、`Closer`（清理）、`Hooks`（拦截 ——
  全部八个回调 H1–H8 必填：显式 no-op，而非缺席）、`Sandbox`（权限膜）、
  `ContextBudget`（上下文调节器 —— 必须有 Trimmer 与 MaxTokens）
- **Step-Resume** —— 每次 `Stimulate` 是一个无状态步骤；停止迭代器，在宿主侧处理
  （异步任务、人工接管、`ErrMaxRounds` 续跑），再 `Stimulate` 继续。工具请求输入（ask_user）
  不在此列，走下面的统一挂起-恢复协议（唯一形式）
- **统一挂起-恢复（v1.3.2 起）** —— 三种挂起共用同一条快照 + 恢复路径：
  - **ask_user**：工具返回 `Effect{WaitInput: 问题}`，循环产出 `EventState(StateWaiting)` +
    `EventWaitInput`（工具、问题、不透明 `Session`）后**迭代器正常结束** ——不阻塞、不占轮次、
    等待期间不触发 budget。`Agent.Resume(ctx, sess, response)` 续跑：响应以挂起工具的结构化
    结果进入循环（`Prompt.ToolResults` 条目，ID 保留），先执行剩余工具，再从挂起轮继续。
  - **Pause**：`Agent.Pause()` 在间隙点（每轮 Think 前 / 每个工具执行前）生效，循环产出
    `EventState(StatePaused)` + `EventPaused`（Session 快照）后迭代器正常结束 ——
    `Agent.Resume(ctx, sess, "")` 续跑（无 pending 工具可注入）。暂停点在工具执行前时，
    当前工具计入快照，Resume 先执行它。
  - **可持久化**：`Session.Marshal()` / `UnmarshalSession` 提供带版本号的 JSON 持久化 ——
    挂起或暂停的循环可跨进程存活（对齐主流 checkpoint/resume）。
  超时由宿主控制（默认拒绝）；替代旧的在 Effector 内同步阻塞做法
- **三态 Sandbox 裁决与反思原语** —— `Sandbox.Allow` 返回 `Verdict`：
  `VerdictDeny`（零值，fail-closed）/ `VerdictAllow` / `VerdictAsk`——ask 经与
  ask_user 相同的挂起-恢复协议征询确认，批准后才执行。`OnCycleEnd(ctx, output,
  outcome)` 以 `CycleOutcome`（Done/Suspended/MaxRounds/Error/Aborted）分类每轮结束方式；
  `BeforeStimulate` 可写轮级反思便签到 `Prompt.Reflection`，全轮 Thinker 可见
- **结构化工具反馈** —— 工具结果以 `Prompt.ToolResults` 回流（`ToolResult{ID, Name, Result, Err}`，
  单一轨道；`call_xxx` ID 保留）；渲染（tool 角色消息、`[tool_call_id=xxx]` 标记、纯文本）归宿主
  Thinker —— 文本轨（`Context`）只保留宿主基底与 sandbox 裁决
- **工具级超时与重试** —— `Config.ToolTimeout` 约束每次工具执行；
  `ToolMaxRetries` 重试执行器错误（`Effect.Err` 业务错误永不重试）
- **历史由宿主管理**（MemHop 模式）—— 上下文累积与记忆注入都是你的职责
- **扁平多 agent 模型** —— 子 agent 与 agent 间通信是宿主工具
  （`spawn_agent` / `send_message`），绝不做框架级嵌套
- **阻力是反馈，不是失败** —— 被拒绝的工具、工具错误、目标繁忙都以 `EventToolResult`
  反馈回流循环，循环继续

## 升级到 v1.2.0

- **每个接线点现在都是必填** —— hooks H1–H8 必须全部设置（不需要行为的地方传显式
  no-op）；缺失回调 `New` 直接失败。`Sandbox` 与 `ContextBudget` 本就必填；现在
  循环层彻底不再容忍 nil —— `Bounds()` 快照、`EventSandbox` 审计判定与
  `Budget.Trimmer` 调用都是无条件的。
- **`Blueprint.Strict` 已移除** —— 不再有 warn 级可提升，删除 `Blueprint` 字面量中的该字段。
- **`ContextBudget` 完整性强制** —— Trimmer 为 nil 或 MaxTokens <= 0 装配失败
  （不裁剪的预算不是预算）。
- **`Replace` 拒绝 nil/不完整端口** —— 换入的器官必须完整。
- **`Sandbox` 必须实现 `Bounds() string`** —— 返回执行边界描述；
  框架每次 `Stimulate` 快照一次，通过 `Prompt.Bounds` 以只读方式提供给 hook 与 Thinker：
  ```go
  func (s *MySandbox) Bounds() string { return "read-only /workspace" }
  ```
- **缺失端口错误为 `errors.Join` 聚合** —— 请用 `errors.Is` / `strings.Contains` 匹配，
  切勿精确比较错误字符串。

## 架构

```
meowire (模块根)
  └── api/            门面 + 组合根 —— 唯一对外表面
      ├── internal/cell     agent 内核（ID + 端口 + DecisionLoop）
      ├── internal/nerve    决策循环、端口、钩子、事件、守卫
      ├── internal/synapse  个体间连接与信号投递契约（宿主参考）
      └── internal/memory   记忆 CRUD 契约（宿主参考，框架不消费）
```

| 概念 | 位置 | 职责 |
|---|---|---|
| `Agent` / `New` / `Stimulate` / `Close` | `api/` | 门面：整个对外表面 |
| `DecisionLoop.Cycle` | `internal/nerve/loop.go` | 纯编排：Think → Act → 产出事件 |
| `Cell` | `internal/cell/cell.go` | 极简内核：ID + 端口 + 循环 |
| `Thinker` / `Effector` / `Closer` | 端口 | 宿主提供的能力 |
| `Hooks` | 端口 | BeforeStimulate / AfterStimulate / BeforeThink / AfterThink / BeforeAct / AfterAct / OnError / OnCycleEnd |
| `Sandbox` | 守卫 | 工具权限门，每次 Act 前调用；`Bounds()` 经 `Prompt.Bounds` 把执行边界透传给 Thinker |
| `ContextBudget` | 守卫 | 每次 Think 前裁剪上下文 |
| `Event` | 事件 | 循环的类型化观察镜像 |
| `Synapse` / `Memory` | internal | 供宿主参考的独立契约 |

## 安装

```sh
go get github.com/qyiun666/meowire@latest
```

导入门面包 —— 唯一对外表面：

```go
import meowire "github.com/qyiun666/meowire/api"
```

宿主侧完整集成契约 —— 六端口、逐字段语义、事件流与陷阱清单 —— 见
[宿主集成指南](host-integration.md)。

## 快速开始

```go
package main

import (
	"context"
	"fmt"

	meowire "github.com/qyiun666/meowire/api"
)

// thinker 实现 meowire.Thinker —— LLM 端口。
type thinker struct{}

func (thinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	return &meowire.Decision{Text: "Hello from meowire!"}, nil
}

// effector 实现 meowire.Effector —— 工具执行端口。
type effector struct{}

func (effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
	return &meowire.Effect{Result: "ran " + a.Call.Name}, nil
}

// closer 实现 meowire.Closer —— 宿主资源清理。
type closer struct{}

func (closer) Close() error { return nil }

// sandbox 实现 meowire.Sandbox —— 工具权限门。
type sandbox struct{}

func (sandbox) Allow(ctx context.Context, a meowire.Action) (meowire.Verdict, string, error) {
	return meowire.VerdictAllow, "", nil
}

func (sandbox) Bounds() string { return "read-only /workspace" }

func main() {
	// Blueprint：一次定义，多次 New（flat model 多 agent）
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
`Session` 挂起，宿主经 `Agent.Resume(ctx, sess, response)` 续跑（见下文“统一挂起-恢复”）。
两条路径互斥；用 break + `Stimulate` 模拟 ask_user 会丢失挂起上下文（`Session` 不透明，
无法手工重建）。

### 统一挂起-恢复（v1.3.2）

三种挂起 —— 工具请求输入（ask_user）、Sandbox 征询（`VerdictAsk`）与宿主请求暂停（Pause）—— 共用同一机制：
循环产出携带不透明 `Session` 快照的挂起事件后**迭代器正常结束**；宿主保存 Session
（可选经 `Session.Marshal()` / `UnmarshalSession` 跨进程持久化恢复），然后调用
`Agent.Resume(ctx, sess, response)` 从挂起点继续 —— 不占轮次、等待期间不触发 budget。

- **ask_user**：`Effect{WaitInput: 问题}` → `EventState(StateWaiting)` + `EventWaitInput`；
  响应以挂起工具的结构化结果注入。
- **Pause**：`Agent.Pause()` 在间隙点生效 → `EventState(StatePaused)` + `EventPaused`；
  `Resume(sess, "")` 续跑，不注入任何内容（无 pending 工具）。暂停点在工具执行前时，
  当前工具（及其后的调用）计入 `Session.remaining`，Resume 先执行它们。
- **Sandbox 征询**：`Sandbox.Allow` 返回 `VerdictAsk` → 同样的 `EventState(StateWaiting)` +
  `EventWaitInput`（问题来自裁决 reason）；响应语法与 ask_user 一致 —— 空串拒绝
  （`[sandbox-denied: declined]`）、`[denied:` 前缀按文本拒绝（反馈落库为
  `[sandbox-denied: ...]` 规范形式）、其余任何响应批准并放行 pending
  调用（不再重新过门禁）。
- `Agent.Resume` 自动清除失效的暂停请求；`Agent.Unpause()` 只能撤销尚未生效的暂停请求。
- Session 单次使用：重复恢复会重放剩余工具调用（宿主责任）。
  该统一模型取代旧的阻塞式 PauseGate 等待（v1.3.2 breaking）。

### 轮数上限

`Config.MaxRounds`（默认 8）是硬上限。如果最后一轮仍有未决的工具调用，循环以
`ErrMaxRounds` 结束 —— 该轮的工具结果未被再次思考。用 Step-Resume 从循环停止处继续。

### 记忆由宿主管理（MemHop）

框架从不存储历史。你自行维护对话上下文，通过 `Organs.Context` 注入
（或通过 `Hooks.BeforeThink` 按轮注入）。`internal/memory` 提供参考性的 `Memory` 契约
（`Save` / `Recall` / `Forget`）供你的后端实现 —— 框架不消费它。

## 多 agent

Meowire 是**扁平模型**：一个 `Agent` = 一个内核。宿主拥有所有实例。

- 子 agent：`spawn_agent` 宿主工具，结果以 `EventToolResult` 反馈回流
- agent 间通信：`send_message` 宿主工具（路由语义参考 `internal/synapse`）；
  阻力（目标繁忙、未知 agent）成为反馈，绝不是硬停止
- 能力发现：`AgentCard` 渲染 A2A 风格卡片；`Signal.Status`（A2A 六态任务生命周期）
  端到端追踪每个跨 agent 任务

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
| 宿主集成指南 | [host-integration.md](host-integration.md) |
| 宿主参考实现 | [reference-host.md](reference-host.md) — 按步骤从零实现一个 AI 宿主 |
| 协议映射指南 | [protocols.md](protocols.md) — MCP / A2A / AGENTS.md / Authority |
| Email | qyiun666@163.com |
