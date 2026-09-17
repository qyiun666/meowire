# 适配文档：把 `Thinker` 接到 `github.com/openai/openai-go/v3`

本文只解决一个问题：meowire 的 `Thinker` 端口（`Think(ctx, *Prompt) (*Decision, error)`）
对着一个真实的 LLM SDK 要怎么写。meowire 不携带任何器官实现（见
`notes/implemented/architecture/2026-09-17-kernel-ships-no-organs.md`），所以这里写的是
**宿主侧的代码**，本文所在仓库不会 import 这个 SDK。

## 0. 取证基准

本文所有 SDK 签名按本机 module cache 里的
`github.com/openai/openai-go/v3@v3.61.0` 逐条核对，标注的坐标相对该 module 根目录。

版本差异会咬人，先记三条：

- `openai.F(...)`、`openai.O[T]`、`param.Field[T]` 在 v3.61 已移除
  （`MIGRATION.md:7`、`:76`）。可选值改用 `param.Opt[T]`，构造用
  `openai.String/Int/Bool/Float`（`field.go:9-12`）；必填字段是裸值。
- 流式类型在 `packages/ssestream`，不叫 `packages/sse`。
- 累加器是零值可用的 `openai.ChatCompletionAccumulator`（`streamaccumulator.go:20`），
  方法叫 `AddChunk`，没有 `accumulator` 子包。

对照自己的版本：`go doc github.com/openai/openai-go/v3.ChatCompletionNewParams`。

本文出现的每段 Go 代码都在**仓库外**的临时模块里编译过：`go build` / `go vet` 对着
`openai-go/v3@v3.61.0` 与本仓当前 HEAD 通过（离线，proxy 指向本地 module cache）。
本仓自身依然零三方依赖——这些代码属于宿主。

## 1. 两端形状

```go
// meowire
type Thinker interface {
    Think(ctx context.Context, p *Prompt) (*Decision, error)
}
type Decision struct {
    Text      string
    ToolCalls []ToolCall   // {ID, Name, Args string}
    Usage     *Usage       // {Prompt, Completion, Total int}；nil = 不发 EventUsage
}
```

```go
// openai-go/v3
func (r *ChatCompletionService) New(ctx context.Context, body ChatCompletionNewParams,
    opts ...option.RequestOption) (res *ChatCompletion, err error)          // chatcompletion.go:71
func (r *ChatCompletionService) NewStreaming(ctx context.Context, body ChatCompletionNewParams,
    opts ...option.RequestOption) (stream *ssestream.Stream[ChatCompletionChunk])  // :99
```

一次 `Think` = 一次 chat 请求。内核不假设你流式还是不流式，也不要求 `Think` 幂等——
但**它可能把同一个 `*Prompt` 再交给你几次**（见 §6）。

## 2. `Prompt` 全字段 → 请求的落点

`Prompt` 是进脑的唯一数据包（`internal/nerve/port.go:24-60`）。13 个字段都有落点，
**漏一个字段就是一个器官静默失能**——下表是完整清单。

| `Prompt` 字段 | 类型 | chat 请求落点 |
|---|---|---|
| `System` | `string` | 首条 system/developer 消息的开头 |
| `Identity` | `string` | 同上，紧跟 `System` |
| `Methods` | `[]MethodSpec{Name, Desc, Input, Output}` | 同一消息里的能力清单文本（描述性，**不是**可调用工具，别塞进 `Tools`） |
| `Bounds` | `string` | 同一消息里的执行边界段（`Sandbox.Bounds()` 快照） |
| `Plan` | `string` | 同一消息里的计划段，或 assistant 预填充 |
| `Context` | `[]string` | 同一消息里的背景段（宿主基线 + 框架追加的 `[sandbox-denied: ...]`，工具结果**不在**这里） |
| `Reflection` | `string` | 同一消息里的自查段（上一轮复盘；空 = 无） |
| `Memories` | `[]Record{Key, Kind, Content []byte, ...}` | 检索段文本；如何编排（放 system 还是 user 前置）归宿主 |
| `Input` | `string` | `openai.UserMessage(p.Input)` |
| `Stimuli` | `[]Signal` | 邻居来信：渲染成文本段（角色由宿主决定），或按 `Kind` 折进背景段 |
| `Inhibit` | `[]string` | 本轮被撤回的工具名：**同时**从 `Tools` 里剔除并在消息里说明为什么不可用（只剔不说是让模型猜） |
| `Tools` | `[]ToolSpec{Name, Desc, Input, Output}` | `params.Tools`，见 §5 |
| `ToolResults` | `[]ToolResult{ID, Name, Result, Err}` | `assistant(tool_calls)` + `tool` 成对消息，见 §3 |

固定段与动态段分开拼，是这份表最重要的用法：内核每轮只换动态段，
把固定段做成可缓存的前缀，才吃得到服务商的前缀缓存。

## 3. 承重约束：`tool` 消息必须挂在 `assistant(tool_calls)` 之后

OpenAI 的消息序列要求每个 `role:"tool"` 消息前面有一条内容匹配的
`role:"assistant"` + `tool_calls`。而内核交给你的只有
`ToolResult{ID, Name, Result, Err}`——**它不替你保留模型上一轮说过什么、参数是什么**。

所以适配器必须自己记下模型发出的调用。内核给出的口子足够：`ToolResult.ID` 回声的正是
`ToolCall.ID`（模型给的 `call_xxx`），而你在 `Decision.ToolCalls` 里本来就有完整参数。

```go
type chatThinker struct {
    client  openai.Client
    model   openai.ChatModel
    tools   []openai.ChatCompletionToolUnionParam
    issued  map[string]meowire.ToolCall // call id -> 模型当时的调用
}
```

`Think` 返回 `Decision` 时写入 `issued`，下一次 `Think` 按 `p.ToolResults` 的 ID 取出配对。
这不是"接线缺口"，是器官的内部状态：内核给的是**语义**（哪个调用 resulted in 什么），
角色编排是宿主的责任（`ToolResult` 的文档注释就写着"渲染归宿主 Thinker 决定"）。

两个边界情况要有明确处理，否则会发出非法请求：
- `issued[ID]` 查不到（宿主重启、`Session` 从旧进程恢复）→ 退化路径：把这条结果作为文本
  并入背景段，不要发孤立的 `tool` 消息。
- 一轮多个调用 → 一条 assistant 消息带多个 `tool_calls`，随后逐个 `tool` 消息，顺序与
  `p.ToolResults` 一致（内核保证它按调用序累积）。

## 4. `Decision` 回流

```go
choice := complete.Choices[0]                                  // chatcompletion.go:272 起
dec := &meowire.Decision{Text: choice.Message.Content}
for _, tc := range choice.Message.ToolCalls {                  // :3038，union 是扁平字段
    dec.ToolCalls = append(dec.ToolCalls, meowire.ToolCall{
        ID:   tc.ID,
        Name: tc.Function.Name,
        Args: tc.Function.Arguments,   // 全程是 JSON 文本，原样交给 Effector
    })
    t.issued[tc.ID] = dec.ToolCalls[len(dec.ToolCalls)-1]
}
```

`FinishReason` 在本版本是裸 `string`（`"stop" | "length" | "tool_calls" | ...`，
`chatcompletion.go:271`），别指望枚举常量。`Usage` 三字段是 `int64`
（`completion.go:173-179`），落成 `meowire.Usage` 的 `int` 时自己转；
拿到零值 `Usage` 时返回 `nil`，事件流就不会发一条假的 `EventUsage`。

`Args` 不做 JSON 校验：那是 Effector 与 `Sandbox` 的事，Thinker 改了会把模型的意图变成宿主的意图。

## 5. 工具定义映射

```go
// ToolSpec.Input 是 JSON Schema 文本，SDK 要的是 map
var schema openai.FunctionParameters                     // = shared.FunctionParameters（aliases.go:492）
if spec.Input != "" {
    if err := json.Unmarshal([]byte(spec.Input), &schema); err != nil {
        return nil, fmt.Errorf("tool %s schema: %w", spec.Name, err)
    }
}
tools = append(tools, openai.ChatCompletionToolUnionParam{
    OfFunction: &openai.ChatCompletionFunctionToolParam{
        Function: openai.FunctionDefinitionParam{          // shared/shared.go:877
            Name:        spec.Name,
            Description: openai.String(spec.Desc),
            Parameters:  schema,
        },
    },
})
```

三处形状落差要认：

- `FunctionDefinitionParam.Name` 的约束（`a-zA-Z0-9_-`、长度 ≤64，`shared.go:878-879`）
  只在注释里，**客户端不校验**，服务端拒。`ToolSpec.Name` 若来自不可信来源，宿主自己过滤。
- `Strict` 存在（`shared.go:886`，`param.Opt[bool]`），但 `ToolSpec` 没有对应字段：
  要严格模式就在宿主侧按工具名维护一张表，别去改内核的 `ToolSpec`。
- `ToolSpec.Output` 无处可放——chat completions 不接受输出 schema。渲染进描述文本或不发，
  由宿主定；它仍是给模型看的一段说明。

## 6. 错误、取消与重试：两层不要相乘

传输侧：SDK 默认重试 **2 次**（`internal/requestconfig/requestconfig.go:282`），
退避 `0.5s * 2^n`、上限 8s，尊重 `Retry-After`（`:421`），并按 `x-should-retry` 与状态码
（408/409/429/5xx、连接错误）判定；`shouldRetry` 未导出，想自己接管退避就
`option.WithMaxRetries(0)`（`option/requestoption.go:114`）。

内核侧：`thinkWithRetry`（`internal/nerve/retry.go:41-61`）在 `Config.MaxRetries` 内
**无退避地**把同一个 `*Prompt` 再交给 `Think`；`ctx` 取消立刻返回；返回
`(nil, nil)` 会被框架当成错误。它不分类错误——401 和 429 会被一样重投。

所以：

```go
client := openai.NewClient(option.WithMaxRetries(0))   // 传输重试只留一层
```
并把 `Config.MaxRetries` 当作**唯一**的重试策略（它会立即重投，所以幂等性由模型请求本身保证）。

错误形状只有一个：非 2xx 是 `*openai.Error`（= `apierror.Error` 别名，`aliases.go:17`），
字段 `StatusCode/Code/Message/Type`，方法是指针接收者，所以
`var apiErr *openai.Error; errors.As(err, &apiErr)`。网络错误**不被包装**，原样透出
（`apierror.go:12-14`）——别去 `errors.Is` 一个 SDK sentinel。`*openai.Error` 没有 `Unwrap`。

永久错误（400/401/403 配额与参数类）要在 Thinker 内**快速失败并把 `StatusCode` 拼进错误文本**：
内核会重投，但重投同一请求不会成功，唯一能少花的是尽早让这一轮以 `EventError` 结束
（`MaxRetries=0` 时即第一次就结束）。

`ctx` 必须透传给 SDK 调用（`New(ctx, ...)`），并且**不要**在 `Think` 里用
`context.WithoutCancel`：`Close` 与宿主取消正是靠这条链落地的。

## 7. 流式

`params` 里没有 `Stream` 字段，流式与否由调用哪个方法决定（`chatcompletion.go:106` 内部注入）。
要拿 usage 必须显式开：

```go
params := openai.ChatCompletionNewParams{
    Messages: msgs, Model: t.model, Tools: t.tools,
    StreamOptions: openai.ChatCompletionStreamOptionsParam{
        IncludeUsage: openai.Bool(true),                   // :3381
    },
}
stream := t.client.Chat.Completions.NewStreaming(ctx, params)
acc := openai.ChatCompletionAccumulator{}
for stream.Next() {
    chunk := stream.Current()
    if !acc.AddChunk(chunk) {                              // streamaccumulator.go:140
        return nil, errors.New("openai thinker: chunk not incorporated")
    }
    // 只在这里做 UI 推流，见下面的膜约束
}
if err := stream.Err(); err != nil { return nil, fmt.Errorf("openai thinker: %w", err) }
// acc 内嵌 ChatCompletion：acc.Choices / acc.Usage 直接用
```

工具调用的分片按 `chunk.Choices[0].Delta.ToolCalls[i].Index`（`int64`，`:1173`）归并，
`Function.Arguments` 是 JSON 文本增量——这正是 SDK 的累加器替你做的事，不要自己拼
（`JustFinishedContent()` / `JustFinishedToolCall()` 只在对应段完成时点亮一次，适合驱动 UI）。

**出口膜的时序**（这条最容易踩）：内核在整轮文本生成之后才让它过 `Sandbox.Emit`
（`internal/nerve/gate.go`），被拒时事件流里出现的是 `[sandbox-denied: ...]`，
被征询（Ask）时草稿被扣进 `Session`。也就是说：你把 token 直接推给用户，等于在裁决之前
先把话说了。三种诚实做法，选一种并写进宿主文档：

1. **缓冲**：Thinker 内只累加不推流，`Decision` 返回后由宿主按 `EventText` 上屏（最简单，
   与膜契约天然一致）；
2. **推流 + 更正**：边推边上屏，`EventText` 到达时若与已推内容不同就整段替换（膜拒绝时
   替换成 `[sandbox-denied: ...]`）；
3. **推流但只推已裁决段**：本轮不推，等下一轮——实际等价做法 1。

UI 推流通道属于 Thinker 的内部事，**不要**把它伪装成事件流的一部分：事件流只有一种文本
事件（`EventText`，整段、已过膜），这是契约而不是缺陷。

## 8. `Bootable`：客户端什么时候建

`openai.NewClient(opts ...option.RequestOption)` 不做网络握手（`client.go:111`），所以
**没有必须提前打开的东西就别声明 `Boot`**。要校验密钥、预热模型目录、准备连接池时才声明：

```go
func (t *chatThinker) Boot(ctx context.Context) error {
    if os.Getenv("OPENAI_API_KEY") == "" {
        return errors.New("openai thinker: OPENAI_API_KEY is empty")
    }
    t.client = openai.NewClient(option.WithMaxRetries(0))
    return nil
}
```

框架在 `New` 里按 `PortOrder()` 逐个启动、失败即中止并经宿主 `Closer` 释放
（`api/assemble.go`），`Replace` 换入时同样先启动再提交。**一次装配只启动一次**，
所以这个 Thinker 实例属于一个 agent，不跨 `New` 复用（`internal/nerve/lifecycle.go`）。

## 9. 参考实现（非流式，可直接改）

```go
type chatThinker struct {
    client  openai.Client
    model   openai.ChatModel
    tools   []openai.ChatCompletionToolUnionParam
    issued  map[string]meowire.ToolCall
}

func (t *chatThinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
    msgs := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(renderFixed(p))}

    for _, tr := range p.ToolResults {
        call, ok := t.issued[tr.ID]
        if !ok {                                   // 无配对的退路：并进文本轨
            msgs = append(msgs, openai.UserMessage("tool "+tr.Name+" result: "+firstNonEmpty(tr.Result, tr.Err)))
            continue
        }
        msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
            OfAssistant: &openai.ChatCompletionAssistantMessageParam{
                ToolCalls: []openai.ChatCompletionMessageToolCallUnionParam{{
                    OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
                        ID:   call.ID,
                        Type: "function",
                        Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
                            Name:      call.Name,
                            Arguments: call.Args,
                        },
                    },
                }},
            },
        })
        msgs = append(msgs, openai.ToolMessage(firstNonEmpty(tr.Err, tr.Result), tr.ID))
    }
    msgs = append(msgs, openai.UserMessage(p.Input))

    complete, err := t.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
        Messages: msgs, Model: t.model, Tools: t.tools,
    })
    if err != nil {
        return nil, fmt.Errorf("openai thinker: %w", err)   // 内核按 MaxRetries 重投同一 Prompt
    }
    if len(complete.Choices) == 0 {
        return nil, errors.New("openai thinker: completion has no choices")
    }
    return t.decisionOf(complete), nil
}
```

`renderFixed` 拼 §2 表里全部标"同一消息"的段，`decisionOf` 就是 §4 的代码。
把 `Tools` 的构造放在宿主启动期而不是每轮 `Think` 里：`p.Tools` 是固定的
（`Connectome` 把 `Tools` 归在构造期注入的固定段），重算只做浪费。

## 10. 交付前自检

- [ ] `Prompt` 13 个字段各有落点，特别是 `Memories` / `Stimuli` / `Reflection` / `Inhibit`
- [ ] 每个 `tool` 消息前有一条 `assistant(tool_calls)`；查不到配对时走文本轨
- [ ] 只有一层重试（`option.WithMaxRetries(0)` + `Config.MaxRetries`，或反之）
- [ ] `ctx` 一路透传，`Think` 内没有 `WithoutCancel`
- [ ] `Usage` 为零值时返回 `nil`，不发假 `EventUsage`
- [ ] 流式推流策略与 `Sandbox.Emit` 的关系被写明（§7 三选一）
- [ ] 声明了 `Bootable` 的话，该实例只用于一个 agent
- [ ] 本仓库仍然零三方依赖：这些代码在宿主仓，不在 meowire

---

跨仓提醒：meowagent 通过 `go.mod` require 本仓的**已打 tag 版本**，本文写的适配形状
在打 tag 之后才对下游生效（`AGENTS.md` 的跨仓边界一节）。
