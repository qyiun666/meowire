# AGENTS.md — meowire

决策循环内核 / 连接层（`github.com/qyiun666/meowire`）。与 MemHop 互不依赖，两者只被宿主组装层 meowagent 同时引用。

## 意图（动手前先读，改完自检）

- **在解什么问题**：每个宿主都在自己重写一遍 Think→Act 循环，于是循环的语义（工具被拒怎么回流、挂起怎么恢复、一轮到底怎么算结束）各家都不同且都不对。本仓把这件苦活做成库：一条 Think→Act 事件流 + 七端口装配（Thinker / Effector / Closer / Hooks / Sandbox / ContextBudget / Memory），**评价任何改动的标准是它让端口契约更好装配还是更难装配**。`Thinker` 端口不内置任何实现：Prompt 渲染与 LLM 传输全归宿主，内核只定义进脑的数据包与出脑的裁决形状。
- **近三期靶子**：① 挂起-恢复这条主线从 v1.3.0/1.3.2 的 ask 挂起走到「膜守循环两侧 + 挂起成因入线」（`Verdict` 三态在 `Allow` 与 `Emit` 两侧同形，Session wire v3 带上归属 cell、按名编码的 waitKind 与被扣住的草稿），仍是当前重心；② `Prompt.Reflection` 槽位与 `CycleOutcome` 终态分类刚落在 "lite" 阶段，语义还没定齐；③ 公开面收敛（八钩子必填 + `OnCycleEnd` 终态 + 蓝图独占可换槽名 `WirePoint.Slot` + 预算端口覆盖两条累积轨 + 记忆端口 P7 + 出口裁决归端口不归钩子 P5c），新增一律先问能不能并入现有端口；不能并入且缺的是「框架调用时点」的，升为必填端口，并同批交付 槽名 + Validate 分支 + 同步守卫 三件。
- **明确不做**：不破零三方依赖；声明记忆端口的契约与调用时点（`Recall`/`Remember` + `CycleFacts` 事实回传），但存储、索引、检索算法、遗忘策略一律不做，实现仍归 MemHop/宿主；不做宿主工具（spawn 等归 meowagent）；不为宿主兼容调整自身端口设计。

## 跨仓边界（只负责自身）

- 本仓一切改动**只以自身语义正确性为准**：设计端口、事件流与钩子契约时不考虑宿主 meowagent 的现有实现，更不改它的文件
- 契约变更的唯一生效入口是**打 tag**——meowagent 经 `go.mod` require 远端版本消费本仓，跟版与适配是 meowagent 侧独立轮次的工作
- 父目录 `go.work` 把三仓源码在本地串起来，只用于「改完跑一次下游 `go build` 取证」；取证不等于动手，`go.work` 自身也不在本仓轮次内修改
- 跨仓写入由 `~/.qoder-cn/hooks/block-cross-repo.sh`（PreToolUse）机械拦截，被拒即停手收口，本轮不主动为宿主评估影响
