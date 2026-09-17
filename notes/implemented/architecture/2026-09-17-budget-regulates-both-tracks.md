# 决策档案: 预算调节器覆盖两条累积轨

Status: implemented

## Problem

`ContextBudget` 只裁文本轨 `Context`。一轮循环里真正无界增长的是两个字段：`Context`（钩子/记忆注入）与 `ToolResults`（每次工具执行 append 一条结构化反馈，直到本轮 Think 结束）。于是宿主声明的 `MaxTokens` 只描述了进脑内容的一半，工具轮数越多谎报越大——预算端口名为 budget，实为单轨裁剪器。

判据是「宿主只实现器官就能装配出可用内核」，因此缺口必须落在端口契约上，而不是等宿主自己在 `BeforeThink` 里手工清理 `Prompt.ToolResults`。

## Decision

一个调节器裁两轨：`ContextBudget` 增加 `TrimResults func([]ToolResult, int) []ToolResult`，与既有 `Trimmer` 在**同一时点**（每次 Think 前）、以**同一额度**（`MaxTokens`）执行。端口数量不变，仍是第六个必填端口。

两轨的划分由 Prompt 字段的累积性穷举得出：除 `Context` 与 `ToolResults` 外，其余字段（`Input`/`Plan`/`Reflection`/`Tools`/`System`）每轮整体替换而非 append，因而无需调节。该论证由 `metabolize()` 的注释与该字段的「整轮替换」不变式共同承载；一旦有人把某条轨改成累积，论证当场失效。

完整性由三处机械校验咬住：`api.Validate` 与 `Cell.Replace` 都拒绝缺任一裁剪函数或 `MaxTokens <= 0` 的 Budget；蓝图登记 `P6b Budget.TrimResults`（可选子槽，由主槽 P6 隐含，与 `P5b Sandbox.Bounds` 同形）。

## Alternatives considered

- **新增独立端口（如 `ResultBudget`）管反馈轨**：端口是宿主装配面的成本，第七个必填端口要留给真正缺「框架调用时点」的能力；同一额度拆成两个端口还会允许两者不一致。
- **在框架内做默认裁剪（截断尾部 N 条）**：内核不持有策略，任何内置默认都是替宿主决定丢什么——与本仓「无 stub、无默认实现」的纪律冲突，且丢错的代价由宿主承担。
- **让宿主在 `BeforeThink` 里自行覆写 `p.ToolResults`**：钩子拿到的是整包 Prompt，宿主能改，但那是把接线的活儿推回宿主侧，正好违反本轮判据。
- **复用 `Trimmer` 加类型分支**：一个函数两种入参形状，端口签名变成 `[]any`，失去编译期约束。

## Consequences

- **Breaking**：既有只填 `Trimmer` 的 `ContextBudget` 装配失败（`New` 报 error 级 Issue），宿主须补一个 `TrimResults`（不想裁剪时返回入参原切片）。跟版适配是宿主侧独立轮次。
- 两个裁剪器共享一个额度，宿主若想让反馈轨占更小份额，需在自己的闭包里做分配——框架不再提供第二个旋钮，这是「一个调节器」换来的简洁。
- 等待/挂起期间两轨都不裁剪（无 Think 即无调节），与既有语义一致。
- 附带收口：可换槽名的唯一事实源移到蓝图 `WirePoint.Slot`，`cell.Replace` 的分发表在、`api` 的 `Slot*` 常量与 `Connectome()` 三方由守卫测试钉死（不再是三处手写副本）。
