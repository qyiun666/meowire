// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// chat.go — Chat Completions wire (once + stream). The wire types mirror
// the OpenAI protocol exactly — field names are the contract, and the
// package tests pin them with hand-written JSON so a tag regression cannot
// slip through.

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

const chatPath = "/chat/completions"

// ---- wire types (request marshalling / response unmarshalling) ----

type chatRequest struct {
	Model         string             `json:"model"`
	Messages      []chatMessage      `json:"messages"`
	Tools         []chatTool         `json:"tools,omitempty"`
	Temperature   float32            `json:"temperature,omitempty"`
	TopP          float32            `json:"top_p,omitempty"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatTool struct {
	Type     string     `json:"type"` // "function"
	Function chatToolFn `json:"function"`
}

type chatToolFn struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"` // always serialized (nil → null)
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Message chatRespMessage `json:"message"`
}

type chatRespMessage struct {
	Content   string             `json:"content"`
	ToolCalls []chatDoneToolCall `json:"tool_calls,omitempty"`
}

type chatDoneToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type"`
	Function chatFuncCall `json:"function"`
}

type chatFuncCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta chatDelta `json:"delta"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage,omitempty"`
}

type chatDelta struct {
	Content          string              `json:"content,omitempty"`
	ReasoningContent string              `json:"reasoning_content,omitempty"` // deepseek-reasoner reasoning stream
	ToolCalls        []chatDeltaToolCall `json:"tool_calls,omitempty"`
}

type chatDeltaToolCall struct {
	Index    *int         `json:"index,omitempty"` // nil on gateways that omit it (arrival-order fallback)
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type"`
	Function chatFuncCall `json:"function"`
}

// ---- once ----

func (t *Thinker) chatOnce(ctx context.Context, req chatRequest) (*meowire.Decision, error) {
	resp, err := postJSON[chatResponse](ctx, t.http, chatPath, req)
	if err != nil {
		return nil, fmt.Errorf("openai: llm call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: llm returned empty choices")
	}
	msg := resp.Choices[0].Message
	dec := &meowire.Decision{Text: msg.Content}
	for _, tc := range msg.ToolCalls {
		dec.ToolCalls = append(dec.ToolCalls, meowire.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}
	dec.Usage = chatUsageOf(resp.Usage)
	return dec, nil
}

// ---- stream ----

func (t *Thinker) chatStream(ctx context.Context, req chatRequest) (*meowire.Decision, error) {
	req.Stream = true
	req.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	resp, err := t.http.post(ctx, chatPath, req, true)
	if err != nil {
		return nil, fmt.Errorf("openai: start stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("openai: start stream: %w", statusError(resp))
	}
	defer resp.Body.Close()

	sse := newSSEReader(resp.Body)
	var (
		sb    strings.Builder
		calls accToolCalls
		usage *meowire.Usage
	)
	for {
		payload, err := sse.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("openai: stream receive: %w", err)
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return nil, fmt.Errorf("openai: stream chunk decode: %w", err)
		}
		// the usage chunk ships an empty choices array — content parsing
		// must tolerate that without touching nil slots
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				sb.WriteString(delta.Content)
				t.emit(ctx, ChunkText, delta.Content)
			}
			// reasoning token deltas: separate kind, never mixed into the body
			if delta.ReasoningContent != "" {
				t.emit(ctx, ChunkReasoning, delta.ReasoningContent)
			}
			calls.accum(delta.ToolCalls)
		}
		if chunk.Usage != nil {
			usage = chatUsageOf(chunk.Usage)
		}
	}
	return &meowire.Decision{Text: sb.String(), ToolCalls: calls.finish(), Usage: usage}, nil
}

// chatUsageOf converts a chat wire usage; all-zero (server sent no
// accounting) yields nil = skip the billing event (framework semantics).
func chatUsageOf(u *chatUsage) *meowire.Usage {
	if u == nil || (u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0) {
		return nil
	}
	return &meowire.Usage{Prompt: u.PromptTokens, Completion: u.CompletionTokens, Total: u.TotalTokens}
}
