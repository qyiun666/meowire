// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// render_test.go — Prompt → request rendering units (pure functions, no
// I/O): message assembly order and roles, tool schema mapping, sampling
// propagation, slot texts, and the verbatim Context pass-through.

package openai

import (
	"encoding/json"
	"strings"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

func TestRenderMessages(t *testing.T) {
	p := &meowire.Prompt{
		System:   "sys",
		Identity: "你是 meow，角色 assistant，语气温暖",
		Methods:  []meowire.MethodSpec{{Name: "m1", Desc: "d1"}},
		State:    "thinking",
		Plan:     "p1",
		Context:  []string{"c1", "c2"},
		Input:    "hello",
	}
	msgs := renderMessages(p)
	want := []struct{ role, content string }{
		{roleSystem, "sys"},
		{roleSystem, "你是 meow，角色 assistant，语气温暖"},
		{roleSystem, "可用方法：m1（d1）"},
		{roleSystem, "当前状态：thinking。当前计划：p1"},
		{roleUser, "c1"},
		{roleUser, "c2"},
		{roleUser, "hello"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages count = %d, want %d", len(msgs), len(want))
	}
	for i, w := range want {
		if msgs[i].Role != w.role || msgs[i].Content != w.content {
			t.Errorf("messages[%d] = %+v, want role=%q content=%q", i, msgs[i], w.role, w.content)
		}
	}
}

// TestRenderMessagesEmptySystem a blank System adds no message (real
// gateways return 400 for messages with missing content).
func TestRenderMessagesEmptySystem(t *testing.T) {
	msgs := renderMessages(&meowire.Prompt{Input: "hello"})
	if len(msgs) != 1 {
		t.Fatalf("messages count = %d, want 1 (Input only)", len(msgs))
	}
	if msgs[0].Role != roleUser || msgs[0].Content != "hello" {
		t.Errorf("messages[0] = %+v, want user/hello", msgs[0])
	}
}

// TestRenderMessagesBounds Bounds renders as a system slot (after State);
// blank adds nothing.
func TestRenderMessagesBounds(t *testing.T) {
	p := &meowire.Prompt{
		System: "sys",
		Bounds: "  Execution boundary: workspace /tmp/ws; tools: bash  ",
		Input:  "hello",
	}
	msgs := renderMessages(p)
	if len(msgs) != 3 {
		t.Fatalf("messages count = %d, want 3 (System+Bounds+Input)", len(msgs))
	}
	if msgs[1].Role != roleSystem || msgs[1].Content != "Execution boundary: workspace /tmp/ws; tools: bash" {
		t.Errorf("messages[1] = %+v, want trimmed Bounds system slot", msgs[1])
	}
	if got := renderMessages(&meowire.Prompt{Input: "hello"}); len(got) != 1 {
		t.Errorf("blank Bounds messages count = %d, want 1", len(got))
	}
}

// TestRenderMessages_ToolResults the structured tool-feedback track renders
// after Context and before Input: "[tool-result name] result" /
// "[tool-result name] error: err". Empty results also render (fact feedback).
func TestRenderMessages_ToolResults(t *testing.T) {
	p := &meowire.Prompt{
		System:  "sys",
		Context: []string{"c1"},
		ToolResults: []meowire.ToolResult{
			{Name: "bash", Result: "ok"},
			{Name: "file_read", Err: "no such file"},
			{Name: "meow_search_memory", Result: ""},
		},
		Input: "hi",
	}
	msgs := renderMessages(p)
	want := []struct{ role, content string }{
		{roleSystem, "sys"},
		{roleUser, "c1"},
		{roleUser, "[tool-result bash] ok"},
		{roleUser, "[tool-result file_read] error: no such file"},
		{roleUser, "[tool-result meow_search_memory] "},
		{roleUser, "hi"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages count = %d, want %d", len(msgs), len(want))
	}
	for i, w := range want {
		if msgs[i].Role != w.role || msgs[i].Content != w.content {
			t.Errorf("messages[%d] = %+v, want role=%q content=%q", i, msgs[i], w.role, w.content)
		}
	}
}

// TestRenderMessages_ContextVerbatim the Context text track passes through
// byte-identical: the framework produces its sandbox-denial lines in final
// form and the renderer does no pattern rewriting.
func TestRenderMessages_ContextVerbatim(t *testing.T) {
	p := &meowire.Prompt{
		Input:   "hi",
		Context: []string{"[bash] ls", "[sandbox-denied: 越界]", "普通上下文"},
	}
	msgs := renderMessages(p)
	for i, want := range p.Context {
		if msgs[i].Content != want {
			t.Errorf("Context[%d] = %q, want verbatim %q", i, msgs[i].Content, want)
		}
	}
}

// TestRenderMessages_ToolResultsEmpty no ToolResults adds no message (nil
// and empty are equivalent).
func TestRenderMessages_ToolResultsEmpty(t *testing.T) {
	msgs := renderMessages(&meowire.Prompt{System: "sys", Context: []string{"c1"}, Input: "hi"})
	if len(msgs) != 3 {
		t.Fatalf("messages count = %d, want 3 (System+Context+Input)", len(msgs))
	}
	if got := renderMessages(&meowire.Prompt{Input: "hi", ToolResults: []meowire.ToolResult{}}); len(got) != 1 {
		t.Errorf("empty ToolResults messages count = %d, want 1", len(got))
	}
}

// TestRenderMessages_Reflection both the presence and the absence of the
// [Reflection] slot are load-bearing: rendered when set, never an empty
// ghost segment when not.
func TestRenderMessages_Reflection(t *testing.T) {
	p := basePrompt()
	p.Reflection = "上一轮失败：忘了先建目录"
	msgs := renderMessages(p)
	if msgs[len(msgs)-2].Content != "[Reflection] 上一轮失败：忘了先建目录" {
		t.Errorf("reflection slot = %q, want prefixed line", msgs[len(msgs)-2].Content)
	}
	for _, m := range renderMessages(basePrompt()) {
		if strings.Contains(m.Content, "Reflection") {
			t.Fatalf("unset Reflection must not render: %s", m.Content)
		}
	}
}

func TestRenderChatTools(t *testing.T) {
	specs := []meowire.ToolSpec{
		{Name: "calc", Desc: "计算器", Input: `{"type":"object","properties":{"expr":{"type":"string"}}}`},
		{Name: "noinput", Desc: "无参工具"},
	}
	tools := renderChatTools(specs)
	if len(tools) != 2 {
		t.Fatalf("tools count = %d, want 2", len(tools))
	}
	if tools[0].Type != "function" || tools[0].Function.Name != "calc" || tools[0].Function.Description != "计算器" {
		t.Errorf("tools[0] = %+v", tools[0])
	}
	if string(tools[0].Function.Parameters) != `{"type":"object","properties":{"expr":{"type":"string"}}}` {
		t.Errorf("tools[0].Parameters = %s", tools[0].Function.Parameters)
	}
	if tools[1].Function.Parameters != nil {
		t.Errorf("tools[1].Parameters = %v, want nil (no Input)", tools[1].Function.Parameters)
	}
	if renderChatTools(nil) != nil {
		t.Error("renderChatTools(nil) must be nil")
	}
}

// TestRenderChatTools_WireShape the chat tool definition must serialize to
// the OpenAI wire shape (hand-rolled JSON as the oracle).
func TestRenderChatTools_WireShape(t *testing.T) {
	b, err := json.Marshal(renderChatTools([]meowire.ToolSpec{{Name: "calc", Desc: "d", Input: `{"type":"object"}`}}))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"function","function":{"name":"calc","description":"d","parameters":{"type":"object"}}}]`
	if string(b) != want {
		t.Errorf("tool JSON = %s, want %s", b, want)
	}
}

func TestRenderMethods(t *testing.T) {
	cases := []struct {
		name    string
		methods []meowire.MethodSpec
		want    string
	}{
		{name: "empty renders nothing", methods: nil, want: ""},
		{name: "semicolon separated", methods: []meowire.MethodSpec{
			{Name: "m1", Desc: "d1"},
			{Name: "m2", Desc: "d2"},
		}, want: "可用方法：m1（d1）；m2（d2）"},
		{name: "no desc, no brackets", methods: []meowire.MethodSpec{{Name: "m1"}}, want: "可用方法：m1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderMethods(c.methods); got != c.want {
				t.Errorf("renderMethods(%v) = %q, want %q", c.methods, got, c.want)
			}
		})
	}
}

// ---- responses-side rendering ----

func TestRenderResponses(t *testing.T) {
	p := &meowire.Prompt{
		System:   "sys-baseline",
		Identity: "我叫小咪",
		Methods:  []meowire.MethodSpec{{Name: "m1", Desc: "d1"}},
		State:    "thinking",
		Plan:     "plan-x",
		Bounds:   "只读",
		Context:  []string{"ctx-1", "ctx-2"},
		Input:    "hi",
		Tools: []meowire.ToolSpec{
			{Name: "bash", Desc: "执行命令", Input: `{"type":"object","properties":{"cmd":{"type":"string"}}}`},
		},
	}
	req := renderResponses(Config{Model: "m-test"}, p)
	if req.Model != "m-test" {
		t.Errorf("Model = %q, want m-test", req.Model)
	}
	// instructions merge all system-side slots (System → Identity → Methods → State/Plan → Bounds)
	for _, want := range []string{"sys-baseline", "我叫小咪", "可用方法：m1（d1）", "当前状态：thinking", "当前计划：plan-x", "只读"} {
		if !strings.Contains(req.Instructions, want) {
			t.Errorf("Instructions missing %q, got %q", want, req.Instructions)
		}
	}
	if len(req.Input) != 3 { // ctx-1 + ctx-2 + input
		t.Fatalf("input messages = %d, want 3", len(req.Input))
	}
	if req.Input[2].Role != roleUser {
		t.Errorf("last input Role = %q, want user", req.Input[2].Role)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("Tools = %d, want 1", len(req.Tools))
	}
	// inline function shape: name/parameters/description flattened next to type
	b, _ := json.Marshal(req.Tools[0])
	s := string(b)
	for _, want := range []string{`"name"`, `"bash"`, `"description"`, `"parameters"`} {
		if !strings.Contains(s, want) {
			t.Errorf("tool JSON missing %s, got %s", want, s)
		}
	}
}

func TestRenderResponses_ToolResults(t *testing.T) {
	p := &meowire.Prompt{
		System:  "sys",
		Context: []string{"ctx-1"},
		ToolResults: []meowire.ToolResult{
			{Name: "bash", Result: "ok"},
			{Name: "file_read", Err: "boom"},
		},
		Input: "hi",
	}
	msgs := renderResponses(Config{Model: "m-test"}, p).Input
	if len(msgs) != 4 { // ctx-1 + 2 tool results + input
		t.Fatalf("input messages = %d, want 4", len(msgs))
	}
	want := []string{"ctx-1", "[tool-result bash] ok", "[tool-result file_read] error: boom", "hi"}
	for i, w := range want {
		if len(msgs[i].Content) == 0 || msgs[i].Content[0].Text != w {
			t.Errorf("input[%d] = %+v, want text %q", i, msgs[i].Content, w)
		}
	}
}

func TestRenderResponses_NoTools(t *testing.T) {
	req := renderResponses(Config{Model: "m-test"}, &meowire.Prompt{System: "s", Input: "hi"})
	if len(req.Tools) != 0 {
		t.Errorf("Tools = %+v, want empty (zero carried without definitions)", req.Tools)
	}
}

// TestRenderCarriesSampling the configured sampling must reach the wire on
// both paths, and zero config must send nothing (these protocol fields
// cannot express 0 vs unset).
func TestRenderCarriesSampling(t *testing.T) {
	cfg := Config{Model: "m", Sampling: Sampling{Temperature: 0.33, TopP: 0.77, MaxTokens: 2048}}

	creq := renderChat(cfg, basePrompt())
	if creq.Temperature != 0.33 || creq.TopP != 0.77 || creq.MaxTokens != 2048 {
		t.Fatalf("chat sampling not carried: %+v", creq)
	}
	zero := renderChat(Config{Model: "m"}, basePrompt())
	if zero.Temperature != 0 || zero.TopP != 0 || zero.MaxTokens != 0 {
		t.Fatalf("zero config must not send sampling: %+v", zero)
	}

	rreq := renderResponses(cfg, basePrompt())
	if rreq.Temperature == nil || *rreq.Temperature != 0.33 {
		t.Fatalf("responses temperature not carried: %+v", rreq.Temperature)
	}
	if rreq.TopP == nil || *rreq.TopP != 0.77 {
		t.Fatalf("responses top_p not carried: %+v", rreq.TopP)
	}
	if rreq.MaxOutputTokens != 2048 {
		t.Fatalf("responses max_output_tokens not carried: %+v", rreq.MaxOutputTokens)
	}
	rzero := renderResponses(Config{Model: "m"}, basePrompt())
	if rzero.Temperature != nil || rzero.TopP != nil || rzero.MaxOutputTokens != 0 {
		t.Fatalf("zero config must not send responses sampling: %+v", rzero)
	}
}
