# 内置大脑规格：meowire × `github.com/openai/openai-go/v3`

本文只解决一个问题：meowire 的进脑数据包（`Prompt`）对着真实的 openai-go 要怎么落、
出脑的裁决（`Decision`）要怎么折。**内置大脑（`internal/brain`）已按本文实现**——仓库
唯一 import 这个 SDK 的包，版本锚 v3.61.0；宿主不再写 Thinker，本文件是它的实现规格
与协议参考。大脑有两根 wire（chat completions 为默认，Responses API 由 `Mode` 枚举
选择）；§1–§9 以 chat completions 为基准，§10 是 Responses wire 的差异面，其余
契约（无状态、重试、Sink、Usage）两根共用。

## 0. 取证基准

本文所有 SDK 签名按本机 module cache 里的
`github.com/openai/openai-go/v3@v3.61.0` 逐条核对，标注的坐标相对该 module 根目录。

版本差异会咬人，先记三条：

- `openai.F(...)` 与 `param.Field[T]` 在 v3.61 已移除（`MIGRATION.md:7`、`:76`）。
  可选值现在是 `param.Opt[T]`（零值 = 省略，靠 Go 1.24 的 `json:",omitzero"`），
  构造用 `openai.String/Int/Bool/Float`（`field.go:9-12`）或通用的 `openai.Opt[T]`（`:15`）；
  必填字段是裸值。
- 流式类型在 `packages/ssestream`，不叫 `packages/sse`。
- 累加器是零值可用的 `openai.ChatCompletionAccumulator`（`streamaccumulator.go:20`），
  方法叫 `AddChunk`，没有 `accumulator` 子包。

对照自己的版本：`go doc github.com/openai/openai-go/v3.ChatCompletionNewParams`。

规格中的坐标（`chatcompletion.go:71` 这类）相对 v3.61.0 的 module 根目录；升版前先按
§0 复核差异。`internal/brain` 的行为由 `internal/brain/brain_test.go`（离线假端点）钉住：
落点、配对、流式 Sink、错误形状，全部有断言。

## 1. 两端形状

内置大脑的文件分工（六枚生产文件，读实现时按这张表找）：`brain.go`（`Config`/`Mode`/
`Think` 分路 / `wrapErr` / `Sink`+`WithSink`）、`render.go`（`Prompt` → messages，固定段与
动态段分块）、`tools.go`（`toolParams`/`toolSchema`/`describeTool`，后两枚两根 wire 共用）、
`decision.go`（`decisionOf`）、`stream.go`（`streamCompletion`）、`responses.go`（Responses wire 的
`thinkResponses`/`responseInput`/`responseToolParams`/`streamResponse`/`responseDecisionOf`）。


```go
// 内核端口 internal/nerve/port.go:344 —— 不在公开面：宿主不实现 Thinker，
// 它由 api 的组合根用 Organs.Brain 参数构造（内置大脑是唯一实现）。
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

`Prompt` 是进脑的唯一数据包（`internal/nerve/port.go:26-53`）。11 个字段都有落点，
**漏一个字段就是一个器官静默失能**——下表是完整清单。

| `Prompt` 字段 | 类型 | chat 请求落点 |
|---|---|---|
| `System` | `string` | 首条 system/developer 消息的开头 |
| `Identity` | `string` | 同上，紧跟 `System` |
| `Methods` | `[]MethodSpec{Name, Desc, Input, Output}` | 同一消息里的能力清单文本（描述性，**不是**可调用工具，别塞进 `Tools`） |
| `Bounds` | `string` | 同一消息里的执行边界段（`Sandbox.Bounds()` 快照） |
| `Plan` | `string` | 同一消息里的计划段，或 assistant 预填充 |
| `Context` | `[]string` | 同一消息里的背景段：宿主基线 + 框架追加的裁决文本——`[sandbox-denied: 原因]`（`internal/nerve/gate.go:52` 的 `deniedText`）。**被膜拒掉的调用只落在这里**（`ToolResult` 的文档就写着"Sandbox denials are verdicts, not tool results"），因此它们没有配对，别送进 `tool` 消息轨 |
| `Reflection` | `string` | 同一消息里的自查段（宿主在 `BeforeStimulate` 写、跨轮同值，框架从不写；空 = 无） |
| `Memories` | `[]Record{Key, Kind, Content []byte, Created}` | 检索段文本；`Content` 是 `[]byte`，转字符串前自己决定解码；`Created` 是 Unix 毫秒。每轮整体替换、不进 `Session` 快照 |
| `Input` | `string` | `openai.UserMessage(p.Input)` |
| `Tools` | `[]ToolSpec{Name, Desc, Input, Output}` | `params.Tools`，`Output` 折进 description（chat 协议无输出 schema 位），见 §5。**这是每轮现读的数据**：`BeforeStimulate` 可以整体改写它（`internal/nerve/hooks.go:66`），缓存到启动期会静默丢掉改写 |
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
    client openai.Client
    model  openai.ChatModel

    mu    sync.Mutex      // Think 会被并发调用，见下
    turns []turn          // 本会话的模型轮次：文本 + 那一轮发出的 tool_calls
}
```

`Think` 返回 `Decision` 时把这一轮记进 `turns`，下一次 `Think` 用它重建
`assistant`/`tool` 段（§9 的 `transcript`）。这不是"接线缺口"，是器官的内部状态：
内核给的是**语义**（哪个调用 resulted in 什么），角色编排是器官的责任——内置大脑
选了无状态重建（`ToolResult` 的文档注释写着"渲染归大脑、不归内核"）。

三件事由适配器自己负责，内核不代管：

- **并发**：cell 没有运行锁（`internal/cell/cell.go:169-217` 只在 `wireMu` 下快照端口），
  同一 Agent 上并发跑两次 `Stimulate`/`Resume` 就会并发调用同一个 Thinker 实例，
  一个 Thinker 服务多个 Agent 时同理。所以缓存必须加锁——并发写 `map`/`slice` 是直接
  fatal，不是慢一点。（`Act` 只有在框架开了 `ParallelActs` 时才要求并发安全；
  `Think` 的并发来自宿主怎么用。）
- **初始化**：实例由构造器建（§9），零值 `chatThinker` 能跑但客户端是空的。
- **边界与长度**：这份缓存是"一次对话"的量级，宿主知道自己何时开新对话，
  由宿主触发重置（§9 的 `Reset`）；跨进程的 `Session` 恢复只带回内核那半份状态，
  要保真就得把 `turns` 一起持久化。

两个边界情况要有明确处理，否则会发出非法请求：

- **被膜拒掉的调用**：它不进 `ToolResults`（§2），但模型确实发出过那个 `tool_call`。
  assistant 消息里列出的每个 `tool_call_id` 都必须有对应的 `tool` 消息，缺一条整个请求
  就非法——所以要补一条占位结果，拒绝原因已经由 `[sandbox-denied: ...]` 在 `Context`
  里给过模型了。
- **一轮多个调用 / 跨轮累积**：一个 turn 一条 assistant 带多个 `tool_calls`，随后按同序
  各跟一条 `tool` 消息；`ToolResults` 在一次 `Stimulate` 内跨轮累积，所以分组按自己记的
  turn 走，不要按结果切片的长度猜。

## 4. `Decision` 回流

```go
func (t *chatThinker) decisionOf(c *openai.ChatCompletion) *meowire.Decision {
    ch := c.Choices[0]                        // Choices: chatcompletion.go:188
    dec := &meowire.Decision{Text: ch.Message.Content}

    t.mu.Lock()                                // 与 transcript 共享 turns
    t.turns = append(t.turns, turn{text: ch.Message.Content})
    mine := &t.turns[len(t.turns)-1]
    for _, tc := range ch.Message.ToolCalls {  // []ChatCompletionMessageToolCallUnion：union 是扁平字段
        call := meowire.ToolCall{
            ID:   tc.ID,
            Name: tc.Function.Name,
            Args: tc.Function.Arguments,       // 全程是 JSON 文本，原样交给 Effector
        }
        dec.ToolCalls = append(dec.ToolCalls, call)
        mine.calls = append(mine.calls, call)
    }
    t.mu.Unlock()

    if u := c.Usage; u.TotalTokens != 0 || u.PromptTokens != 0 || u.CompletionTokens != 0 {
        dec.Usage = &meowire.Usage{                                // Usage: chatcompletion.go:238
            Prompt: int(u.PromptTokens), Completion: int(u.CompletionTokens), Total: int(u.TotalTokens),
        }                                                          // 三字段是 int64：completion.go:173-179
    }
    return dec
}
```

记的是**整轮**（文本 + 该轮全部调用）而不是一张 call id 表：被膜拒掉的调用永远不会有
`ToolResult`，重建 assistant 消息时仍要出现它（§3 的第一个边界）。

`FinishReason` 在本版本是裸 `string`（`ChatCompletionChoice` 在 `chatcompletion.go:262`，
字段在 `:272`），别指望枚举常量。`Usage` 保持零值时返回 `nil`，事件流就不会发一条假的
`EventUsage`。反方向（宿主 → SDK）的请求侧 `Type` 字段是 `constant.Function`，
省略即按 `"function"` 序列化，不必手写。

`Args` 不做 JSON 校验：那是 Effector 与 `Sandbox` 的事，Thinker 改了会把模型的意图变成宿主的意图。

## 5. 工具定义映射

```go
// toolParams 每轮现读 p.Tools（§2 的改写警告）：本轮哪些工具可读由 p.Tools 本身表达。
// 无工具的轮返回 nil——wire 因此省略整个 tools 字段；空数组会被部分 OpenAI 兼容端点
// 直接拒绝。ToolSpec.Input 是 JSON Schema 文本，SDK 要的是 map（toolSchema 解析）。
func toolParams(p *nerve.Prompt) ([]openai.ChatCompletionToolUnionParam, error) {
    if len(p.Tools) == 0 {
        return nil, nil
    }
    tools := make([]openai.ChatCompletionToolUnionParam, 0, len(p.Tools))
    for _, spec := range p.Tools {
        schema, err := toolSchema(spec)                      // = shared.FunctionParameters（aliases.go:492）
        if err != nil {
            return nil, err
        }
        tools = append(tools, openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{ // shared/shared.go:877
            Name:        spec.Name,
            Description: openai.String(describeTool(spec)), // Output 无座位，折进描述
            Parameters:  schema,
        }))
    }
    return tools, nil
}
```

三处形状落差要认：

- `FunctionDefinitionParam.Name` 的约束（`a-zA-Z0-9_-`、长度 ≤64，`shared.go:878-879`）
  只在注释里，**客户端不校验**，服务端拒。`ToolSpec.Name` 若来自不可信来源，宿主自己过滤。
- `Strict` 存在（`shared.go:886`，`param.Opt[bool]`），但 `ToolSpec` 没有对应字段：
  要严格模式就在宿主侧按工具名维护一张表，别去改内核的 `ToolSpec`。
- `ToolSpec.Output` 无处可放——chat completions 不接受输出 schema；内置大脑把它折进描述文本
  （`describeTool`，responses 侧同一折法），它仍是给模型看的一段说明。

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
内置大脑正是这么做的：`internal/brain/brain.go` 建 client 时传 `option.WithMaxRetries(0)`，
  唯一预算由 `nerve` 侧的 `thinkWithRetry`（`internal/nerve/retry.go`）花掉——大脑自己不重试；
  两层相乘会把一次故障变成 `MaxRetries × SDK 重试` 次请求。
  非 2xx 的状态码被拼进错误文本（`wrapErr`），为的是让永久失败早死而不是烧预算。

错误形状只有一个：非 2xx 是 `*openai.Error`（= `apierror.Error` 别名，`aliases.go:17`），
字段 `StatusCode/Code/Message/Type`，方法是指针接收者，所以
`var apiErr *openai.Error; errors.As(err, &apiErr)`。网络错误**不被包装**，原样透出
（`internal/apierror/apierror.go:12-14`："Other errors are not wrapped by this SDK"）
——别去 `errors.Is` 一个 SDK sentinel。`*openai.Error` 没有 `Unwrap`。

重投**不是幂等的**：同一个 `*Prompt` 再交给你一次，就是再发一次真实请求、再花一次 token、
再可能得到一个不同的答案。所以永久性错误（400/401/403 与参数类）要在 Thinker 内
**快速失败并把 `StatusCode` 拼进错误文本**：唯一能少花的是尽早让这一轮以 `EventError`
结束（`MaxRetries=0` 时即第一次就结束）。瞬态错误（429/5xx/连接）才值得让内核重投。

`ctx` 必须透传给 SDK 调用（`New(ctx, ...)`），并且**不要**在 `Think` 里用
`context.WithoutCancel`：`Close` 与宿主取消正是靠这条链落地的。

## 7. 流式

`params` 里没有 `Stream` 字段，流式与否由调用哪个方法决定（`chatcompletion.go:106` 内部注入）。
要拿 usage 必须显式开：

```go
tools, err := toolParams(p)                                    // §5：每轮现读
if err != nil {
    return nil, err
}
params := openai.ChatCompletionNewParams{
    Messages: msgs, Model: t.model, Tools: tools,
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
// 累加器内嵌 ChatCompletion（streamaccumulator.go:20），§4 的决策回收直接复用：
return t.decisionOf(&acc.ChatCompletion), nil
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
**没有必须提前打开的东西就别声明 `Boot`**：客户端交给 §9 的构造器，`Boot` 只承载真正的
fail-fast（校验密钥、预热模型目录、准备连接池）。

```go
func (t *chatThinker) Boot(ctx context.Context) error {
    if os.Getenv("OPENAI_API_KEY") == "" {
        return errors.New("openai thinker: OPENAI_API_KEY is empty")
    }
    return nil
}
```

框架在 `New` 里按 `PortOrder()` 逐个启动、失败即中止并经宿主 `Closer` 释放
（`api/assemble.go`），`Replace` 换入时同样先启动再提交。**一次装配只启动一次**，
所以这个 Thinker 实例属于一个 agent，不跨 `New` 复用（`internal/nerve/lifecycle.go`）。

组合器（`GuardStack` / `FallbackEffector`）**不转发 Boot**（框架对交进来的端口值
做能力断言，`internal/nerve/compose.go:31`）。成员各自需要启动就在组合前自己启动，
或者只把 `Boot` 声明在最外层实际使用的那个实例上。内置大脑没有组合器——换模型是
重新 `New`，不是运行期换件。

## 9. 参考实现（带状态的对照客户端，非内置大脑；非流式，可直接改）

以下是**带状态**的对照客户端（它自己攒 `turns` 缓存），所以错误前缀自取（`openai thinker:`）；
内置大脑无状态、不缓存轮次，错误一律 `brain:`（`internal/brain/*.go`）。

```go
type turn struct {
    text  string             // 模型这一轮说的话（重建历史时插回）
    calls []meowire.ToolCall // 它这一轮发出的调用（含被膜拒掉的）
}

type chatThinker struct {
    client openai.Client
    model  openai.ChatModel

    mu    sync.Mutex             // Think 会被并发调用，见 §3
    turns []turn                 // 本 invocation 的模型轮次，按序
}

func newChatThinker(model openai.ChatModel) *chatThinker {
    return &chatThinker{
        client: openai.NewClient(option.WithMaxRetries(0)),   // §6：重试只留一层
        model:  model,
    }
}

// Reset 开一次新对话，由宿主在自己的调用侧触发——只有它知道哪一次是新的。
// 别拿 BeforeStimulate 当这个信号：它是 Cycle 与 Resume 共享的序言
// （internal/nerve/loop.go:159-187），挂起-恢复也会走它，一 Reset 就把
// 正在恢复的那份对话抹掉了。Resume 不调 Reset，turns 原样留着，
// 这正是恢复后还要能重建 assistant/tool 配对的原因。
func (t *chatThinker) Reset() {
    t.mu.Lock()
    t.turns = nil
    t.mu.Unlock()
}

func (t *chatThinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
    tools, err := toolParams(p)                       // §5：每轮现读 p.Tools
    if err != nil {
        return nil, err
    }
    msgs := []openai.ChatCompletionMessageParamUnion{
        openai.SystemMessage(render(p)),              // §2 表里全部固定段
    }
    msgs = append(msgs, t.transcript(p.ToolResults)...)
    msgs = append(msgs, openai.UserMessage(p.Input))

    complete, err := t.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
        Messages: msgs, Model: t.model, Tools: tools,
    })
    if err != nil {
        return nil, fmt.Errorf("openai thinker: %w", err)   // 内核可能按 MaxRetries 重投，见 §6
    }
    if len(complete.Choices) == 0 {
        return nil, errors.New("openai thinker: completion has no choices")
    }
    return t.decisionOf(complete), nil                      // §4
}

// transcript 按 §3 的形状重建：一个 turn 一条 assistant（带该轮文本与全部
// tool_calls），随后按同序各跟一条 tool 消息。内核跨轮累积 ToolResults，
// 所以这里按自己记的 turn 走，而不是按结果切片猜分组。
func (t *chatThinker) transcript(results []meowire.ToolResult) []openai.ChatCompletionMessageParamUnion {
    t.mu.Lock()
    defer t.mu.Unlock()

    content := make(map[string]string, len(results))
    for _, tr := range results {
        content[tr.ID] = cmp.Or(tr.Err, tr.Result)
    }

    var msgs []openai.ChatCompletionMessageParamUnion
    for _, tn := range t.turns {
        if len(tn.calls) == 0 {
            continue
        }
        calls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(tn.calls))
        for _, c := range tn.calls {
            calls = append(calls, openai.ChatCompletionMessageToolCallUnionParam{
                OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
                    ID: c.ID,
                    Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
                        Name: c.Name, Arguments: c.Args,
                    },
                },
            })
        }
        msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
            OfAssistant: &openai.ChatCompletionAssistantMessageParam{
                Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(tn.text)},
                ToolCalls: calls,
            },
        })
        for _, c := range tn.calls {
            // 被膜拒掉的调用不进 ToolResults（§2），但模型确实说过要调它：
            // 每个 tool_call 必须有一条 tool 消息回应，缺一条整个请求就非法，
            // 所以补占位。拒绝原因已经以 [sandbox-denied: ...] 在 Context 里给过模型。
            msgs = append(msgs, openai.ToolMessage(cmp.Or(content[c.ID], "(withheld)"), c.ID))
        }
    }
    return msgs
}
```

`render` 拼 §2 表里全部标"同一消息"的段（含 `Memories` 的渲染），
`decisionOf` 就是 §4 的代码。工具表**必须每轮现建**（`toolParams(p)`）：
`BeforeStimulate` 可以整体改写 `p.Tools`，缓存到启动期会静默丢掉改写，
代价只是一次 JSON 反序列化。固定段与动态段分开的收益（前缀缓存）在 §2 末。

## 10. 第二根 wire：Responses API（`Mode` = 2）

`BrainConfig.Mode`（公开名 `meowire.BrainMode`）在 `Think` 入口分路：`BrainModeChat`
（1，默认；零值同 1）走 §1–§9 的 chat completions，`BrainModeResponses`（2）走本节。
`Mode` 之外的值由 api 层 Validate 拒绝（它是裸枚举校验，没有事件枚举那样的名字表——`Mode` 不上线、
不进事件流，本包也不重复校验）。两根 wire 的契约完全同形：
无状态渲染、单层重试、Sink 先于出口膜、Usage 零值返回 nil、`wrapErr` 带 wire 名
共用（非 2xx 两根都是 `*openai.Error`，错误文本点名是哪根 wire 拒了请求）。

服务与类型在子包 `github.com/openai/openai-go/v3/responses`（根包
`openai.Client.Responses` 直达）：

```go
func (r *ResponseService) New(ctx context.Context, body ResponseNewParams, ...) (res *Response, err error)
func (r *ResponseService) NewStreaming(ctx context.Context, body ResponseNewParams, ...)
    (stream *ssestream.Stream[ResponseStreamEventUnion])
```

`Prompt` → `ResponseNewParams` 落点：

| 进料 | 落点 |
|---|---|
| §2 的 system bundle 全部文本段 | `Instructions`——协议原生的顶层系统指令位，不占 input 列表，固定段/动态段排布照旧吃前缀缓存 |
| `Input` | input 首条 easy input message（role=user、content 内联字符串；该消息上线**不带 type 判别**，role+content 即消息） |
| `ToolResults` | 每个 result 一对：`function_call{call_id, name, arguments:"{}"}` + `function_call_output{call_id, output: cmp.Or(Err, Result)}`——§3 单批配对的 Responses 版，被膜拒掉的调用天然缺席、无需占位 |
| `Tools` | `Tools[].OfFunction`（`FunctionToolParam{Name, Description, Parameters}`）；`Output` 仍折进描述，schema 解析与 §5 共用 |
| 协议恒定项 | **`Store: false`**——内核不发 `previous_response_id`、每轮全量重建，服务端 30 天留存只是没人读的副本；不设 `Conversation`/`Background`/`ToolChoice`/`Strict` |

`*responses.Response` → `Decision` 折回：

- `Status=="failed"` → 错误（带 `Error.Code/Message`，让这轮早死不烧重试预算）；
  `"incomplete"` 按部分文本放行（对称 chat 侧不拦 length 截断）
- `Output` 项：`type=="message"` 的 `output_text` 内容段拼成 `Text`；
  `type=="function_call"` → `ToolCall{ID: call_id, Name, Args: arguments}`（原样透传，
  校验归 Effector 与 Sandbox）
- `Usage{InputTokens, OutputTokens, TotalTokens}` → `Usage{Prompt, Completion, Total}`，
  全零返回 nil

流式：**v3.61.0 没有 Responses 累加器，也不需要**——终态事件
（`response.completed`/`response.failed`/`response.incomplete`）内嵌整只 `Response`，
折回只读它；`response.output_text.delta` 只用来推 Sink（出口膜裁决之前，§7 的
推流+更正契约原样成立）；`error` 事件直接报错；函数调用参数增量不推（Sink 是文本
通道，调用整只在终态事件里到达）。流式与否仍由调用哪个方法决定，`stream:true` 由
SDK 注入请求体；消费侧一旦提前返回，`defer stream.Close()` 兜底（SSE 契约：Next
未返回 false 就停止迭代必须 Close，`closeOnce` 幂等，成功路径的自动关闭是 no-op）。

## 11. 交付前自检

- [ ] `Prompt` 11 个字段各有落点，特别是 `Memories` / `Reflection` / `Bounds`
- [ ] assistant 只列 `ToolResults` 里的调用，每条都有一条 tool 回话；被膜拒掉的
      调用天然缺席（拒绝走 Context 轨，无需占位——占位补法是 §3 带状态对照专属）
- [ ] `Tools` 每轮从 `p.Tools` 现建（没有启动期缓存）；`p.Tools` 为空时返回 `nil`
      ——两根 wire 都不发 `tools` 字段（空数组会被部分 OpenAI 兼容端点拒绝）
- [ ] （仅 §3/§9 带状态对照客户端适用）共享缓存有锁，`Reset` 的触发点由宿主
      定（不是 `BeforeStimulate`）——内置大脑无状态、无 `Reset`，此项对它不适用
- [ ] 只有一层重试（`option.WithMaxRetries(0)` + `Config.MaxRetries`，或反之）
- [ ] `ctx` 一路透传，`Think` 内没有 `WithoutCancel`
- [ ] `Usage` 为零值时返回 `nil`，不发假 `EventUsage`
- [ ] 流式推流策略与 `Sandbox.Emit` 的关系被写明（§7 三选一）
- [ ] 声明了 `Bootable` 的话，该实例只用于一个 agent；组合进 `Fallback*` 时成员自己启动
- [ ] 本仓库的唯一 SDK import 在 `internal/brain`（版本锚 v3.61.0）；`internal/nerve` 零
      provider 词汇，`go list -deps ./internal/nerve` 可证
- [ ] 两根 wire 共用 `toolSchema`/`describeTool`/`wrapErr` 与 Sink 契约；`Mode` 分路只
      发生在 `Think` 入口，chat 路径零改动
- [ ] responses 侧 `Store:false` 且不发 `previous_response_id`；`Status=="failed"` 折回
      为错误（带 Code），`incomplete` 放行部分文本

---

内置大脑无状态：模型的往轮文本不在 `Prompt` 契约里（内核不回传），assistant/tool 配对
从 `ToolResults` 单批重建（一条 assistant 携带全部历史调用 + 每个结果一条 tool 回话），
被膜拒掉的调用天然缺席（拒绝走 Context 轨）。§3 的 turn 状态法是带状态客户端的通用
做法对照——`internal/brain` 选择了更小的形状，取舍记录在
`internal/brain/agent.md`。
