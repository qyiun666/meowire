# 决策档案: 官方大脑内置 —— 宿主传参，Thinker 端口净删除

Status: implemented

## Problem

第三次提出同一件事（v1.3.7 交付过 stdlib 版、v1.3.8 净删、09-18 又否决过「内置+去端口」组合）：宿主接入 meowwire 必须自写一个 LLM 适配器官，把内核的 Prompt 契约渲染成某个服务商的协议、再折回来。这一次用户给出的形状与此前两次都不同——**宿主对大脑无感**：New 时传 model/key/流式参数，每次对话带文本，内核内部自理；并且不再为任何既有宿主保留兼容路径。

## Decision

大脑成为框架内置器官：`internal/brain` 是全仓唯一 import openai-go 的包（版本锚 v3.61.0；wire 由模式枚举选择——chat completions 为默认，Responses 为第二根，系同日所有者追加裁定，两根 wire 渲染同一份无状态 Prompt、折回同一个 Decision；Responses 侧 Store 关闭：内核每轮全量重建、从不回读服务端会话状态），由 api 组合根从 `Organs.Brain` 构造、以 Thinker 身份注入循环；`internal/nerve` 保持零 provider 词汇。公开面净删除：`Thinker` 类型别名、`Organs.Think`、`SlotThink`、`Replace("think")`、蓝图 P1、`FallbackThinker`（组合多个大脑已无对象）。蓝图新增 F2（框架内置 Brain，phase 1，非宿主槽、不可换）。流式增量走 `WithSink(ctx)`（在出口膜裁决之前，宿主收 EventText 时整段替换已推内容）；事件流不加 token 增量。重试单层：SDK `WithMaxRetries(0)`，预算归内核 `Config.MaxRetries`。渲染无状态：Prompt 是一轮的全部真相，assistant/tool 配对从 ToolResults 单批重建。

## Alternatives considered

- **伴生子模块 brain/（宿主 import 一个额外包）** —— 落选，用户裁定宿主必须无感：多一次 import 就是多知道一分大脑的存在。
- **保留 Think 双轨（自写器官与参数二选一）** —— 落选，用户明言不兼容旧版本、不围绕既有宿主设计；双轨是兼容层，不是形状。
- **维持现状（器官全归宿主）** —— 落选，用户裁定推翻；三次否决的理由（卖点、端口收敛压力、发布节奏）记录在案但被所有者放弃。
- **端口收紧为配置但保留 Thinker 接口作逃生口** —— 落选，与双轨同病：接口还在就是双轨还在。
- **事件流加 token 级增量** —— 落选（维持 09-18 裁定）：出口膜的裁决单位是一整轮文本，流式化重定义裁决粒度；Sink 通道已覆盖流式 UX。
- **渲染带状态（自记模型轮次重建对话）** —— 落选：Prompt 契约本就不回传模型往轮文本，内核之外再造一份循环看不见的对话状态，恰好是 09-17 删除内置实现时点名的归属混淆；无状态渲染协议合法且锁数量减一。

## Consequences

- 根模块零三方依赖终结：go.mod 恒带 openai-go（+tidwall 四件套间接依赖），每个宿主的二进制都链接它，即使永远只用参数路径。
- 仓库背上永久性协议跟踪义务：openai-go ~3 天一个 minor、main 已新增 Azure/AWS/WebSocket 依赖；版本锚 v3.61.0，升版是主动决策不是自动跟随。
- 公开面 Breaking（删除 Thinker/Organs.Think/SlotThink/Replace think 槽/P1/FallbackThinker）；蓝图六宿主端口 + Brain 参数 + F2 内置点，wiring_free 字段集换一删一增仍 13 项。
- 09-17 档案整体被取代（本档案）；09-18 档案的「内置 LLM 客户端维持否决」一条被本档案推翻，其余裁定（身份、事件流形状、流式粒度、消息轨归属）全部维持。
- 「宿主只实现器官即可装配」判据照旧成立，但器官清单里少了大脑：接一个模型从「写 500 行适配器」变成「填四个参数」。
- 组合器家族缩小：FallbackThinker 净删除，GuardStack/FallbackEffector 维持；think 槽从 Replace 的可换集合消失——换模型是重新 New，不是运行期换件。
