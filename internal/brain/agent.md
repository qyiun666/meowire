# brain — 内置大脑（openai-go）

职责：把 `nerve.Prompt` 变成一次 LLM 调用（chat completions 或 Responses wire，`Mode` 枚举选择），把响应折回 `nerve.Decision`。全仓唯一 import openai-go 的包——provider 词汇到此为止（`internal/nerve` 零 SDK 知识，本器官由 api 组合根构造并注入 Think 槽）。

## 关键决定

- **双 wire，一枚枚举**：`Mode`（1=chat 默认、零值同 1；2=responses）在 `Think` 入口分路，chat 路径零改动。两根 wire 渲染同一份无状态 Prompt、折回同一个 Decision——模式是传输选择，不是契约分叉。unknown mode 由 api 层 Validate 拒绝，本包不重复校验。
- **responses 侧不碰服务端状态**：`Store:false`、从不发 `previous_response_id`——内核每轮全量重建，服务端留存只会堆没人读的副本。`Status=="failed"` 折回为错误（带 Code 早死），`incomplete` 放行部分文本（对称 chat 侧不拦 length 截断）。流式没有累加器也不需要：终态事件内嵌整只 `Response`，`output_text.delta` 只用来推 Sink。
- **无状态**：Prompt 是一轮的全部真相；assistant/tool 配对从 ToolResults 单批重建（一条 assistant 携带全部历史调用 + 每个结果一条 tool 回话，协议合法）。模型自己的往轮文本不在 Prompt 里，也不出现在消息里；跨轮连续性归 Mem 端口。被膜拒掉的调用天然缺席（拒绝以 `[sandbox-denied: ...]` 走 Context 轨），无需占位结果。responses 侧同语义：每个 result 一对 function_call + function_call_output。
- **消息顺序**：system、user(Input)、配对段。Input 跨轮不变，前缀稳定，服务商前缀缓存吃得到；固定段在 system 前半、动态段在后，同理（responses 侧 system bundle 落 `Instructions` 顶层位）。
- **重试单层**：client `WithMaxRetries(0)`，预算归内核 `Config.MaxRetries`。两层相乘会把一次故障放大成 MaxRetries × SDK 次请求。
- **流式走 Sink**：`WithSink(ctx, …)` 挂在调用 context 上；delta 在出口膜（Sandbox.Emit）裁决之前推出，宿主收到 EventText 时整段替换已推内容。事件流不加 token 增量（09-18 裁定维持）。
- **Tools 每轮现读** `p.Tools`：BeforeStimulate 可整体改写，缓存到构造期会静默丢掉改写。本轮无工具时返回 nil（线格式整个省略 `tools` 字段），不发空数组——部分 OpenAI 兼容端点会拒它。
- **错误形状**：非 2xx 是 `*openai.Error`（无 Unwrap），`errors.As` 经 `%w` 可达；status 拼进错误文本让永久错误（400/401/403）早死，别烧重试预算。网络错误 SDK 原样透出。

## 陷阱

- `BaseURL` 空 = 官方端点（不传该 option）；`Key`/`Model` 必填由 api 层 Validate 拦截，本包不重复校验。
- `Usage` 全零返回 nil（不发假 EventUsage）；chat 流式必须显式 `IncludeUsage`，否则 wire 不带账。
- easy input message 上线不带 type 判别（role+content 即消息）；读 `Response.Usage` 时字段名是 input_tokens/output_tokens/total_tokens，别按 chat 的字段名猜。
- Args 原样透传（JSON 文本），校验归 Effector 与 Sandbox——大脑改写它就把模型意图变成了宿主意图。
- SDK 版本锚 `v3.61.0`；升版前先跑 thinker-openai-go.md §0 的差异核对（`param.Opt`、SSE 包名、累加器方法名这些坑）。

## 规格出处

`thinker-openai-go.md`：Prompt 11 字段落点表、配对承重约束、两层重试、SSE 与出口膜的时序；§10 是 Responses wire 的差异面（落点表、Store:false、终态事件折回）。
