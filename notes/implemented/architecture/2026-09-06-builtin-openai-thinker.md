# 决策档案: meowire 内置参考 LLM Thinker（openai 包，宽缝收编）

Status: implemented

`openai/` 包随本档案同任务落地，渲染文案与传输一并收编进内核。

## Problem

宿主集成 meowire 必须自己实现 Thinker 端口，其中约八成代码是两件与宿主个体无关的事：渲染 meowire 自有框架格式（ToolResults/State/Reflection/sandbox 拒绝行都是框架定义并填充的）与 OpenAI 协议镜像（SSE 解析、流式 tool call 累积、双 wire、重试）。每个宿主复制一遍框架自己的契约，且协议细节（done 覆盖幂等、缺 index 退化、usage 空 choices）极易写错。

## Decision

新增顶层包 `openai/`（`github.com/qyiun666/meowire/openai`）内置参考 Thinker：`New(Config, opts) → 实现 api.Thinker`，宿主填 Prompt 即用。

- **宽缝**：渲染随 Thinker 收编——渲染是框架实现自身 prompt 契约（措辞影响循环质量，循环质量本就是本仓判定标准），不是宿主策略。宿主真正独有的只剩 UI 推流/双模型/采样装配 glue
- **零三方依赖红线不破**：stdlib 手写 HTTP 重试与 SSE 解析，不引 go-openai/retryablehttp
- **端口不收敛**：`api.Thinker` 契约零改动，自定义提示词/传输的宿主仍自行实现
- **wire 不编号**：chat completions 与 responses API 同属 OpenAI 系，一个包双 wire（`WireFromURL` 自动探测）；anthropic 有消费再平级加包
- **配套上移**：sandbox 拒绝反馈文本由 nerve 产出侧直接给最终形式 `[sandbox-denied: reason]`（原 `[denied: reason]`），渲染端零模式匹配；`[denied:` 前缀保留唯一身份 = Resume 应答协议输入编码（拒绝臂，含 `[denied: timeout]` 配方）——输入协议与输出反馈自此分家

## Alternatives considered

- **窄缝（只收传输，渲染留宿主）**——初版方案。细读渲染代码后否决：约七成"渲染文案"实为框架自有格式的实现（decorateToolFeedback 装饰的就是框架自己产出的格式），留宿主=继续让宿主复制框架契约；且 prompt 措辞直接影响循环质量，归内核所有更自洽
- **独立 Go module（meowire-llm）**——为数百行开一个仓库太重；适配器纯 stdlib 无依赖需隔离，同仓子包即可
- **引入 go-openai 到内核**——破零三方依赖红线（meowire 唯一明确的"明确不做"之一）；且其类型面远宽于循环所需，内核被迫携带无用表面
- **维持现状（等第二个宿主出现再收）**——被用户拍板否决：宿主复制的痛苦是现在时的，且渲染/协议的"规格"由框架与 OpenAI 各自定死，n=1 宿主不会污染其形状
- **核心事件流增加 token delta 事件（EventTextDelta）**——落选：传输层流式不是循环语义，ChunkSink（观测面回调）已覆盖宿主 UI 需求；往事件流塞 delta 会改事件契约且让内核关心传输节奏

## Consequences

- 收编后宿主侧 thinker 可缩编为装配 glue（宿主轮次的事，本仓不评估），生态里同一份传输/渲染知识只剩内核一份
- 渲染措辞成为本仓承诺：改动渲染文案 = 改循环行为，按「更准还是更糊」评审；测试以手写 JSON 字面量为独立基准，协议形状回归被钉死
- `[sandbox-denied:` 是行为变更：宿主 UI 若直接显示 Effect.Err/Context 文本，字样随 tag 更新变化——契约变更经打 tag 生效，宿主跟版自理
- 传输语义改进：流式请求不再被整体 body 超时截断（只限时到响应头），与非流式的 120s 语义有意分叉
- 新公开面（包级 API）进入收敛纪律：`openai/surface_test.go` 钉面，后续增删须有意识落地
