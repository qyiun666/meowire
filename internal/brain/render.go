// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// render.go — Prompt → chat messages. Stateless by design: the kernel hands
// the whole truth of a round (its eleven fields), and this render rebuilds the
// protocol's tool pairing from ToolResults alone. The model's own prior
// narration is not in the Prompt, so it is not in the messages either —
// continuity across invocations is the Mem port's business, not the brain's.
package brain

import (
	"cmp"
	"strconv"
	"strings"

	"github.com/openai/openai-go/v3"

	"github.com/qyiun666/meowire/internal/nerve"
)

// messages builds the request's message list in conversation order: the
// system bundle first, the round's stimulus second — the stimulus is constant
// across an invocation's rounds, so the prefix stays stable and a provider's
// prefix cache keeps eating it — then the reconstructed assistant/tool pairs.
func messages(p *nerve.Prompt) []openai.ChatCompletionMessageParamUnion {
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemText(p)),
		openai.UserMessage(p.Input),
	}
	msgs = append(msgs, toolTurns(p.ToolResults)...)
	return msgs
}

// toolTurns rebuilds the pairing the protocol requires: every tool message
// must answer a tool_call on the assistant message right before it. A
// ToolResult's ID echoes the call the model issued, so one assistant message
// carrying every prior call, followed by one tool reply per result, is a
// valid self-paired shape. Denied calls never enter ToolResults — a denial
// rules through the Context track instead — so they are simply absent here
// and need no placeholder. Call arguments are not part of a ToolResult and
// are not reconstructed: the model re-reads its own intent from the call name
// and the feedback.
func toolTurns(results []nerve.ToolResult) []openai.ChatCompletionMessageParamUnion {
	if len(results) == 0 {
		return nil
	}
	calls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(results))
	for _, tr := range results {
		calls = append(calls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: tr.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tr.Name,
					Arguments: "{}",
				},
			},
		})
	}
	var msgs []openai.ChatCompletionMessageParamUnion
	msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{
			ToolCalls: calls,
		},
	})
	for _, tr := range results {
		// Err non-empty means the call failed; its text is the reply.
		msgs = append(msgs, openai.ToolMessage(cmp.Or(tr.Err, tr.Result), tr.ID))
	}
	return msgs
}

// systemText folds every text-bearing Prompt field into the one system
// message, fixed sections first: the kernel rewrites only the dynamic half
// between rounds, and a stable prefix is what a prefix cache eats. Empty
// sections are skipped whole — no titles without bodies.
func systemText(p *nerve.Prompt) string {
	type section struct{ title, body string }
	fixed := []section{
		{"Instructions", p.System},
		{"Identity", p.Identity},
		{"Capabilities", describeMethods(p.Methods)},
		{"Execution bounds", p.Bounds},
	}
	dynamic := []section{
		// Background carries the host base plus the framework's
		// "[sandbox-denied: ...]" rulings — the denials' only seat.
		{"Background", lines(p.Context)},
		{"Plan", p.Plan},
		{"Reflection", p.Reflection},
		{"Recalled memories", describeRecords(p.Memories)},
	}
	var b strings.Builder
	for _, s := range append(fixed, dynamic...) {
		if s.body == "" {
			continue
		}
		b.WriteString("## ")
		b.WriteString(s.title)
		b.WriteString("\n")
		b.WriteString(s.body)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// describeMethods renders the built-in capability list as descriptive text.
// It never enters the request's tools — Methods say "what you are", Tools say
// "what you may call now", and mixing them sends the model after tools that
// do not exist.
func describeMethods(methods []nerve.MethodSpec) string {
	parts := make([]string, 0, len(methods))
	for _, m := range methods {
		parts = append(parts, describeOne(m.Name, m.Desc, m.Input, m.Output))
	}
	return strings.Join(parts, "\n")
}

// describeRecords renders the recalled-memory track. Content is []byte and
// is read as UTF-8 here: the day the memory wire gains an encoding
// (compression, a serialized structure), the change lands in this file, not
// in the kernel.
func describeRecords(records []nerve.Record) string {
	parts := make([]string, 0, len(records))
	for _, r := range records {
		head := "- " + r.Key
		if r.Kind != "" {
			head += " [" + r.Kind + "]"
		}
		if r.Created != 0 {
			head += " @" + strconv.FormatInt(r.Created, 10)
		}
		parts = append(parts, head+"\n"+string(r.Content))
	}
	return strings.Join(parts, "\n")
}

// lines renders the background track, one bullet per entry.
func lines(entries []string) string {
	parts := make([]string, 0, len(entries))
	for _, l := range entries {
		parts = append(parts, "- "+l)
	}
	return strings.Join(parts, "\n")
}

// describeOne collapses one capability declaration to a line, empty fields
// skipped.
func describeOne(name, desc, input, output string) string {
	var b strings.Builder
	b.WriteString("- ")
	b.WriteString(name)
	if desc != "" {
		b.WriteString(": ")
		b.WriteString(desc)
	}
	for _, pair := range []struct{ label, value string }{{"input", input}, {"returns", output}} {
		if pair.value != "" {
			b.WriteString(" (")
			b.WriteString(pair.label)
			b.WriteString(" ")
			b.WriteString(pair.value)
			b.WriteString(")")
		}
	}
	return b.String()
}
