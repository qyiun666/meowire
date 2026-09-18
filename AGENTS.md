# AGENTS.md — meowire

单 agent 决策循环内核（`github.com/qyiun666/meowire`）。与 MemHop 互不依赖，两者只被宿主组装层 meowagent 同时引用。

## 意图（动手前先读，改完自检）

- **在解什么问题**：每个宿主都在自己重写一遍 Think→Act 循环，于是循环的语义（工具被拒怎么回流、挂起怎么恢复、一轮到底怎么算结束）各家都不同且都不对。本仓把这件苦活做成库：一条 Think→Act 事件流 + 七端口装配（Thinker / Effector / Closer / Hooks / Sandbox / ContextBudget / Memory），**评价任何改动的标准是它让端口契约更好装配还是更难装配**。`Thinker` 端口不内置任何实现：Prompt 渲染与 LLM 传输全归宿主，内核只定义进脑的数据包与出脑的裁决形状。
- **近三期靶子**：① 挂起-恢复（`Verdict` 三态在 `Allow` 与 `Emit` 两侧同形，Session wire 带上归属 cell、按名编码的 `WaitKind` 与被扣住的草稿）；`Resume` 收 `Response{Answer, Deny}`，四成因共用，`Session.Kind()` 点名该读哪个字段；`[denied:` 前缀协议不存在——宿主写进 `Answer` 的文本哪怕以 `[denied:` 开头也只是文本；② **一个 `Agent` 就是一个内核，宿主只实现七个端口就装配出可用的单 agent**——判据由 `test/public_organs_test.go`（器官全用公开名写出、一次 `Stimulate` 跑完两轮）与 `test/wiring_free_test.go`（机械扫描宿主侧文件里是否出现 channel/goroutine/接收，以及 `Organs` 是否多出面向邻居的槽）钉住；一个端口后面可以有多个实现（组合器 `GuardStack`/`Fallback*` 返回端口类型本身，蓝图一枚插槽都没多）且器官可以先启动再使用（`Bootable`，顺序由 `PortOrder()` 从蓝图导出），事件流本身可以落盘且自带版本闸门（`EncodeEvent`/`DecodeEvent`：枚举按名字上线且**写侧就拒绝名表之外的值**（不然制造的是永远读不回的那条记录）、过不去的值在 `Dropped` 里点名、每条事件带 `CellID`/`Seq`/`TS`（序号与时刻让一份日志分得清「这个 agent 没话说」与「记录丢了」，线格式 v2 起）），工具批的并行有上限（`MaxParallelActs` 只收窄 `ParallelActs`）；`Prompt.Reflection` 与 `CycleOutcome` 语义已定齐（前者是宿主写在 `BeforeStimulate` 原型上的槽位、每轮 verbatim 透传给 Thinker，内核零反射逻辑；后者五臂终态 Done/Suspended/MaxRounds/Error/Aborted，首次标记优先，经 `OnCycleEnd` 交付）；③ 公开面收敛（八钩子必填 + `OnCycleEnd` 终态 + `Organs.ID` 必填无默认（事件署名与 `Session` 归属都按它判定）+ 蓝图独占可换槽名 `WirePoint.Slot` + 预算端口覆盖两条累积轨 + 记忆端口 P7 + 出口裁决归端口不归钩子 P5c + 可选的框架调用时点用能力断言（`Bootable` + 派生的 `PortOrder()`）而不是新端口 + 凡宿主要按身份判定的哨兵错误一律在 api 可见（`internal/` 不可 import，文档承诺的 `errors.Is` 才有落点），新增一律先问能不能并入现有端口；不能并入且缺的是「框架调用时点」的，升为必填端口，并同批交付 槽名 + Validate 分支 + 同步守卫 三件。
- **明确不做**：不破零三方依赖；**不内置任何器官实现，也不为了让集成「开箱即用」而把某个端口改成可选**——曾内置过 stdlib 手写的 openai Thinker（v1.3.6 收编）又于 v1.3.8 净删除，理由是「一份可 import 的实现带来的不是示范，是『应当用它』的暗示」；引第三方 LLM SDK（如 openai-go/v3）比那次收编代价更高（发布节奏挂到别人的 release 上）且同样落在这个裁定之外；降装配门槛只走 `FullHooks` 那条既有路子（宿主显式调用的 helper，蓝图仍全必填）；一个端口变可选 ⟺ 必填集合变小 ⟺ `PortOrder()` 派生、`hostOrgans` 启动表、slots 同步守卫、`wiring_free_test` 字段集合四件同改，且 `Required:false` 会让该端口的 `Boot` 时点被 `PortOrder()` 静默丢掉；**不做 agent 间通信**——寻址、路由、投递、能力发现、突触权重与学习规则全在宿主，一个 `Agent` 就是一个内核，多 agent 是宿主 `New` 多个实例（子 agent 由宿主的 spawn 工具经 `New`+`Stimulate` 产生，`Organs.ID` 必填且各实例不得重名，因为事件署名与 `Session` 归属都按它判定），`Organs` 上不允许出现任何面向邻居的槽（`test/wiring_free_test.go` 钉住）；声明记忆端口的契约与调用时点（`Recall`/`Remember` + `CycleFacts` 事实回传），但存储、索引、检索算法、遗忘策略一律不做，实现仍归 MemHop/宿主；不做宿主工具（spawn 等归 meowagent）；不为宿主兼容调整自身端口设计。

## 跨仓边界（只负责自身）

- 本仓一切改动**只以自身语义正确性为准**：设计端口、事件流与钩子契约时不考虑宿主 meowagent 的现有实现，更不改它的文件
- 契约变更的唯一生效入口是**打 tag**——meowagent 经 `go.mod` require 远端版本消费本仓，跟版与适配是 meowagent 侧独立轮次的工作
- 父目录 `go.work` 把三仓源码在本地串起来，只用于「改完跑一次下游 `go build` 取证」；取证不等于动手，`go.work` 自身也不在本仓轮次内修改
- 跨仓写入由 `~/.qoder-cn/hooks/block-cross-repo.sh`（PreToolUse）机械拦截，被拒即停手收口，不主动为宿主评估影响
