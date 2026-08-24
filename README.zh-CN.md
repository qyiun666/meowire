# Meowire

Go 语言仿生 agent 编排基座 —— **纯装配，零默认实现。**

Meowire 是一个用于构建 agent 宿主的极简决策循环内核。它负责编排（Think → Act → 产出事件），
其余全部交给你：LLM、工具、记忆、安全策略。框架不绑架你的技术栈 —— 只有一个干净、零依赖、
值得信赖的循环。

> 需要 Go 1.26+（使用 `iter.Seq`）。

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
  `EventSandbox`（动作级审计记录）
- **动作级审计轨迹** —— 每个 Sandbox 决策（允许/拒绝）产出 `EventSandbox`
  判定（工具、原因、错误）；持久化事件流即得完整"谁/做了什么/为什么被允许"审计，
  符合 Authority 安全模型
- **接线图检视** —— `Connectome`/`Validate`/`RenderDiagram`/`RenderJSON` 把装配视为
  图（数据对象节点 + 插槽边），供人或机器渲染
- **动态接线（突触可塑性）** —— `Agent.Replace(slot, port)` 运行时替换
  `Think`/`Act`/`Sandbox`/`Budget`/`Hooks`；下次 `Stimulate` 生效，飞行中的 `Stimulate` 保留原端口
- **Agent Card（A2A 就绪）** —— `AgentCard(Organs)` 把装配渲染为机器可读能力卡（JSON）；
  发布到 `/.well-known/agent-card.json` 即可被其他 agent 发现
- **A2A 风格任务状态** —— `Signal.Status` 携带六个任务生命周期状态
  （submitted/working/needs-input/completed/failed/cancelled），端到端追踪跨 agent 任务
- **六个宿主注入端口**（全部必填，无 stub）：
  `Thinker`（LLM）、`Effector`（工具）、`Closer`（清理）、`Hooks`（拦截）、
  `Sandbox`（权限门）、`ContextBudget`（上下文裁剪）
- **Step-Resume** —— 每次 `Stimulate` 是一个无状态步骤；停止迭代器，在宿主侧处理
  （人工审批、异步任务），再 `Stimulate` 继续。无需框架支持即可实现人机协作
- **Pause/Resume** —— 间隙点（每轮 Think 前 / 每个工具执行前）的进程内暂停；
  循环产出 `EventState(StatePaused)` 并保留循环内状态，直到恢复
- **工具级超时与重试** —— `Config.ToolTimeout` 约束每次工具执行；
  `ToolMaxRetries` 重试执行器错误（`Effect.Err` 业务错误永不重试）
- **历史由宿主管理**（MemHop 模式）—— 上下文累积与记忆注入都是你的职责
- **扁平多 agent 模型** —— 子 agent 与 agent 间通信是宿主工具
  （`spawn_agent` / `send_message`），绝不做框架级嵌套
- **阻力是反馈，不是失败** —— 被拒绝的工具、工具错误、目标繁忙都以 `EventToolResult`
  反馈回流循环，循环继续

## 升级到 v2.0

- **`New` 接收单个 `Blueprint{Organs, Config, Strict}`** —— 一次定义、多次 `New`：
  在调用点把 `Organs`/`Config` 包进 `Blueprint`。`Strict: true` 时 warn 级装配问题
  （半配 hook 对、记忆/计划通路不完整）也会被拒绝。
- **`Sandbox` 现在必须实现 `Bounds() string`** —— 返回执行边界描述；
  框架每次 `Stimulate` 快照一次，通过 `Prompt.Bounds` 以只读方式提供给 hook 与 Thinker：
  ```go
  func (s *MySandbox) Bounds() string { return "read-only /workspace" }
  ```
- **缺失端口错误改为 `errors.Join` 聚合** —— 单端口缺失保持精确的 `meow: required port X not injected` 格式；
  请用 `errors.Is` / `strings.Contains` 匹配，切勿精确比较错误字符串。

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
[宿主集成指南](docs/host-integration.md)。

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

func (sandbox) Allow(ctx context.Context, a meowire.Action) (bool, string, error) {
	return true, "", nil
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
（通过你的 `Effector`），结果自动追加到下一轮 `Prompt.Context`。宿主观察
（`EventToolCall` / `EventToolResult`），但绝不向打开的迭代器回灌数据。

停止消费（yield 返回 false）**放弃本轮**：停止点及其后的工具**不会**执行，
本轮累积的所有状态被丢弃。`OnCycleEnd` 保证每次 `Cycle` 恰好执行一次 —— 正常、错误、中止三条路径都覆盖。

### Step-Resume（人机协作）

停止迭代器，在宿主侧执行工具（人工审批、异步工作、外部服务），把结果追加到你的历史中，
然后再次 `Stimulate`。每次 `Stimulate` 都是无状态步骤 —— 这是实现 `ask_user`、
长任务和重试的推荐方式。

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
| 宿主集成指南 | [docs/host-integration.md](docs/host-integration.md) |
| 协议映射指南 | [docs/protocols.md](docs/protocols.md) — MCP / A2A / AGENTS.md / Authority |
| Email | qyiun666@163.com |
