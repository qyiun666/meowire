// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// decision.go — ChatCompletion → Decision.
package brain

import (
	"errors"

	"github.com/openai/openai-go/v3"

	"github.com/qyiun666/meowire/internal/nerve"
)

// decisionOf folds one completion back into the loop's shape. Usage stays nil
// when the wire carried zeros — the event stream then reports nothing instead
// of a fake EventUsage. Tool-call arguments pass through as the JSON text the
// model produced; judging them is downstream's job.
func decisionOf(c *openai.ChatCompletion) (*nerve.Decision, error) {
	if len(c.Choices) == 0 {
		return nil, errors.New("brain: completion returned no choices")
	}
	message := c.Choices[0].Message
	dec := &nerve.Decision{Text: message.Content}
	for _, tc := range message.ToolCalls {
		dec.ToolCalls = append(dec.ToolCalls, nerve.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}
	if u := c.Usage; u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 {
		dec.Usage = &nerve.Usage{
			Prompt:     int(u.PromptTokens),
			Completion: int(u.CompletionTokens),
			Total:      int(u.TotalTokens),
		}
	}
	return dec, nil
}
