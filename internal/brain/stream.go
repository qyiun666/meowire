// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// stream.go — the SSE path. Both transports end as one *openai.ChatCompletion
// (the accumulator embeds it), so the folding back has a single shape.
package brain

import (
	"context"
	"errors"

	"github.com/openai/openai-go/v3"
)

// streamCompletion consumes the SSE stream, pushes text deltas to the sink as
// they arrive (before the output membrane rules — stream-and-correct is the
// sink contract's whole point), and returns the accumulated completion.
// IncludeUsage must be set explicitly or the wire carries no usage and the
// round silently misses its EventUsage. Tool-call fragment merging (by the
// delta index) is the accumulator's job, not ours.
func streamCompletion(ctx context.Context, client openai.Client, params openai.ChatCompletionNewParams, sink Sink) (*openai.ChatCompletion, error) {
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	stream := client.Chat.Completions.NewStreaming(ctx, params)
	// Close on every exit: the accumulator-refusal branch returns mid-loop,
	// and the SSE contract says an iteration stopped before Next returns
	// false must Close (closeOnce makes the normal path's close a no-op).
	defer stream.Close()
	acc := openai.ChatCompletionAccumulator{}
	for stream.Next() {
		chunk := stream.Current()
		if !acc.AddChunk(chunk) {
			return nil, errors.New("brain: stream chunk not incorporated by the accumulator")
		}
		// The final chunk carries only usage; Choices is empty.
		if sink != nil && len(chunk.Choices) > 0 {
			if delta := chunk.Choices[0].Delta.Content; delta != "" {
				sink(delta)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, wrapErr(err, "chat completion")
	}
	return &acc.ChatCompletion, nil
}
