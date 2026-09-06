# agent.md — openai

meowire 内置参考 Thinker（LLM 大脑的参考实现）：零三方依赖的 OpenAI 兼容客户端，双 wire（Chat Completions + Responses API）。评价标准与内核一致：它让端口契约更好装配（宿主填 Prompt 即用），而不是替代端口。

## 职责（文件分工）

- `openai.go`：Config/Sampling/Option/New/Think 分发；`WithStreamGate`（每轮 Think 判定流式）+ `WithChunkSink`（ChunkText/ChunkReasoning；sink 阻塞即背压，ctx 短路）+ `WithNoTools`（纯对话，剥工具定义）
- `wire.go`：Wire 枚举 + `WireFromURL`（协议形态判定唯一实现，deepseek 走 responses）+ Chunk 类型
- `transport.go`：stdlib 手写 HTTP 传输 + 重试（429/5xx/网络瞬时，120s 超时，3 次退避 1s~8s，Retry-After 优先）+ SSE 行解码；流式客户端只设 ResponseHeaderTimeout（整体 body 超时会截断长流）
- `render.go`：Prompt → 两 wire 请求（渲染 = 框架自身 prompt 契约的规范呈现；槽位文案、`[tool-result name]` 行格式、采样零值不下发）；sandbox 拒绝行原样透传（最终形式由 nerve 产出侧保证）
- `chat.go` / `responses.go` / `accum.go`：两 wire 的 once/stream 路径与流式 tool call 累积状态机

## 关键决策

- 宽缝收编（2026-09-06）：渲染文案随内置 Thinker 进入本包——渲染是框架实现自身契约，不是宿主策略（决策档案 `notes/implemented/architecture/2026-09-06-builtin-openai-thinker.md`）
- `[denied:` 前缀唯一身份 = Resume 应答协议输入编码；输出反馈文本为 `[sandbox-denied: ...]`（nerve 产出侧规范化），本包零模式匹配
- 零三方依赖红线：stdlib 手写 SSE/重试，不引 go-openai/retryablehttp；重试只盖连接建立（流建立后 recv 错误不重试）
- wire 不编号：chat + responses 同属 OpenAI 系一个包；anthropic 有消费再平级加包
- 传输流式不进核心事件流（无 EventTextDelta）——ChunkSink 是观测面，完整文本恒走 Decision

## 陷阱

- 测试 wire 载荷全部手写 JSON 字面量（独立基准）——改本包结构体 json 标签前先想到这里
- usage 全零 → nil（跳过计费事件）是框架语义，两 wire 一致；chat usage chunk 的 choices 为空数组，解析必须容忍
- responses 流式 `output_item.done` 携带完整 args，覆盖已累积增量（幂等）；缺 `output_index` 退化为单槽 0
- 空白 System 不渲染（真实网关对缺 content 的空消息回 400）；空 ToolResult 也渲染（空输出是事实反馈）
- 流式 + Think 重试：nerve 的 thinkWithRetry 整体重试 Think——流中断前已推 sink 的增量会在重试轮重放（UI 观测面重复，Decision 数据路径不受影响；传输层重试只盖连接建立，不重试流中断）
