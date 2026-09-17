# 决策档案: 记忆升为必填端口（时点归内核）

Status: implemented

## Problem

判据是「宿主只实现器官就能装配出可用的内核」，而经验回灌这一环恰恰做不到：`internal/memory` 只是一份框架**不消费**的参考契约，真正把记忆接进循环要宿主自己写闭包——在 `BeforeStimulate`/`BeforeThink` 里召回、在 `AfterStimulate` 里保存。蓝图甚至把 H3 的 Desc 写成 "memory retrieval injection point"，即框架一边声称不管记忆，一边在契约里给记忆留出注入位。

缺的不是存储能力（MemHop 早已具备），是**框架调用时点**：没有时点，「何时召回、何时回写」就是各家宿主的私有约定，同一个内核在不同宿主下经验流动时机不同。

## Decision

记忆升为第七个必填端口 `Memory`（`Organs.Mem`，蓝图 `P7` + 隐含子槽 `P7b`，可换槽名 `mem`），内核只拥有两件事：

- **时点**：`Recall` 在预算裁剪之后、`BeforeThink` 钩子之前（每轮 Think 一次）；`Remember` 在 `OnCycleEnd` 之前，每次调用恰好一次，四条终态路径（done / error / 挂起 / 消费者放弃）都走到。
- **入参与出参的形状**：`MemoryQuery{CellID, Cue}` 只装框架知道的两件事（谁在问、被问了什么）；`CycleFacts{CellID, Input, Output, Outcome}` 只装框架构造得出的事实。返回值进 `Prompt.Memories` 这条**易失轨**——每轮整体替换、不累积、不进 `Session` 快照。

策略一律不进内核：检索算法、排序、条数、保留窗口、写什么、删除与遗忘（`Forget` 从契约里删除，删除是宿主对自己后端做的事，不是框架时点）。

失败语义按端口各自的方向定：`Recall` 失败即该轮 Think 失败（器官是装配的一部分，框架不替宿主决定「少一半上下文也想」）；`Remember` 失败不改写已定终态，经 `OnError` 报告。

双轨边界由此固定：**P7 = 结构化 `[]Record` 进脑；H3 = 文本轨 `p.Context` 覆写**。两者不重叠，`api/agent.md` 写明。

## Alternatives considered

- **维持宿主自己接线（现状）** —— 落选。正是判据要消灭的那类缺口：机制在框架里，时点约定散在宿主里。
- **内核直接依赖 MemHop** —— 落选，破「与 MemHop 互不依赖」的仓库边界，也破零三方依赖。
- **把 `Memory` 做成可选端口（nil = 不接记忆）** —— 落选。本仓纪律是「显式 no-op 是决定、缺席是缺器官」；可选端口会让时点重新变成宿主约定，等于没做。惰性生活函数（召回空、回写不存）就是「不要记忆」的正确表达。
- **保留 `Save`/`Forget` 三方法形状** —— 落选。`Save` 没有框架时点（何时保存是策略），`Forget` 同理且属删除；必填端口的每一个方法都必须是框架会调用的那一个。
- **`Recall` 每次调用一次（放在 `BeforeStimulate`）** —— 落选。多轮工具循环里第一轮召回的结果会被后续轮次沿用，而每轮的 `Input`/已累积反馈都变了；按 Think 召回才配得上「整轮替换」的易失轨语义。代价是宿主后端可能被每轮打一次，节流属于器官。

## Consequences

- **Breaking**：`New` 少一个 `Mem` 即失败；`api.Memory` 的三方法形状换成两方法；`internal/memory` 包整体消失（`Record` 形状进 `nerve`，`Query` 换成 `MemoryQuery`）。宿主跟版是独立轮次。
- 端口数从六升到七，与本仓「公开面收敛」的既有倾向相反。换来的收敛判据落成机制：新增必填端口必须同批交付 槽名 + Validate 分支 + 同步守卫 三件（`api/agent.md`、`slots_sync_test.go` 已承载），而不是重新变成一句禁令。
- 每轮 Think 前多一次外部调用，宿主后端的调用频次随轮数上升；框架不代做节流与缓存。
- 「框架不管理历史」这句旧措辞在两份 host guide 与 README 双语里改写为「时点归框架，存储归宿主」；`AGENTS.md` 的「明确不做」相应改口（仍不做存储/索引/检索算法/遗忘策略）。
