# 协议映射指南：meowire 与 2026 行业标准

> meowire 是一个接线内核：内置大脑说 OpenAI 兼容线协议，除此之外不内置
> 任何互操作协议，也不禁止任何协议。
> 本文说明宿主如何把 meowire 的端口/事件/契约映射到 2026 年主流的
> Agent 互操作标准（MCP / A2A / AGENTS.md / Authority），作为宿主侧
> 实现的执行指南。框架本身不消费这些协议——实现全部在宿主域。

## 背景：2026 年的 Agent 生态坐标

- **Agent = Model + Harness**（OpenAI 与 LangChain 共同确立的行业公式）：
  模型负责推理，Harness 负责上下文、工具、沙箱、记忆、权限与闭环。
- **MCP + A2A 双层模型**：Agent→工具用 MCP（Anthropic，JSON-RPC，
  OAuth 2.1 授权）；Agent→Agent 用 A2A（Google 发起，Linux Foundation
  标准，v1.0 生产可用）。两者互补，生产系统都需要。
- **Authority 模型**：安全控制点从"你能访问什么"（Authorization）移向
  "这个具体动作此刻是否应发生"（Authority）——每次动作执行前独立
  决策 + 完整审计。
- **Harness 演进**：1.0 Bolt-On → 2.0 Co-Training（模型吸收能力，
  harness 变薄）→ 3.0 Attention（harness 只留权限、身份、信任、
  可解释性，即 human attention policy surface）。

meowire 的设计（纯接线、六个宿主端口 + 内置大脑、动作级拦截、事件流观测）与上述
坐标天然对齐：互操作协议是宿主域，接线是框架域。

## 映射总表

| meowire 概念 | 行业标准/模式 | 宿主实现位置 |
|---|---|---|
| 内置大脑 | LLM 调用（OpenAI 兼容端点，模型经参数指定） | `Organs.Brain` 传参 |
| `Effector` 端口 | MCP client（工具调用） | `Organs.Act` |
| `ToolSpec` | MCP server 的 `tools` 列表投影 | `Organs.Tools` |
| `Sandbox.Allow` | Authority 动作级授权（每次执行前） | `Organs.Sandbox` |
| `Sandbox.Emit` | 出口审查（该轮文本被听到前） | `Organs.Sandbox` |
| `EventSandbox` | 审计记录（谁/什么/为什么被允许，两侧同一通道） | 事件流持久化 |
| `CycleOutcome` / `OnCycleEnd` | A2A 任务终态的唯一判词（映射归宿主） | 宿主任务表 |
| `Methods` | Agent Card 的 skills 名单（卡片由宿主渲染发布） | `Organs.Methods` |
| `System` / `Identity` | AGENTS.md（人机协作边界） | `Organs.System` / `Identity` |
| `ContextBudget` | 上下文工程（context rot 治理） | `Organs.Budget` |
| `Memory` 端口 | 分层记忆的调用时点（召回进脑、终态回写） | `Organs.Mem` |
| Step-Resume | 长时任务状态外化（checkpoint） | 宿主历史 + 重新 `Stimulate` |

## 1. MCP（Agent → 工具）

- `Effector.Act(ctx, Action)` 是宿主实现 MCP client 的落点：`Action.Call`
  携带工具名与参数（JSON Schema 描述来自 `ToolSpec`），宿主把调用
  翻译为 MCP `tools/call`，把结果翻译为 `Effect`。
- `ToolSpec` 列表可在每次 `Stimulate` 前由宿主从 MCP server 拉取并注入
  （`Hooks.BeforeStimulate` 写回 `Tools`），实现工具的动态发现。
- 鉴权：MCP 已把 OAuth 2.1 定为授权标准；宿主在 `Act` 内做令牌交换，
  每次操作签发范围最小、可审计的临时凭证。`Sandbox` 可叠加策略门。
- 参考实现：[modelcontextprotocol](https://github.com/modelcontextprotocol)。

## 2. A2A（Agent → Agent）——映射全部落在宿主域

meowire 采用扁平模型：一个 Agent 一个内核，宿主管实例（`spawn_agent` 宿主工具）。
**内核不携带 agent 间信封**——委托、投递、答复、配对、能力卡与路由都在宿主实现。
框架只保证一件对 A2A 有用的事：每个 agent 的一次运行**终态可判**。

- **任务状态映射**：宿主把一次 `Stimulate` 当作 A2A 的一个 task 来跑，收尾时按
  `OnCycleEnd(ctx, output, outcome)` 给的 `CycleOutcome` 写状态——
  Done→completed、Suspended→needs-input、Error/MaxRounds→failed、消费者中途放弃迭代器→
  Aborted。**框架四条都给判词**，`OutcomeAborted` 不是沉默——「不回话」是宿主策略，不是框架没算完。
  两个映射要宿主自己补：ctx 取消在框架侧只落 `OutcomeError`（`OnCycleEnd` 分不清它和器官失败），
  要映射成 cancelled 得从 `EventError.Err` 上 `errors.Is(err, context.Canceled)` 判；`OutcomeError`
  也不区分是哪根轨失败。六态语义归协议，写入者归宿主。
- **能力发现**：`Organs.Methods`（能力清单，只描述不消费）就是 A2A Card 的
  skills 名单来源，宿主自行渲染成 JSON 并发布到 `/.well-known/agent-card.json`；
  框架不产出卡片。
- **传输与路由**：A 怎么找到 B（channel/HTTP/Redis/gRPC）、谁能被寻址，全是宿主的事。
  跨进程时把 `EncodeEvent` 的 JSON 记录直接当传输单元即可——每条自带产出它的
  `CellID`、`Seq`、`TS`、按名字编码的枚举与版本闸门（事件线格式当前 v2，Session 线格式当前 v4），
  读端不认就拒读，不做降级解释。
  可携带性是有边界的：三枚框架哨兵（`ErrMaxRounds`/`ErrForeignSession`/`ErrCellClosed`）按 code
  原样恢复，`errors.Is` 照旧成立；宿主自己的错误只回来同样的文本，并在 `Dropped` 里点名 `err.identity`；
  膜的裁决错误同理点名为 `verdict.err.identity`。**写侧直接拒**的是名字表拼不出的值（未知 kind、
  未知 state、非 state 事件却带 state 名、未知 ruling）以及序列化失败的 `Session`——制造一条永远读不回的记录，
  比拒绝它更糟。枚举名表住在哪里：12 个事件名 `internal/nerve/event.go`、7 个状态名 `state.go`、
  4 个挂起成因名 `port.go`、3 个裁决名 `sandbox.go`；字段级说明见 `host-integration.md` §6.2/§6.5。
- 参考实现：[A2A 官方仓库](https://github.com/a2aproject/A2A)（Linux
  Foundation）。

## 3. AGENTS.md（人机协作边界）

- AGENTS.md 类文件是"人类为中心"的协作政策面（Harness 3.0 的
  human attention policy surface 雏形）：告知 Agent 权限边界、工作
  规范、验收标准。
- meowire 宿主把 AGENTS.md 内容注入 `Organs.System`（系统指令）与
  `Organs.Identity`（身份/人格），框架不解析格式——政策是文本，
  边界是代码（`Sandbox`）。

## 4. Authority（两侧授权 + 审计）

- `Sandbox.Allow(ctx, Action)` 在**每次工具执行前**被调用，`Sandbox.Emit(ctx,
  Utterance)` 在**该轮文本被任何人听到前**被调用——循环两侧各一个授权点
  （对比"登录后一路放行"的会话级授权）。宿主在此结合
  身份、被授予的权限、组织策略、声明的 intent 与实时上下文
  做决策。
- **裁决**：两个方法各返回三态 `Verdict`——`VerdictAllow` 放行、
  `VerdictDeny`（零值，fail-closed）拒绝、`VerdictAsk` 挂起征询外部确认
  （复用统一的挂起-恢复协议，批准才生效；文本侧征询时草稿扣在 `Session` 里）。
- **审计**：每次裁决产出 `EventSandbox` 事件，携带 CellID、工具调用（文本侧裁决为
  零值）、裁决（Ruling）、策略原因/征询问题与评估错误；ask 裁决以终结的第二条记录闭合
  审计链。宿主持久化事件流即得到完整审计日志（谁、代表谁、何时、做了什么、
  为什么被允许）。
- `Sandbox.Bounds()` 在每次 `Stimulate`/`Resume` 的序言里快照进 `Prompt.Bounds`，
  把执行边界告知 LLM——边界既是拦截也是提示。

## 5. 长时任务（状态外化）

Anthropic 长时 Agent 的核心结论：上下文压缩不够，**状态必须外化**
（文件系统/事件日志 > 聊天记录；交接物 > 记忆回放）。meowire 的
配合方式：

- Step-Resume：每个 `Stimulate` 是无状态 step；宿主在 step 之间把
  中间产物（计划、进度、交接物）落盘，下次 `Stimulate` 前经
  `Hooks.BeforeStimulate` 注入原型（写回即全轮生效）。`BeforeThink` 是**每轮**都触发的槽，
  且它与循环共享底层数组（只能整体替换 `p.Context`，不能 append），跨 step 的常驻注入不要放它那里。
- 事件流即日志：把 `iter.Seq[Event]` 序列化（append-only 事件日志 /
  WAL），宿主可随时重建或审计任意 step 的执行轨迹。
- 崩溃恢复：宿主重放事件日志 → 重建上下文 → 重新 `Stimulate`。

## 5b. 动态接线（运行时换器官）

`Agent.Replace(slot, port)` 支持在两次 `Stimulate` 之间替换运行端口
（act/sandbox/budget/mem/hooks）：换沙箱策略、换工具集都不必重建
Agent；大脑无槽，换模型是重新 `New`。这与 DeepSeek Harness 的运行时热插拔是
同一哲学，但 meowire 保持编译期类型安全（Replace 做端口类型断言）
与"飞行中的 Stimulate 不受影响"的语义——每次 `Stimulate` 快照
端口构造全新 LoopContext，替换只在下次生效。

## 6. 与 DeepSeek Harness（Cordis）的定位差异

DeepSeek Harness（2026.08 开源，MIT）以 Cordis 元框架实现
"一切皆插件"：模型、工具、循环、沙箱全部是可热插拔的运行时插件，
适合做"平台/系统"。meowire 是**编译期接线的内核基座**：端口在
`New` 时校验注入，循环固定，宿主掌握全部实现。二者同源（依赖注入 +
组合根），取舍如下：

| | meowire | DeepSeek Harness (Cordis) |
|---|---|---|
| 接线时机 | 编译期装配（`New` 校验）+ 运行时替换（`Replace`） | 运行时（热插拔） |
| 内核职责 | 决策循环 + 事件流 | 插件加载/卸载/依赖管理 |
| 语言 | Go，内核零三方依赖（模块带 openai-go 大脑） | TypeScript 生态 |
| 适用 | 嵌入宿主程序的 Agent 内核 | 独立 Agent 运行时平台 |

选择建议：需要嵌入、最小依赖、可审计的内核 → meowire；需要运行时
热插拔的完整平台 → DeepSeek Harness。
