// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// render.go — Prompt → wire request rendering (pure functions, no I/O).
// The slot texts here are the framework's canonical presentation of its own
// prompt contract (System/Identity/Methods/State/Bounds/Context/
// ToolResults/Reflection/Input): the wording directly shapes loop quality,
// so it lives with the loop. Sandbox denial lines arrive in their final
// form ([sandbox-denied: ...], produced by the nerve loop) and pass through
// untouched.

package openai

import (
	"encoding/json"
	"strings"

	meowire "github.com/qyiun666/meowire/api"
)

const (
	roleSystem = "system"
	roleUser   = "user"
)

// ---- chat wire ----

func renderChat(cfg Config, p *meowire.Prompt) chatRequest {
	return chatRequest{
		Model:       cfg.Model,
		Messages:    renderMessages(p),
		Tools:       renderChatTools(p.Tools),
		Temperature: cfg.Sampling.Temperature,
		TopP:        cfg.Sampling.TopP,
		MaxTokens:   cfg.Sampling.MaxTokens,
	}
}

// renderMessages assembles: System → Identity → Methods → State/Plan →
// Bounds (system role; blank slots skipped — real gateways return 400 for
// messages with missing content) → Context → ToolResults → Reflection →
// Input (user role, one message each).
func renderMessages(p *meowire.Prompt) []chatMessage {
	msgs := make([]chatMessage, 0, 5+len(p.Context)+len(p.ToolResults))
	if s := strings.TrimSpace(p.System); s != "" {
		msgs = append(msgs, chatMessage{Role: roleSystem, Content: p.System})
	}
	if s := strings.TrimSpace(p.Identity); s != "" {
		msgs = append(msgs, chatMessage{Role: roleSystem, Content: s})
	}
	if s := renderMethods(p.Methods); s != "" {
		msgs = append(msgs, chatMessage{Role: roleSystem, Content: s})
	}
	if s := renderState(p); s != "" {
		msgs = append(msgs, chatMessage{Role: roleSystem, Content: s})
	}
	if s := strings.TrimSpace(p.Bounds); s != "" {
		msgs = append(msgs, chatMessage{Role: roleSystem, Content: s})
	}
	for _, c := range p.Context {
		msgs = append(msgs, chatMessage{Role: roleUser, Content: c})
	}
	for _, tr := range renderToolResults(p.ToolResults) {
		msgs = append(msgs, chatMessage{Role: roleUser, Content: tr})
	}
	if s := renderReflection(p); s != "" {
		msgs = append(msgs, chatMessage{Role: roleUser, Content: s})
	}
	msgs = append(msgs, chatMessage{Role: roleUser, Content: p.Input})
	return msgs
}

// renderMethods renders the built-in capability description as
// self-recognition text: "可用方法：m1（d1）；m2（d2）".
func renderMethods(ms []meowire.MethodSpec) string {
	var sb strings.Builder
	for i, m := range ms {
		if i > 0 {
			sb.WriteString("；")
		}
		sb.WriteString(m.Name)
		if m.Desc != "" {
			sb.WriteString("（")
			sb.WriteString(m.Desc)
			sb.WriteString("）")
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	return "可用方法：" + sb.String()
}

// renderState renders loop state and plan ("当前状态：thinking。当前计划：...").
func renderState(p *meowire.Prompt) string {
	var sb strings.Builder
	if p.State != "" {
		sb.WriteString("当前状态：")
		sb.WriteString(p.State)
	}
	if p.Plan != "" {
		if sb.Len() > 0 {
			sb.WriteString("。")
		}
		sb.WriteString("当前计划：")
		sb.WriteString(p.Plan)
	}
	return sb.String()
}

// renderReflection renders the per-round reflexion note (the [Reflection]
// slot): blank = unused, no tokens spent.
func renderReflection(p *meowire.Prompt) string {
	if s := strings.TrimSpace(p.Reflection); s != "" {
		return "[Reflection] " + s
	}
	return ""
}

// renderToolResults renders the structured tool-feedback track as plain
// text lines: "[tool-result name] result" / "[tool-result name] error:
// err". Empty results also render (an empty tool output is still a fact).
func renderToolResults(trs []meowire.ToolResult) []string {
	if len(trs) == 0 {
		return nil
	}
	out := make([]string, 0, len(trs))
	for _, tr := range trs {
		if tr.Err != "" {
			out = append(out, "[tool-result "+tr.Name+"] error: "+tr.Err)
		} else {
			out = append(out, "[tool-result "+tr.Name+"] "+tr.Result)
		}
	}
	return out
}

// renderChatTools maps ToolSpec → chat tool definitions (Input is a JSON
// Schema string; an empty schema marshals as null on the wire).
func renderChatTools(specs []meowire.ToolSpec) []chatTool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(specs))
	for _, s := range specs {
		fn := chatToolFn{Name: s.Name, Description: s.Desc}
		if s.Input != "" {
			fn.Parameters = json.RawMessage(s.Input)
		}
		out = append(out, chatTool{Type: "function", Function: fn})
	}
	return out
}

// ---- responses wire ----

// renderResponses assembles the Responses request: this wire has no system
// role, so the system-side slots merge into instructions ("\n\n" joined,
// blanks skipped); Context/ToolResults/Reflection/Input render as user
// input messages.
func renderResponses(cfg Config, p *meowire.Prompt) respRequest {
	var ins strings.Builder
	for _, s := range []string{p.System, strings.TrimSpace(p.Identity), renderMethods(p.Methods), renderState(p), p.Bounds} {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if ins.Len() > 0 {
			ins.WriteString("\n\n")
		}
		ins.WriteString(s)
	}
	input := make([]respInput, 0, 1+len(p.Context)+len(p.ToolResults))
	for _, c := range p.Context {
		input = append(input, respUserText(c))
	}
	for _, tr := range renderToolResults(p.ToolResults) {
		input = append(input, respUserText(tr))
	}
	if s := renderReflection(p); s != "" {
		input = append(input, respUserText(s))
	}
	input = append(input, respUserText(p.Input))
	req := respRequest{Model: cfg.Model, Instructions: ins.String(), Input: input}
	if tools := renderRespTools(p.Tools); len(tools) > 0 {
		req.Tools = tools
	}
	if v := cfg.Sampling.Temperature; v > 0 {
		req.Temperature = &v
	}
	if v := cfg.Sampling.TopP; v > 0 {
		req.TopP = &v
	}
	if v := cfg.Sampling.MaxTokens; v > 0 {
		req.MaxOutputTokens = v // value field with omitempty (unlike the two pointers)
	}
	return req
}

func respUserText(text string) respInput {
	return respInput{
		Type:    "message",
		Role:    roleUser,
		Content: []respText{{Type: "input_text", Text: text}},
	}
}

// renderRespTools maps ToolSpec → responses inline function tool
// definitions (name/description/parameters flattened next to type — same
// semantics as the chat-side definitions).
func renderRespTools(specs []meowire.ToolSpec) []respTool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]respTool, 0, len(specs))
	for _, s := range specs {
		t := respTool{Type: "function", Name: s.Name, Description: s.Desc}
		if s.Input != "" {
			t.Parameters = json.RawMessage(s.Input)
		}
		out = append(out, t)
	}
	return out
}
