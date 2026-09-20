# meowire 宿主参考实现：从零构建一个 AI Agent

> 阅读对象：要真正把 meowire 跑起来的宿主开发者。
> 本文按步骤实现一个**可运行的 AI 宿主**：OpenAI 兼容 LLM（可换任意厂商）+ 工具分发 + 权限门 + 上下文裁剪 + 记忆 + 多 agent + 持久化。
> 与 [host-integration.md](host-integration.md) 的分工：那边是**契约权威**（所有签名、字段语义），本文是**照做的样板**（完整代码 + 步骤 + 坑）。
> 模块路径：`github.com/qyiun666/meowire`，对外包：`github.com/qyiun666/meowire/api`（别名 `meowire`）。

## 0. 心智模型（先读，30 秒）

- **一个 `Agent` = 一个 agent 内核**。`New(bp)` 一次 = 一个 agent；**多 agent = 同一个 `Blueprint` 多次 `New`** + 宿主自己负责 agent 间的一切通信（channel/HTTP/Redis 任选，框架不持有路由表）。
- 框架给循环（Think → Act → yield 事件流）和**内置大脑**（`Organs.Brain` 传参即得）；工具、权限、记忆是宿主实现——六端口 + Brain 参数全部必填，其余器官没有默认实现。
- 宿主只需要掌握三个方法：`New`（装配）、`Stimulate`（跑一轮）、`Close`（关闭）。
- **框架侧没有任何需要宿主持久化的自主变化值**：需要落盘的全是宿主自己的东西——历史/计划（`Organs.Context`）、记忆端口后面的库、以及 §8.3 的事件 WAL。
- 每次 `Stimulate` 是无状态 step：循环内状态不跨调用保留，历史/计划/进度由宿主外化存储（§9）。

## 1. 步骤总览（7 步）

| 步骤 | 做什么 | 产出 |
|------|--------|------|
| 1 | 建项目，装 meowire | `go.mod` + 目录 |
| 2 | **填 Brain 参数**（内置大脑：BaseURL/Key/Model/Stream/Mode——Mode 选 wire，默认 chat） | 无需写文件 |
| 3 | 实现 Effector（工具注册表 + 分发） | `effector.go` |
| 4 | 实现 Sandbox（权限门）+ ContextBudget（两轨调节器） | `guards.go` |
| 5 | 实现 Memory 端口（Recall/Remember）+ Hooks | `memory.go` |
| 6 | 组装 Blueprint，New + 事件循环 | `main.go` |
| 7 | 扩展多 agent（宿主组合多实例）+ 事件日志（状态外化） | `multi.go` |

每步代码都可独立编译。最终完整代码约 300 行（LLM 对接已由框架内置）。

---

## Step 1：项目骨架

```bash
go mod init myhost
go get github.com/qyiun666/meowire
```

```
myhost/
├── go.mod
├── main.go        # 组装 + 运行（Step 6）
├── effector.go    # Effector 端口（Step 3）
├── guards.go      # Sandbox + ContextBudget（Step 4）
└── memory.go      # Memory 端口 + Hooks（Step 5）
```

---

## Step 2：填 Brain 参数（内置大脑）

LLM 对接已由框架内置：`internal/brain` 是全仓唯一 import openai-go 的包，宿主经
`Organs.Brain` 传四个参数，组合根构造注入——不写 Thinker、不写 LLM 客户端、不 import
任何 SDK。

```go
brain := meowire.BrainConfig{
    BaseURL: os.Getenv("LLM_BASE_URL"), // 空 = 官方 OpenAI 端点；DeepSeek/Ollama/Qwen 换这里
    Key:     os.Getenv("LLM_API_KEY"),
    Model:   os.Getenv("LLM_MODEL"),
    Stream:  true, // SSE 传输（可选）
}
```

流式增量（可选）：`ctx = meowire.WithSink(ctx, sink)` 挂在 `Stimulate` 的 context 上，
delta 在出口膜裁决之前送达；`EventText`（整段、已过膜）到达时整段替换已推内容。

大脑把 `Prompt` 的 11 个字段渲染成 messages、把回复折成 `Decision`（含工具调用与
token 用量），被膜拒掉的调用以 `[sandbox-denied: ...]` 走 Context 轨、配对从
`ToolResults` 单批重建。逐字段行为与协议坑（版本差异、累加器、错误形状）见
[thinker-openai-go.md](thinker-openai-go.md)——那是内置大脑的实现规格。

---

## Step 3：Effector —— 工具分发器

```go
// effector.go
package main

import (
	"context"
	"fmt"

	meowire "github.com/qyiun666/meowire/api"
)

// toolFn 是宿主工具函数：ctx + JSON 参数字符串 → 结果
type toolFn func(ctx context.Context, args string) (*meowire.Effect, error)

type effector struct {
	registry map[string]toolFn
}

func (e *effector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
	fn, ok := e.registry[a.Call.Name]
	if !ok {
		return &meowire.Effect{Err: "unknown tool: " + a.Call.Name}, nil
	}
	return fn(ctx, a.Call.Args)
}
```

**工具失败两种返回方式，行为不同：**

| 返回 | 框架行为 |
|---|---|
| `Effect{Err: "..."}` | 业务错误 → 写入 `ToolResults.Err`（结构化轨），**不重试**（防重复副作用）；渲染归宿主 |
| `return nil, err` | 执行层错误 → 按 `Config.ToolMaxRetries` 重试，超时错误不重试；最终错误同样写入 `ToolResults.Err` |

`spawn_agent` 这类工具在这里实现：工具自己 `New` 一个 `Agent`、消费它的 `Stimulate`、把最终输出作为普通 `Effect.Result` 交回主循环，见 Step 7。

---

## Step 4：Sandbox + ContextBudget —— 两个守卫

```go
// guards.go
package main

import (
	"context"
	"strings"

	meowire "github.com/qyiun666/meowire/api"
)

// ---- Sandbox：循环两侧三态裁决——Allow 守执行、Emit 守出口；拒绝 = 反馈，不终止循环 ----

type sandbox struct {
	allowed      map[string]bool // 工具名白名单（白名单验证，非黑名单）
	dangerous    map[string]bool // 敏感工具 → VerdictAsk 挂起征询
	secretMarker string          // 出口审查标记（出现在草稿里即拒绝该轮文本）
	bounds       string          // 执行边界描述（透传给 LLM）
}

func (s *sandbox) Allow(ctx context.Context, a meowire.Action) (meowire.Verdict, string, error) {
	select {
	case <-ctx.Done():
		return meowire.VerdictDeny, "", ctx.Err()
	default:
	}
	if s.allowed[a.Call.Name] {
		return meowire.VerdictAllow, "", nil
	}
	if s.dangerous[a.Call.Name] {
		// 挂起征询：reason 即问题文本，宿主经 Resume(sess, Response{Answer/Deny}) 批准或拒绝
		return meowire.VerdictAsk, "允许执行 " + a.Call.Name + " 吗？", nil
	}
	return meowire.VerdictDeny, "tool not in whitelist: "+a.Call.Name, nil
}

// Emit 在该轮文本被任何人听到之前过审：拒绝即取代草稿，不终止循环。
func (s *sandbox) Emit(ctx context.Context, u meowire.Utterance) (meowire.Verdict, string, error) {
	if s.secretMarker != "" && strings.Contains(u.Text, s.secretMarker) {
		return meowire.VerdictDeny, "contains a secret", nil
	}
	return meowire.VerdictAllow, "", nil
}

func (s *sandbox) Bounds() string { return s.bounds }

// ---- ContextBudget：每次 Think 前裁两条累积轨；是裁剪器不是硬停 ----

// 粗略 token 估算：英文 ~4 字符/token，中文 ~1.5 字符/token
func trimContext(ctx []string, max int) []string {
	if max <= 0 || len(ctx) <= max {
		return ctx
	}
	kept := ctx[len(ctx)-max:] // 保留最新的 max 条（旧的可丢给记忆系统）
	return kept
}

// 结构化反馈轨同一额度、同一时点裁剪：不裁时原样返回即可
func trimResults(rs []meowire.ToolResult, max int) []meowire.ToolResult {
	if max <= 0 || len(rs) <= max {
		return rs
	}
	return rs[len(rs)-max:]
}
```

**细节：**
- `Allow` 返回 `(VerdictAllow, _, nil)` 放行执行；返回 `(VerdictDeny, reason, nil)` → 框架生成 `[sandbox-denied: reason]` 反馈，**循环继续**——阻力是反馈不是失败（Deny 是零值，fail-closed）
- `Allow` 返回 `(VerdictAsk, 问题, nil)` → **挂起征询**：走与 ask_user 相同的挂起-恢复协议，批准后才执行且不再重新过门禁
- `Allow` 返回 error → 按 Deny 处理（fail-closed），反馈落库为 `[sandbox-denied: sandbox error: ...]`
- `Emit` 每轮 Think 出文本后、该文本进入事件流与累积输出之前调用：`Allow` 原样说出，`Deny` 以 `[sandbox-denied: reason]` 取代该轮文本（并进 Context，下一轮读得到），`Ask` 扣住草稿挂起，批复后按原稿说出、不重跑该轮 Think
- `Emit` 返回 error 同样按 Deny 处理（fail-closed），审计记录 `Call` 为零值即文本侧裁决
- `Bounds()` 每次 `Stimulate` 开始时快照一次进 `Prompt.Bounds`——**边界既是拦截也是提示**
- 裁剪器不想裁时返回入参原切片即可；但两条轨都必须给：`Trimmer`、`TrimResults` 任一为 nil 或 `MaxTokens <= 0` 装配失败

---

## Step 5：Memory 端口 + Hooks（经验回灌）

框架不存储记忆，但它规定**两个时点**：每轮 Think 前 `Recall`、每次调用的终点 `Remember`（P7，必填端口）。检索算法、写什么、留多久都在器官里。文本轨（`p.Context`）另由钩子注入（H1/H3），与 P7 的结构化轨分属两轨。

```go
// memory.go
package main

import (
	"context"
	"fmt"
	"strings"

	meowire "github.com/qyiun666/meowire/api"
)

// hostMemory：极简经验端口（生产换 DB/Redis/向量库，接口不变）
type hostMemory struct {
	entries []meowire.Record // 按写入序保存
}

// Recall：每轮 Think 前被调用一次，返回值进 Prompt.Memories（整轮替换）。
// 这里的匹配只是示范——真正的排序/召回策略属于器官。
func (m *hostMemory) Recall(_ context.Context, q meowire.MemoryQuery) ([]meowire.Record, error) {
	var out []meowire.Record
	for _, r := range m.entries {
		if r.CellID != q.CellID {
			continue
		}
		if q.Cue != "" && !strings.Contains(string(r.Content), q.Cue) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Remember：每次调用的终点恰好一次（正常/错误/挂起/被放弃四条路径都到）。
// 删除与遗忘不经过这个接口——那是宿主直接对自己的后端做的事。
func (m *hostMemory) Remember(_ context.Context, f meowire.CycleFacts) error {
	if f.Output == "" {
		return nil // 挂起与中途放弃没有可存的终稿
	}
	m.entries = append(m.entries, meowire.Record{
		Key:     fmt.Sprintf("%s-%d", f.CellID, len(m.entries)),
		CellID:  f.CellID,
		Kind:    "turn", // f.Outcome 告诉你这次是善终、挂起还是被放弃
		Content: []byte(f.Output),
	})
	return nil
}

// buildHooks 组装全部八回调（缺的由 FullHooks 补显式 no-op）
func buildHooks() *meowire.Hooks {
	return meowire.FullHooks(meowire.Hooks{
		// 回合开始：往文本轨写常驻提示（原型浅拷贝，安全）
		BeforeStimulate: func(ctx context.Context, p *meowire.Prompt) error {
			p.Context = append(p.Context, "[提示] 回答保持简洁")
			return nil
		},
		// 回合结束：结算落点（三路径都恰好一次）
		AfterStimulate: func(ctx context.Context, output string) {},
	})
}
```

**⚠️ 两个关键陷阱：**
1. **`BeforeThink`（不是 `BeforeStimulate`）里必须整体替换 `p.Context`**（`p.Context = append(p.Context[:0], newCtx...)` 或赋新切片）——它和循环内上下文共享 底层数组，直接 append 会污染循环上下文；`p.ToolResults` 同理（与循环结构化轨共享底层数组），整体替换、禁止原地 append。回合级注入放 `BeforeStimulate`（原型浅拷贝，安全），轮内动态注入才放 `BeforeThink`
2. `AfterStimulate` 的 `output` 参数是整轮累计输出——三路径（正常/错误/消费者 break）都恰好执行一次，是结算和持久化的正确落点

---

## Step 6：组装 + 运行

```go
// main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time" // ToolTimeout 用

	meowire "github.com/qyiun666/meowire/api"
)

var (
	baseURL = envOr("LLM_BASE_URL", "") // 空 = 官方端点
	apiKey  = os.Getenv("LLM_API_KEY")
	model   = envOr("LLM_MODEL", "gpt-4o-mini")
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	mem := &hostMemory{}

	// ① Blueprint 一次定义 → 多 agent 复用（Step 7 的关键）
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-main",
			Brain:   meowire.BrainConfig{BaseURL: baseURL, Key: apiKey, Model: model, Stream: true},
			Act:     &effector{registry: toolRegistry()},
			Closer:  &noopCloser{},
			Hooks:   buildHooks(),
			Sandbox: &sandbox{allowed: map[string]bool{"calc": true}, bounds: "只允许 calc 工具"},
			Budget:  &meowire.ContextBudget{MaxTokens: 20, Trimmer: trimContext, TrimResults: trimResults},
			Mem:     mem,

			System:   "你是 meow agent，用中文回答，简洁直接。",
			Identity: "你叫 meow，角色 assistant",
			Tools: []meowire.ToolSpec{
				{
					Name:   "calc",
					Desc:   "计算数学表达式",
					Input:  `{"type":"object","properties":{"expr":{"type":"string"}},"required":["expr"]}`,
					Output: "计算结果数字",
				},
			},
			Context: []string{"[用户] 喜欢简洁回答"},
		},
		Config: meowire.Config{
			MaxRounds:      8,            // 硬上限，防死循环
			MaxToolOutput:  2000,         // 工具反馈截断
			MaxRetries:     2,            // Think 重试
			ToolTimeout:    30 * time.Second, // 单工具 30s 超时
			ToolMaxRetries: 1,            // 工具执行层错误重试 1 次
		},
	}

	agent, err := meowire.New(bp)
	if err != nil {
		log.Fatalf("assembly: %v", err) // 缺端口/缺回调在这里失败
	}
	defer agent.Close()

	var pendingSession meowire.Session // 挂起会话句柄（EventWaitInput 带出，Resume 传回）

	// ② 消费事件流（Step-Resume：break 即放弃本轮，可稍后再 Stimulate）
	for ev := range agent.Stimulate(context.Background(), "帮我算 (23+45)*2") {
		switch ev.Kind {
		case meowire.EventText:
			fmt.Println("🧠", ev.Text)
		case meowire.EventToolCall:
			fmt.Println("🔧", ev.ToolCall.Name, ev.ToolCall.Args)
		case meowire.EventToolResult:
			fmt.Println("📦", ev.Effect.Result, ev.Effect.Err)
		case meowire.EventSandbox:
			fmt.Printf("🛡 %s %s ruling=%v reason=%q ask=%q\n", ev.Verdict.CellID, ev.Verdict.Call.Name, ev.Verdict.Ruling, ev.Verdict.Reason, ev.Verdict.Question)
		case meowire.EventWaitInput: // 挂起：保存 Session，向用户展示问题；答复到达后 Resume(ctx, sess, resp) 续跑
			fmt.Println("❓", ev.Wait.Call.Name, ev.Wait.Question)
			pendingSession = ev.Wait.Session
		case meowire.EventReplace: // 端口替换审计：模型切换闭环从这里取，不再手工维护状态机
			fmt.Printf("🔁 slot=%s old=%T new=%T\n", ev.Replace.Slot, ev.Replace.OldType, ev.Replace.NewType)
		case meowire.EventUsage:
			fmt.Println("💰", ev.Usage.Total, "tokens")
		case meowire.EventDone:
			fmt.Println("✅", ev.Output)
		case meowire.EventError:
			log.Printf("❌ %v", ev.Err) // 含 ErrMaxRounds → 可 Step-Resume 续跑
			return
		}
	}
}
```

配套小件：

```go
type noopCloser struct{}

func (c *noopCloser) Close() error { return nil }

func toolRegistry() map[string]toolFn {
	return map[string]toolFn{
		"calc": func(ctx context.Context, args string) (*meowire.Effect, error) {
			var p struct{ Expr string `json:"expr"` }
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return &meowire.Effect{Err: "bad args: " + err.Error()}, nil
			}
			return &meowire.Effect{Result: calc(p.Expr)}, nil // calc 用 expr 库或内置 eval
		},
	}
}
```

到这里单 agent 已跑通。验证方式：`go run .`，观察 Think → Act → Result → 第二轮 Think 的事件序列。

---

## Step 7：多 agent（宿主组合多实例）+ 事件日志

### 8.1 多 agent = 多次 New（复用同一 Blueprint）

一个 `Agent` 是一个内核；多 agent 就是多个 `Agent` 实例。Blueprint 一次定义，多处 `New`——注意 **`Organs.ID` 是实例身份（必填、各实例不得重名）**，且 `Organs.Context` 是切片、`Hooks` 闭包捕获，同一 bp 的实例共享它们，所以多实例要各自覆写：

```go
func spawn(bp meowire.Blueprint, id string, mem *hostMemory) *meowire.Agent {
	bp.Organs.ID = id
	bp.Organs.Context = []string{}          // 每个 agent 独立历史
	bp.Organs.Hooks = buildHooks()          // 钩子只管文本轨；记忆是端口（见 Step 5）
	ag, err := meowire.New(bp)
	if err != nil {
		log.Fatal(err)
	}
	return ag
}
```

子 agent 的创建位置：**宿主 Effector 工具内**（`spawn_agent`），对主循环完全透明（扁平模型，无框架级嵌套）。

### 8.2 agent 间协作：宿主自己接，两种形状

内核不知道有第二个实例，所以「A 问 B」就是宿主写的一段普通代码。两种形状够用：

**① 把 B 做成 A 的一个工具**（A 同步等 B 的结果，最常见）：

```go
// ask_helper 工具：主 agent 的一次工具调用 = 起一个 helper 跑一轮
func askHelperTool(bpHelper meowire.Blueprint) toolFn {
	return func(ctx context.Context, args string) (*meowire.Effect, error) {
		helper, err := meowire.New(bpHelper)
		if err != nil {
			return &meowire.Effect{Err: "helper assembly: " + err.Error()}, nil
		}
		defer helper.Close()
		var out string
		for ev := range helper.Stimulate(ctx, args) {
			switch ev.Kind {
			case meowire.EventError:
				return &meowire.Effect{Err: "helper: " + ev.Err.Error()}, nil
			case meowire.EventDone:
				out = ev.Output
			}
		}
		return &meowire.Effect{Result: out}, nil // 结果走正常反馈轨
	}
}
```

要「问哪个专家」由模型的参数决定，宿主分发器查自己那张 `名字 → Blueprint` 的表——
这张表就是路由，框架不持有它。**B 挂起了怎么办**：B 的 `EventWaitInput` 出现在 A 的工具里，
A 的主循环看不见那个 `Session`，所以要么在这里由宿主当场回答（自动化），要么把 B 的
`Session` 存进宿主任务表、改判为「本次工具结果 = 等待人工」。别让一个 agent 替另一个
agent 决定何时继续——这条判断从头到尾都是宿主的。

**② 把 A 的输出喂进 B 的 `Stimulate`**（流水线，A 不等 B）：A 的 `EventDone` 落进宿主
队列，宿主用队列内容起 B。跨进程就把 `EncodeEvent` 的 JSON 行当传输单元（每条自带
`CellID` 与版本闸门）。
### 8.3 事件日志（状态外化：WAL）

`Stimulate` 的事件流就是执行轨迹。宿主把它 append-only 落盘，即得 WAL——崩溃后重放日志 → 重建上下文 → 重新 `Stimulate` 续跑：

```go
// 事件流持久化（每次 Stimulate 消费时同步写）
func consumeAndLog(agent *meowire.Agent, logf func(meowire.Event) error) {
	for ev := range agent.Stimulate(context.Background(), input) {
		if err := logf(ev); err != nil { // 追加写入 WAL（JSON 行）
			handle(err)
		}
		handleEvent(ev)
	}
}
```

配合 `OnCycleEnd` 把每轮输出/计划落盘 checkpoint，宿主即可实现崩溃恢复与审计（详见 [protocols.md §5](protocols.md)）。

---

## 9. 集成注意事项（细节与坑）

### LLM 对接（内置大脑）

1. **无工具时大脑自动省略 `tools` 字段**——部分厂商（Ollama 等）对空数组报错，内置大脑已处理
2. **`ToolSpec.Input` 必须是合法 JSON Schema 字符串**——LLM 按它生成参数；写错 Schema 会导致工具调用 JSON 解析失败
3. **LLM 返回的 `arguments` 是 JSON 字符串**，Effector 侧必须 `json.Unmarshal`；解析失败返回 `Effect{Err: ...}` 让 LLM 自我纠正（阻力是反馈）
4. **流式增量走 `WithSink`**（挂在 Stimulate 的 ctx 上，先于出口膜裁决送达；`EventText` 整段到达时替换已推内容）——见 [host-integration.md §7.3](host-integration.md)
5. **ctx 全程透传**：大脑与工具都吃 `Stimulate` 的 ctx；取消/暂停超时靠它落地

### 循环语义

6. **`ToolCalls` 空 = 循环结束**——LLM 每轮都要返回内容或工具调用，两者皆空会得到空结果
7. **`ErrMaxRounds` 不是 bug**：最后一轮仍有工具调用时抛出；配合 Step-Resume（宿主保存进度 → 重新 `Stimulate` 传剩余任务）是预期用法
8. **提前停止（宿主主动接管）**：`for range` 中 break 即放弃本轮，停止点之后的工具不执行——宿主主动接管（人工审批、异步任务）用此路径；工具请求输入（ask_user）的唯一形式是 `Effect.WaitInput` + `EventWaitInput`/`Resume`（上文 ②），不要用 break + `Stimulate` 模拟（`Session` 不透明，挂起上下文无法手工重建）
9. **事件流是观察镜像**：不能往打开中的迭代器回喂数据；数据回喂走下一次 `Stimulate`
10. **暂停 vs Step-Resume**：Pause 为快照挂起（EventPaused + Session，迭代器正常结束，`Resume(sess, Response{})` 续跑，不占轮次）；Step-Resume 放弃本轮无状态重来

### 记忆与上下文

11. **`BeforeThink` 必须整体替换 `p.Context`**（共享底层数组，直接 append 污染循环上下文）；`p.ToolResults` 同理；回合级注入放 `BeforeStimulate`
12. **`Organs.Context` 是切片且每轮快照**：宿主每次 `Stimulate` 前经 `BeforeStimulate` 更新为最新历史（MemHop：历史宿主管，框架不消费）
13. **裁剪器只剪不报错**：`MaxTokens` 超了不会中断循环，只会丢上下文条目——想硬停用别的机制

### 安全（G-02 落点）

14. **Sandbox 用白名单**（允许什么），不是黑名单；`Bounds()` 的描述要如实反映边界（LLM 会读到）
15. **`Allow` 每次工具执行前调用**——动作级授权 + `EventSandbox` 事件即审计记录，宿主持久化事件流即得完整审计
16. **工具参数反序列化要容错**：LLM 可能给错 Schema 的参数，按 `Effect.Err` 反馈而非 panic

### 持久化（状态外化）

17. **框架不持有任何需要持久化的状态**：`Agent` 的一次运行要么跑完要么挂在 `Session` 里，后者本来就该由宿主保存；要落盘的是宿主自己的历史、记忆库和事件 WAL
18. **保存时机决定丢失窗口**：只在 `Close` 保存会丢异常退出前的进度；重负载场景用每轮 `OnCycleEnd` 快照 + WAL（§8.3）
19. **事件日志用 `meowire.EncodeEvent` 按 JSON 行 append** 即可作 WAL（别直接 `json.Marshal(ev)`：`Err` 字段会被写成 `{}`，枚举也变成数字）；恢复流程 = 逐行 `DecodeEvent` → 重放日志 → 重建 `Organs.Context` → 重新 `Stimulate`，还原不完整的事件其 `Dropped` 字段会点名

### 多 agent（宿主组合）

20. **路由表在宿主手里**：谁能被谁问到、以什么形态（同步工具 / 队列 / HTTP），宿主一张表决定；
    内核只给 `Organs.ID` 给事件署名，从不按名字找另一个实例
21. **两个「框架不决策」**：一次工具调用起子 agent 后，子 agent 挂起了不由框架续（框架从不代
    `Resume`）；子 agent 的终态也不由框架映射成任务状态——`OnCycleEnd` 的 `CycleOutcome` 是
    唯一判词，怎么翻成宿主任务表里的状态归宿主

## 10. 相关文档

| 文档 | 位置 | 内容 |
|------|------|------|
| 契约权威 | [host-integration.md](host-integration.md) | 所有接口签名、字段语义、事件序列、陷阱清单 |
| 大脑规格 | [thinker-openai-go.md](thinker-openai-go.md) | 内置大脑的逐字段落点表与协议参考 |
| 协议映射 | [protocols.md](protocols.md) | MCP / A2A / AGENTS.md / Authority / 长时任务状态外化 |
| 动态接线 | [wiring.md](host-integration.md#51-动态接线-replace运行时换器官) | `Replace` 运行时换端口 |
