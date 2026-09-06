// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// responses.go — Responses API wire (once + stream). The stream consumes
// semantic SSE events (output_text.delta / reasoning_text.delta /
// function_call_arguments.delta / output_item.{added,done} / completed)
// and terminates on connection close (EOF) — there is no [DONE].

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	meowire "github.com/qyiun666/meowire/api"
)

const responsesPath = "/responses"

// Semantic stream event types (OpenAI Responses wire).
const (
	evOutputTextDelta = "response.output_text.delta"
	evReasoningDelta  = "response.reasoning_text.delta"
	evItemAdded       = "response.output_item.added"
	evItemDone        = "response.output_item.done"
	evArgsDelta       = "response.function_call_arguments.delta"
	evCompleted       = "response.completed"
	evFailed          = "response.failed"
	evError           = "response.error"
	evIncomplete      = "response.incomplete"
)

// ---- wire types ----

type respRequest struct {
	Model           string      `json:"model"`
	Instructions    string      `json:"instructions,omitempty"`
	Input           []respInput `json:"input"`
	Tools           []respTool  `json:"tools,omitempty"`
	Stream          bool        `json:"stream,omitempty"`
	Temperature     *float32    `json:"temperature,omitempty"`
	TopP            *float32    `json:"top_p,omitempty"`
	MaxOutputTokens int         `json:"max_output_tokens,omitempty"`
}

type respInput struct {
	Type    string     `json:"type"` // "message"
	Role    string     `json:"role"`
	Content []respText `json:"content"`
}

type respText struct {
	Type string `json:"type"` // "input_text"
	Text string `json:"text"`
}

type respTool struct {
	Type        string          `json:"type"` // "function" (inline function shape)
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"` // always serialized (nil → null)
}

type respResponse struct {
	Status string     `json:"status,omitempty"`
	Error  *respError `json:"error,omitempty"`
	Output []any      `json:"output"`
	Usage  *respUsage `json:"usage,omitempty"`
}

type respError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type respUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// respOutputItem carries the fields shared by output item variants; the
// top-level output stays []any so new variants pass through without a
// library release.
type respOutputItem struct {
	ID        string        `json:"id,omitempty"`
	Type      string        `json:"type"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Content   []respContent `json:"content,omitempty"`
}

type respContent struct {
	Type string `json:"type"` // "output_text"
	Text string `json:"text"`
}

type respEvent struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta,omitempty"`
	OutputIndex int             `json:"output_index,omitempty"`
	Item        *respOutputItem `json:"item,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
	Response    *respResponse   `json:"response,omitempty"`
	Message     string          `json:"message,omitempty"`
	Error       *respError      `json:"error,omitempty"`
}

// ---- once ----

func (t *Thinker) respOnce(ctx context.Context, req respRequest) (*meowire.Decision, error) {
	resp, err := postJSON[respResponse](ctx, t.http, responsesPath, req)
	if err != nil {
		return nil, fmt.Errorf("openai: llm call (responses): %w", err)
	}
	if resp.Status != "completed" {
		if resp.Error != nil {
			return nil, fmt.Errorf("openai: responses status %s: %s (%s)%s", resp.Status, resp.Error.Message, resp.Error.Code, usageSoFar(respUsageOf(resp.Usage)))
		}
		return nil, fmt.Errorf("openai: responses status %s%s", resp.Status, usageSoFar(respUsageOf(resp.Usage)))
	}
	dec := &meowire.Decision{}
	for _, raw := range resp.Output {
		item, ok := respItemOf(raw)
		if !ok {
			continue
		}
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					dec.Text += part.Text
				}
			}
		case "function_call":
			dec.ToolCalls = append(dec.ToolCalls, meowire.ToolCall{
				ID:   firstNonEmpty(item.CallID, item.ID),
				Name: item.Name,
				Args: item.Arguments,
			})
		default:
			// reasoning / web_search_call etc.: never into the reply text,
			// never converted to tools
		}
	}
	dec.Usage = respUsageOf(resp.Usage)
	return dec, nil
}

// ---- stream ----

func (t *Thinker) respStream(ctx context.Context, req respRequest) (*meowire.Decision, error) {
	req.Stream = true
	resp, err := t.http.post(ctx, responsesPath, req, true)
	if err != nil {
		return nil, fmt.Errorf("openai: start stream (responses): %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("openai: start stream (responses): %w", statusError(resp))
	}
	defer resp.Body.Close()

	sse := newSSEReader(resp.Body)
	var (
		sb    strings.Builder
		calls accRespCalls
		usage *meowire.Usage
	)
	for {
		payload, err := sse.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("openai: stream receive (responses): %w", err)
		}
		var ev respEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return nil, fmt.Errorf("openai: stream event decode (responses): %w", err)
		}
		switch ev.Type {
		case evOutputTextDelta:
			sb.WriteString(ev.Delta)
			t.emit(ctx, ChunkText, ev.Delta)
		case evReasoningDelta:
			t.emit(ctx, ChunkReasoning, ev.Delta)
		case evItemAdded, evItemDone:
			if ev.Item != nil && ev.Item.Type == "function_call" {
				calls.bind(ev.OutputIndex, ev.Item)
				// done carries the complete arguments: some gateways skip the
				// per-segment deltas — the done payload wins (covers whatever
				// accumulated, idempotent)
				if ev.Type == evItemDone && ev.Item.Arguments != "" {
					calls.setArgs(ev.OutputIndex, ev.Item.Arguments)
				}
			}
		case evArgsDelta:
			calls.accum(ev.OutputIndex, ev.Arguments)
		case evCompleted:
			if ev.Response != nil {
				usage = respUsageOf(ev.Response.Usage)
			}
		case evFailed, evError, evIncomplete:
			// all three are abnormal endings (matching respOnce, which only
			// accepts completed): incomplete (e.g. max-output-tokens
			// truncation) is not silenced — the error text carries the usage
			// seen so far (the Thinker port has no usage-on-error channel,
			// so the error text is the delivery)
			var seen *meowire.Usage
			if ev.Response != nil {
				seen = respUsageOf(ev.Response.Usage)
			}
			msg := ev.Message
			if msg == "" && ev.Error != nil {
				msg = ev.Error.Message
			}
			return nil, fmt.Errorf("openai: responses stream event %s: %s%s", ev.Type, msg, usageSoFar(seen))
		}
	}
	return &meowire.Decision{Text: sb.String(), ToolCalls: calls.finish(), Usage: usage}, nil
}

// respItemOf re-parses one non-stream output element (top-level []any,
// restored item by item); unparseable variants are skipped — a new item
// shape must not block the whole response.
func respItemOf(raw any) (respOutputItem, bool) {
	b, err := json.Marshal(raw)
	if err != nil {
		return respOutputItem{}, false
	}
	var it respOutputItem
	if err := json.Unmarshal(b, &it); err != nil {
		return respOutputItem{}, false
	}
	return it, true
}

// firstNonEmpty returns the first non-empty string (function-call ID
// prefers call_id, falling back to the item id).
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// respUsageOf converts a responses wire usage; nil or all-zero (server
// sent no accounting) yields nil = skip the billing event (framework
// semantics, same as the chat path).
func respUsageOf(u *respUsage) *meowire.Usage {
	if u == nil || (u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0) {
		return nil
	}
	return &meowire.Usage{Prompt: u.InputTokens, Completion: u.OutputTokens, Total: u.TotalTokens}
}

// usageSoFar renders the accounting seen before a failure as an error-text
// suffix (" (usage so far: 10 prompt + 5 completion tokens)") — the Thinker
// port has no usage-on-error channel, so the error text is the delivery.
// nil usage (no accounting seen) renders nothing.
func usageSoFar(u *meowire.Usage) string {
	if u == nil {
		return ""
	}
	return fmt.Sprintf(" (usage so far: %d prompt + %d completion tokens)", u.Prompt, u.Completion)
}
