# meowire 宿主参考实现：从零构建一个 AI Agent

> 阅读对象：要真正把 meowire 跑起来的宿主开发者。
> 本文按步骤实现一个**可运行的 AI 宿主**：OpenAI 兼容 LLM（可换任意厂商）+ 工具分发 + 权限门 + 上下文裁剪 + 记忆 + 多 agent + 持久化。
> 与 [host-integration.md](host-integration.md) 的分工：那边是**契约权威**（所有签名、字段语义），本文是**照做的样板**（完整代码 + 步骤 + 坑）。
> 模块路径：`github.com/qyiun666/meowire`，对外包：`github.com/qyiun666/meowire/api`（别名 `meowire`）。

## 0. 心智模型（先读，30 秒）

- **一个 `Agent` = 一个 agent 内核**。`New(bp)` 一次 = 一个 agent；**多 agent = 同一个 `Blueprint` 多次 `New`** + 宿主自己负责 agent 间通信（channel/HTTP/Redis 任选，synapse 是参考实现）。
- 框架只给循环（Think → Act → yield 事件流）；**LLM、工具、权限、记忆全是宿主实现**——七端口全部必填，没有默认实现。
- 宿主只需要掌握三个方法：`New`（装配）、`Stimulate`（跑一轮）、`Close`（关闭）。
- **唯一需要持久化的自主变化值**：synapse 突触图的 `Weight`（学习规则改写）、`Fired`（投递计数）与 `Spiked`（最近投递时刻）——导出用 `Edges`，恢复用 `NewDirect(DirectConfig{Initial: ...})`，全程宿主在组合根手动做（§8.3）。
- 每次 `Stimulate` 是无状态 step：循环内状态不跨调用保留，历史/计划/进度由宿主外化存储（§9）。

## 1. 步骤总览（8 步）

| 步骤 | 做什么 | 产出 |
|------|--------|------|
| 1 | 建项目，装 meowire | `go.mod` + 目录 |
| 2 | 写 LLM 客户端（OpenAI 兼容，标准库实现） | `llm.go` |
| 3 | **实现 Thinker**（核心：Prompt → messages → Decision） | `thinker.go` |
| 4 | 实现 Effector（工具注册表 + 分发） | `effector.go` |
| 5 | 实现 Sandbox（权限门）+ ContextBudget（两轨调节器） | `guards.go` |
| 6 | 实现 Memory 端口（Recall/Remember）+ Hooks | `memory.go` |
| 7 | 组装 Blueprint，New + 事件循环 | `main.go` |
| 8 | 扩展多 agent + synapse 持久化 + 事件日志（状态外化） | `multi.go` |

每步代码都可独立编译。最终完整代码约 500 行。

---

## Step 1：项目骨架

```bash
go mod init myhost
go get github.com/qyiun666/meowire
```

```
myhost/
├── go.mod
├── main.go        # 组装 + 运行（Step 7）
├── llm.go         # LLM 客户端（Step 2）
├── thinker.go     # Thinker 端口（Step 3）
├── effector.go    # Effector 端口（Step 4）
├── guards.go      # Sandbox + ContextBudget（Step 5）
└── memory.go      # Memory 端口 + Hooks（Step 6）
```

---

## Step 2：LLM 客户端（OpenAI 兼容）

用标准库 `net/http` 实现，零第三方依赖（宿主想用官方 SDK 也可以，接口一样）。任何 OpenAI 兼容端点都可用：`https://api.openai.com/v1`、DeepSeek、Ollama（`http://localhost:11434/v1`）、Qwen 等，换 `baseURL` + `apiKey` 即可。

```go
// llm.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ---- 与 LLM API 交互的最小结构 ----

type chatMsg struct {
	Role    string          `json:"role"` // system / user / assistant / tool
	Content string          `json:"content"`
	Tools   []toolCallMsg   `json:"tool_calls,omitempty"` // assistant 回复中的工具调用
	ToolID  string          `json:"tool_call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
}

type toolCallMsg struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function toolFunc `json:"function"`
}

type toolFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 字符串
}

// function schema（对应 meowire.ToolSpec 的投影）
type funcSchema struct {
	Type       string         `json:"type"`                 // "function"
	Function   funcDetail     `json:"function"`
}

type funcDetail struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // 直接内嵌 ToolSpec.Input 的 JSON Schema（见 Step 3 的 toolSchemas）
}

type chatRequest struct {
	Model    string      `json:"model"`
	Messages []chatMsg   `json:"messages"`
	Tools    []funcSchema `json:"tools,omitempty"` // 无工具时省略（部分厂商空数组会报错）
}

type chatResp struct {
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// llmClient：OpenAI 兼容 chat/completions 客户端
type llmClient struct {
	baseURL string // 如 https://api.openai.com/v1
	apiKey  string
	model   string
	http    *http.Client
}

func newLLMClient(baseURL, apiKey, model string) *llmClient {
	return &llmClient{
		baseURL: baseURL, apiKey: apiKey, model: model,
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

// Chat 发起一次非流式补全。ctx 取消时立即返回（Thinker 依赖它响应框架取消）。
func (c *llmClient) Chat(ctx context.Context, msgs []chatMsg, tools []funcSchema) (*chatResp, error) {
	body, _ := json.Marshal(chatRequest{Model: c.model, Messages: msgs, Tools: tools})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("llm: status %d: %s", resp.StatusCode, b)
	}
	var out chatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("llm: decode: %w", err)
	}
	return &out, nil
}
```

**坑**：`resp.Body` 必须读完再关（这里是 decode 读完）；`ctx` 必须传 `NewRequestWithContext`，否则框架取消 Think 时你的 HTTP 请求不会中断。

---

## Step 3：Thinker —— AI 对接核心（本文重点）

Thinker 的职责一句话：**把 `meowire.Prompt` 渲染成 LLM messages，把 LLM 回复翻译成 `meowire.Decision`**。框架只认这两个数据包，中间全部宿主自由发挥。

```go
// thinker.go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	meowire "github.com/qyiun666/meowire/api"
)

type thinker struct {
	client *llmClient
}

func (t *thinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err // 已取消就直接退，不白打 API
	}
	msgs := buildMessages(p)
	tools := toolSchemas(p.Tools)
	resp, err := t.client.Chat(ctx, msgs, tools)
	if err != nil {
		return nil, err // 框架会按 Config.MaxRetries 重试，并最终作为 EventError 透出
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("thinker: empty choices")
	}
	m := resp.Choices[0].Message

	// 翻译：LLM 的工具调用 → meowire.ToolCall{ID, Name, Args}
	var calls []meowire.ToolCall
	for _, tc := range m.Tools {
		calls = append(calls, meowire.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
	}

	return &meowire.Decision{
		Text:      m.Content,
		ToolCalls: calls, // 空 = 循环结束（没有工具要调）
		Usage: &meowire.Usage{
			Prompt:     resp.Usage.PromptTokens,
			Completion: resp.Usage.CompletionTokens,
			Total:      resp.Usage.TotalTokens,
		}, // 返回 nil 则跳过 EventUsage 计费事件
	}, nil
}
```

### Prompt → messages 渲染规则（宿主可自由定制，这里是推荐做法）

| Prompt 字段 | 渲染为 | 说明 |
|---|---|---|
| `System` | `system` 消息 | 系统指令（角色、行为规则） |
| `Identity` | 追加进 `system` | 身份描述文本 |
| `Bounds` | 追加进 `system` | 执行边界（"只能访问 /workspace"）——让大脑知道限制 |
| `Methods` | 追加进 `system` | 内置能力声明（只描述不执行） |
| `Tools` | `tools` 参数 | function schema（Step 3 下） |
| `Context` | 多条 `system`/`user` | 记忆基底 + 框架追加的 sandbox 裁决（工具结果不在文本轨） |
| `ToolResults` | 追加进 `user` | 结构化工具结果（`[tool_call_id=xxx]` 标记条目，见下）——框架唯一反馈轨道，必须渲染 |
| `Input` | `user` 消息 | 本次刺激 |
| `Plan` | 追加进 `user` | 任务计划 |

```go
func buildMessages(p *meowire.Prompt) []chatMsg {
	var sys bytes.Buffer // 需要 import bytes
	fmt.Fprintf(&sys, "%s\n\n%s\n\n执行边界：%s", p.System, p.Identity, p.Bounds)
	for _, m := range p.Methods {
		fmt.Fprintf(&sys, "\n能力：%s — %s", m.Name, m.Desc)
	}
	msgs := []chatMsg{{Role: "system", Content: sys.String()}}
	for _, c := range p.Context {
		msgs = append(msgs, chatMsg{Role: "system", Content: "[上下文] " + c})
	}
	// 工具结果：结构化条目（user 内联，带 tool_call_id 标记）。
	// 想升级为原生 tool 角色消息（OpenAI 系要求历史 assistant 消息
	// 含对应 tool_calls）时，以 ID 关联即可——ID 已由框架透传。
	var fb bytes.Buffer
	for _, tr := range p.ToolResults {
		if tr.Err != "" {
			fmt.Fprintf(&fb, "\n[tool_call_id=%s][tool-result %s] error: %s", tr.ID, tr.Name, tr.Err)
		} else {
			fmt.Fprintf(&fb, "\n[tool_call_id=%s][tool-result %s] %s", tr.ID, tr.Name, tr.Result)
		}
	}
	if fb.Len() > 0 {
		msgs = append(msgs, chatMsg{Role: "user", Content: "[工具结果]" + fb.String()})
	}
	userText := p.Input
	if p.Plan != "" {
		userText += "\n[当前计划] " + p.Plan
	}
	msgs = append(msgs, chatMsg{Role: "user", Content: userText})
	return msgs
}
```

### Tools → function schema（`ToolSpec.Input` 本来就是 JSON Schema，直接内嵌）

```go
func toolSchemas(tools []meowire.ToolSpec) []funcSchema {
	out := make([]funcSchema, 0, len(tools))
	for _, t := range tools {
		out = append(out, funcSchema{
			Type: "function",
			Function: funcDetail{
				Name:        t.Name,
				Description: t.Desc,
				Parameters:  json.RawMessage(t.Input), // 原样内嵌 JSON Schema，LLM 据此生成参数
			},
		})
	}
	return out
}
```

`ToolSpec.Input` 约定就是 JSON Schema 字符串（如 `{"type":"object","properties":{...}}`），所以**宿主不需要转换**——直接透传。参考 Step 7 的 `ToolSpec` 写法。

**Thinker 的四个铁律：**
1. **必须监控 `ctx.Done`**——长请求可能被框架取消（Close、宿主取消）；注意 v1.3.2 起 Pause 不再阻塞迭代器（快照挂起，EventPaused 后正常结束），不会通过 ctx 取消来中断暂停
2. **流式输出在 Thinker 内部消费**（如推 WebSocket/SSE）；事件流的 `EventText` 永远整段文本
3. **`ToolCalls` 为空 = 循环结束**——如果 LLM 没调工具也没给文本，会得到空输出但正常结束
4. **并发安全**：同一 Agent 并发 `Stimulate` 时 Thinker 被并发调用（`llmClient` 无共享可变状态，天然安全）

---

## Step 4：Effector —— 工具分发器

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

多 agent 工具里 `spawn_agent` 在这里实现；委托邻居不必自己持有 synapse——返回 `Effect{Send: ...}` 交给框架投递与配对，见 Step 8。

---

## Step 5：Sandbox + ContextBudget —— 两个守卫

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
		// 挂起征询：reason 即问题文本，宿主经 Resume(sess, resp) 批准或拒绝
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

## Step 6：Memory 端口 + Hooks（经验回灌）

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

## Step 7：组装 + 运行

```go
// main.go
package main

import (
	"context"
	"fmt"
	"log"
	"time" // ToolTimeout 用

	meowire "github.com/qyiun666/meowire/api"
)

func main() {
	client := newLLMClient("https://api.openai.com/v1", "sk-xxx", "gpt-4o-mini")
	mem := &hostMemory{}

	// ① Blueprint 一次定义 → 多 agent 复用（Step 8 的关键）
	bp := meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "agent-main",
			Think:   &thinker{client: client},
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
		case meowire.EventWaitInput: // 挂起：保存 Session，向用户展示问题；响应到达后 Resume(ctx, sess, ans) 续跑
			fmt.Println("❓", ev.Wait.Call.Name, ev.Wait.Question)
			pendingSession = ev.Wait.Session
		case meowire.EventReplace: // 端口替换审计：模型切换闭环从这里取，不再手工维护状态机
			fmt.Printf("🔁 slot=%s old=%T new=%T\n", ev.Replace.Slot, ev.Replace.Old, ev.Replace.New)
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

## Step 8：多 agent + 持久化

### 8.1 多 agent = 多次 New（复用同一 Blueprint）

一个 `Agent` 是一个内核；多 agent 就是多个 `Agent` 实例。Blueprint 一次定义，多处 `New`——注意 **`Organs.Context` 是切片、`Hooks` 闭包捕获**，同一 bp 的实例共享它们，所以多实例要各自覆写：

```go
func spawn(bp meowire.Blueprint, id string, mem *hostMemory) *meowire.Agent {
	bp.Organs.ID = id
	bp.Organs.Context = []string{}          // 每个 agent 独立历史
	bp.Organs.Hooks = buildHooks()          // 钩子只管文本轨；记忆是端口（见 Step 6）
	ag, err := meowire.New(bp)
	if err != nil {
		log.Fatal(err)
	}
	return ag
}
```

子 agent 的创建位置：**宿主 Effector 工具内**（`spawn_agent`），对主循环完全透明（扁平模型，无框架级嵌套）。

### 8.2 agent 间委托：工具只说「问谁」，投递/答复/配对归框架

装配顺序要先建图、再装 agent、最后回填路由表（colony 本身是环形的）：

```go
// multi.go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	meowire "github.com/qyiun666/meowire/api"
)

func buildColony(bpMain, bpHelper meowire.Blueprint) (*meowire.Agent, *meowire.Agent, error) {
	syn := meowire.NewDirect(meowire.DirectConfig{}) // 参考实现，resolver 先空着
	bpMain.Organs.Colony, bpHelper.Organs.Colony = syn, syn
	main, err := meowire.New(bpMain)
	if err != nil {
		return nil, nil, fmt.Errorf("main agent: %w", err)
	}
	helper, err := meowire.New(bpHelper)
	if err != nil {
		main.Close()
		return nil, nil, fmt.Errorf("helper agent: %w", err)
	}
	r, err := meowire.Resolve(main, helper)       // ID → 各自收件箱（重复 ID 即拒）
	if err != nil {
		return nil, nil, err
	}
	syn.SetResolver(r)                            // 回填，环闭合
	for _, e := range [][2]string{{"main", "helper"}, {"helper", "main"}} {
		if err := syn.Link(context.Background(), e[0], e[1], 1); err != nil { // 方向要各自连
			return nil, nil, err
		}
	}
	return main, helper, nil
}
```

**委托工具**（主 agent 侧）——只填 `To`/`Payload`，`ID`/`From`/`Kind`/`Status` 归框架：

```go
// delegate 工具：把问题交给邻居，本轮挂起等回信
func delegateTool() toolFn {
	return func(ctx context.Context, args string) (*meowire.Effect, error) {
		var p struct {
			To   string `json:"to"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return &meowire.Effect{Err: "bad args"}, nil
		}
		// 框架铸 ID/From/Kind=submitted → 经 Colony 投递 → 挂起该调用（同 WaitInput 路径）
		return &meowire.Effect{Send: &meowire.Signal{To: p.To, Payload: []byte(p.Text)}}, nil
	}
}
```

投不出去（没接 Colony、目标忙、未知 agent、边没连）**不挂起**，错误按普通工具反馈回流——阻力是反馈不是硬停。

**被委托方不需要任何代码来"回话"**：它的 `Stimulate` 在自己的终点自动向请求方发一条 `KindResponse`（载荷 = 本次调用的最终输出，状态 = `TaskOutcome`：completed / needs-input / failed / cancelled）。入站侧同样零宿主代码：框架在该 agent 的下一个 Think 间隙点排空收件箱写入 `Prompt.Stimuli`（`KindNotice` 若带工具名则本轮撤下该工具，进 `Prompt.Inhibit`）。

**宿主只剩一件事：决定何时继续。**

```go
// 宿主侧回合循环里的续跑分支——框架绝不自行 Resume
for _, r := range main.Resumptions() {
	for ev := range main.Resume(ctx, r.Session, string(r.Response)) {
		relay(ev) // 正常消费事件流
	}
	main.Ack(r.SignalID) // 销账；之后同一请求的回信按普通入站信号呈现
}
```

一个任务可以先报 `needs-input` 再报 `completed`，两条回信配在同一次委托上；`Resumptions()` 是快照，读到 `Ack` 之前一直在。

### 8.3 synapse 持久化：宿主存储，New 时带回来（自主变化值）

`Weight`/`Fired`/`Spiked` 是仅有的自主变化数值。**框架只提供原语，存储由宿主做**——推荐 JSON 文件，生产换 DB/Redis 同理：

```go
// 保存（Agent.Close 时或周期性调用）
func saveSynapse(ctx context.Context, colony meowire.Synapse, path string) error {
	snap, err := colony.Edges(ctx, "") // 全图深拷贝快照
	if err != nil {
		return err
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// 恢复（New 之前，组合根里）
func loadSynapse(ctx context.Context, resolver meowire.Resolver, path string) (meowire.Synapse, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return meowire.NewDirect(meowire.DirectConfig{Resolver: resolver}), nil // 首次启动：空图
	}
	var restored []meowire.Edge
	if err := json.Unmarshal(data, &restored); err != nil {
		return nil, err
	}
	// 注入初始边 = 上次的 Weight/Fired/Spiked 全部带回
	return meowire.NewDirect(meowire.DirectConfig{Resolver: resolver, Initial: restored}), nil
}
```

**要点：**
- `Edges(ctx, "")` 返回深拷贝，导出后可安全序列化；恢复时 `DirectConfig{Initial: ...}` 会 clamp 负权重、去重覆盖——**幂等**
- 时刻 `Spiked` 随快照一起回来，所以 `STDPFrom` 在重启后仍能凭图配对时间，不需要宿主补时钟
- 恢复发生在 `New` 之前、组合根内——**不需要 `Agent.New` 感知 synapse**，它俩本来就不该耦合
- 保存时机宿主自定：`Close` 时、每轮 `OnCycleEnd`、或定期 ticker（重负载场景防丢失）

### 8.4 事件日志（状态外化：WAL）

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

### LLM 对接

1. **无工具时不要传 `tools` 参数**——部分厂商（Ollama 等）空数组报错；`toolSchemas` 返回空时省略字段
2. **`ToolSpec.Input` 必须是合法 JSON Schema 字符串**——LLM 按它生成参数；写错 Schema 会导致工具调用 JSON 解析失败
3. **LLM 返回的 `arguments` 是 JSON 字符串**，Effector 侧必须 `json.Unmarshal`；解析失败返回 `Effect{Err: ...}` 让 LLM 自我纠正（阻力是反馈）
4. **流式输出在 Thinker 内部转发**（WebSocket/SSE/hub channel），事件流只承载整段 `EventText`——见 [host-integration.md §7.3](host-integration.md)
5. **Thinker 必须响应 `ctx.Done`**：HTTP 用 `NewRequestWithContext`，SDK 用带 ctx 的方法；否则取消/暂停超时无法中断

### 循环语义

6. **`ToolCalls` 空 = 循环结束**——LLM 每轮都要返回内容或工具调用，两者皆空会得到空结果
7. **`ErrMaxRounds` 不是 bug**：最后一轮仍有工具调用时抛出；配合 Step-Resume（宿主保存进度 → 重新 `Stimulate` 传剩余任务）是预期用法
8. **提前停止（宿主主动接管）**：`for range` 中 break 即放弃本轮，停止点之后的工具不执行——宿主主动接管（人工审批、异步任务）用此路径；工具请求输入（ask_user）的唯一形式是 `Effect.WaitInput` + `EventWaitInput`/`Resume`（上文 ②），不要用 break + `Stimulate` 模拟（`Session` 不透明，挂起上下文无法手工重建）
9. **事件流是观察镜像**：不能往打开中的迭代器回喂数据；数据回喂走下一次 `Stimulate`
10. **暂停 vs Step-Resume（v1.3.2）**：Pause 为快照挂起（EventPaused + Session，迭代器正常结束，`Resume(sess, "")` 续跑，不占轮次）；Step-Resume 放弃本轮无状态重来

### 记忆与上下文

11. **`BeforeThink` 必须整体替换 `p.Context`**（共享底层数组，直接 append 污染循环上下文）；`p.ToolResults` 同理；回合级注入放 `BeforeStimulate`
12. **`Organs.Context` 是切片且每轮快照**：宿主每次 `Stimulate` 前经 `BeforeStimulate` 更新为最新历史（MemHop：历史宿主管，框架不消费）
13. **裁剪器只剪不报错**：`MaxTokens` 超了不会中断循环，只会丢上下文条目——想硬停用别的机制

### 安全（G-02 落点）

14. **Sandbox 用白名单**（允许什么），不是黑名单；`Bounds()` 的描述要如实反映边界（LLM 会读到）
15. **`Allow` 每次工具执行前调用**——动作级授权 + `EventSandbox` 事件即审计记录，宿主持久化事件流即得完整审计
16. **工具参数反序列化要容错**：LLM 可能给错 Schema 的参数，按 `Effect.Err` 反馈而非 panic

### 持久化（状态外化）

17. **synapse 是唯一有自主变化值的组件**（`Weight`/`Fired`/`Spiked`）——`Edges` 导出 / `NewDirect` 恢复，宿主在组合根做，`Agent.New` 不参与
18. **保存时机决定丢失窗口**：只在 `Close` 保存会丢异常退出前的变异；重负载场景用周期性快照 + WAL（§8.3/§8.4）
19. **事件日志用 `meowire.EncodeEvent` 按 JSON 行 append** 即可作 WAL（别直接 `json.Marshal(ev)`：`Err` 字段会被写成 `{}`，枚举也变成数字）；恢复流程 = 逐行 `DecodeEvent` → 重放日志 → 重建 `Organs.Context` → 重新 `Stimulate`，还原不完整的事件其 `Dropped` 字段会点名

### 多 agent（colony）

20. **收件箱归框架**（[host-integration.md §7.2](host-integration.md)）：每个 cell 自带 `InboxCapacity` 容量的收件箱，`meowire.Resolve(agents...)` 只是把 ID 映射到它；队列满时 `Fire` 返回 `ErrTargetBusy`，从不阻塞发送方。投递只是填队列——目标 cell 在下一个 Think 间隙点、或宿主读 `Resumptions()` 时才看到它
21. **两处「框架不决策」**：信号到了不等于跑了（框架不代为 `Stimulate`），配好了不等于续跑了（框架不代为 `Resume`，见 §8.2）——什么时候让一个 cell 干活、什么时候让挂起的轮次继续，都是宿主的调度权
22. **两个阈值别混，两种发送别混**：`DirectConfig{Floor}` 断流（边留着、`ErrWeakSynapse`、不计数），`Prune` 的 `weightFloor` 删边；`Effect.Send` 有回程（一个收件人、一次挂起），`SkillIndex.FanOut` 没有回程（一个能力可对应零到多个 cell，逐目标报告）

## 10. 相关文档

| 文档 | 位置 | 内容 |
|------|------|------|
| 契约权威 | [host-integration.md](host-integration.md) | 所有接口签名、字段语义、事件序列、陷阱清单 |
| 协议映射 | [protocols.md](protocols.md) | MCP / A2A / AGENTS.md / Authority / 长时任务状态外化 |
| 动态接线 | [wiring.md](host-integration.md#51-动态接线-replace运行时换器官) | `Replace` 运行时换端口 |
